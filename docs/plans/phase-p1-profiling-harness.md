# Phase P1 — Profiling & leak-detection harness + baselines

- **Status:** draft
- **Owning subsystem(s):** `internal/bench` (new `profile/` rig), `internal/telemetry`, `cmd/stowage`, `internal/config`, `internal/api` (pprof admin listener), test-suite wiring across the goroutine-heavy packages
- **RFC sections:** §2.1 (P2 — fire-and-forget, supervised goroutine stages that drain on shutdown), §8.2 (concurrency posture), §11 (observability), §13/§14 (hardening/operations)
- **Depends on phases:** 03 (store seam), 04 (gateway seam), 05–08 (write path), 09–12 (read path + SLO rig), 14 (sweeps), h1 (`boot.StartPipeline`). This phase profiles the assembled system, so it depends on the whole launch track being present — but it is **orthogonal** to it (a productionization-style track, like D-067).
- **Informing briefs:** [01](../research/01-predecessor-python.md) (the Python predecessor's polling-worker-pool pain — the anti-pattern P2 exists to avoid), [02](../research/02-predecessor-ccmem.md) (the CC-memory predecessor's documented lock-contention pain — the lesson behind the sqlite dedicated-writer goroutine, D-022/RFC §8.2). Per `docs/research/INDEX.md`, briefs 01/02 carry the store/vindex **contention lessons** that motivate this phase.

## Goal

When this phase is done, Stowage has, for the first time, a **measured** picture of
its runtime resource behaviour rather than an asserted one. Specifically: (a) a
`net/http/pprof` surface reachable on a separate, off-by-default, auth-gated admin
listener; (b) `runtime`/`MemStats`/`NumGoroutine` sampling emitted through the
telemetry seam at idle and under load; (c) `go.uber.org/goleak` wired into the test
suite of every goroutine-launching package (advisory first); (d) a load+profile rig
under `internal/bench/profile/` that drives ingest + retrieve + sweeps concurrently
and captures CPU/heap/goroutine/block/mutex profiles, asserting **goroutine-count
stability** (boot → steady-state → post-drain) and **idle CPU/alloc ceilings**; and
(e) committed reference baselines in `eval/PROFILE.md`. The five binding properties —
above all P2's "supervised goroutine stages that drain on shutdown" — stop being
claims and become regression-gated measurements. This phase **builds the harness and
records baselines; it does not fix what it finds** — each leak or inefficiency the
harness surfaces lands as a scoped follow-up phase (P2, P3, …), gated by the baseline,
mirroring the eval continuous model (D-035).

## Brief findings incorporated

- **Brief 01 (Python predecessor).** Its 0.25 s embedding-queue polling worker pools
  are the explicit anti-pattern of P2/RFC §3 ("no pollers, no external workers"). The
  harness's idle workstream measures exactly the cost P2 was designed to remove —
  CPU/allocs with all sweeps/tickers running and **zero** traffic — so we can prove we
  did not reintroduce a polling tax.
- **Brief 02 (CC-memory predecessor).** Its documented lock-wait storms under
  concurrent writers are the reason the sqlite driver uses a dedicated writer goroutine
  (RFC §8.2, D-022). The backend-under-load workstream profiles that writer goroutine's
  contention and the Postgres pgx pool saturation directly — i.e. it verifies the
  contention lesson actually held in our implementation.

## Findings I'm departing from

- No brief or RFC section prescribes a profiling subsystem; §11 (observability) stops
  at Prometheus metrics + events. This phase **extends** observability into runtime
  resource profiling without contradicting anything — it is filed as **D-126**
  (ratifies the track, the harness-first structure, the pprof security posture, and the
  advisory-then-promote gate). No settled decision is reversed.
- The existing latency SLO (`make slo`, D-031/D-095) deliberately stays a
  reference-hardware, on-demand, latency-only gate. This phase does **not** fold
  resource profiling into the SLO rig — they answer different questions (p99 latency vs
  CPU/heap/goroutine resource behaviour) and have different cost profiles. The profiling
  rig is a sibling under `internal/bench/`, not a rider on `bench/slo`.

## Design

### Workstream A — pprof admin surface (security-first)

`net/http/pprof` handlers are mounted on a **dedicated** listener, never on the public
API mux. Rationale: the profiling endpoints leak internal state and allow CPU-burning
profile captures; they are an operator tool, not a public surface (CLAUDE.md §7 —
transport protections are set explicitly, never inherited).

- New config knob `server.pprof_listen` (string, **default empty = disabled**). When
  empty, no pprof listener is started and `serve` behaves exactly as today (the
  five-minute-rule zero-config invariant, D-034, is preserved). When set (e.g.
  `127.0.0.1:6060`), `cmd/stowage serve` starts a second `http.Server` and binds the
  `/debug/pprof/*` tree.
- **This mirrors the proven `server.mcp_listen` two-listener pattern (D-074,
  `cmd/stowage/main.go`):** a *separate* `http.Server` whose lifecycle (Shutdown) is
  wired into the same `serve` teardown as the API and MCP listeners, **deliberately not
  inheriting the REST middleware/timeouts.** pprof in particular must **not** inherit the
  API `WriteTimeout` — a 30 s CPU-profile capture would be truncated mid-stream — exactly
  the reasoning that justified the MCP listener's own server. The listener wiring
  therefore lives in `cmd/stowage/main.go` next to `mcpHTTP`, not on the API mux; it sets
  `ReadHeaderTimeout` but no `WriteTimeout`.
- The listener is **auth-gated** by the same constant-time API-key check as the admin
  API (reuse `internal/auth`) via a small handler wrapper around the pprof mux; a request
  without a valid admin key gets 401. It binds loopback by default in every profile;
  binding a non-loopback address requires the operator to set it explicitly (documented
  as a deliberate exposure).
- Knob-guardrail treatment (D-034): tuned default (empty), placement in every profile
  (`assistant`/`coding-agent`/`fleet` all default it empty), docs in the example config
  and this plan, and a smoke check.

### Workstream B — runtime sampling through the telemetry seam

A small `telemetry` sampler (ticker-driven, jittered, opt-in via a knob) reads
`runtime.NumGoroutine()` and `runtime.ReadMemStats` and emits them as Prometheus gauges
(`stowage_goroutines`, `stowage_heap_alloc_bytes`, `stowage_heap_objects`,
`stowage_gc_pause_seconds`) and a typed `events/v1` resource-sample event. It is
non-blocking and off the hot path. This gives the "is the platform healthy" feed
(RFC §11 usage-analytics framing) a resource dimension. Default sample interval is a
profile-defaulted knob; default OFF for `assistant`, ON at a coarse interval for
`fleet`.

The sampler is itself a ticker goroutine, so it is **wired into the `boot.Stack`
lifecycle**: started under the Stack, stopped and drained on `Stack.Close`, and it must
pass its own goleak check. The harness must not introduce the leak class it hunts — a
sampler that outlives `Close` would itself fail the goroutine-stability gate.

### Workstream C — goleak in the test suite (advisory → gate)

Each package that launches goroutines gets a `TestMain` that runs
`goleak.VerifyTestMain(m)` with documented, narrowly-scoped ignore-rules for known
framework goroutines (e.g. the sqlite writer, the pgx pool reaper). Target packages
(from the goroutine-launch inventory): `pipeline`, `lifecycle`, `boot`, `reconcile`,
`retrieval`, `trust`, `causal`, `episodes`, `proactive`, `scoring`, `traces`,
`mcpserver`, `gateway` (+ `bifrost`/`openaicompat`), `store/sqlitestore`, `vindex`.
**Advisory first** (D-126): a leak prints a diagnostic but does not fail CI until the
package's baseline is clean; promotion to a hard failure is a one-line flip per package
in a follow-up, tracked in `eval/PROFILE.md`. `go.uber.org/goleak` is already in
`go.sum` (transitive) — no new direct dependency surface beyond promoting it to a
direct require.

### Workstream D — the load+profile rig (`internal/bench/profile/`)

A `-tags=profile` test rig (sibling to `internal/bench/slo`) that:

1. Boots a full `boot.Stack` + `boot.StartPipeline` against a chosen backend (sqlite
   in-memory for the always-on CI cut; Postgres via `STOWAGE_TEST_PG_DSN` for the
   backend-under-load cut), with the `mock` gateway (no paid calls).
2. Drives a concurrent mixed workload — N ingest streams (write path → buffers →
   extract → reconcile → commit), M retrieve streams, with sweeps **running** — for a
   bounded duration.
3. Captures CPU, heap, goroutine, block, and mutex profiles to artifact files.
   **Block and mutex profiling are off by default in the Go runtime** — the rig must
   enable them in profile-mode via `runtime.SetBlockProfileRate` and
   `runtime.SetMutexProfileFraction` (and reset them after), or those two profiles come
   back empty. They are enabled only under `-tags=profile` / the pprof listener, never
   in the shipped steady state, because the sampling adds per-contention-event overhead.
4. Asserts the gates below.

**Goroutine-stability gate.** Sample `NumGoroutine` at three points: post-boot (S0),
steady-state under load (S1), and **post-drain** after `Stack.Close(ctx)` (which drains
the pipeline via `Drain` and closes the injection/emitter/sampler goroutines) plus a
settle window (S2). Assert `S2 ≤ S0 + ε` (drain returns to baseline — the P2 contract)
and that S1 is bounded by a configured ceiling (no unbounded fan-out). A monotonic climb
in `NumGoroutine` across repeated load cycles is the canonical leak signature and fails
the gate. The settle window retries until the count stabilises (bounded) so an in-flight
teardown goroutine doesn't read as a false positive.

**Idle gate (two signals, split by determinism).** With all sweeps/tickers running and
**zero** request traffic for a bounded window:

- *CI cut (deterministic):* assert **bytes-allocated and goroutine-count deltas** stay
  under configured ceilings. Allocations and goroutine counts are stable across machines,
  so this is the signal the always-on sqlite cut gates on — the "leaking via tickers /
  allocating at idle" check.
- *On-demand cut (noisy):* the **idle CPU-time** ceiling runs only under `make profile`
  (alongside the Postgres / long-duration cuts), never in the per-PR matrix — CPU time at
  idle is too noisy on shared CI runners to gate on (the same reasoning that keeps the SLO
  off the per-PR matrix, D-095).

Together these are the "are we burning CPU / leaking via tickers at idle" check the owner
called out, and the direct rebuttal to the brief-01 polling tax.

**Backend-under-load (Workstream D, Postgres + sqlite cuts).** Profile the pgx pool
(acquisition wait, saturation) and the sqlite dedicated-writer goroutine (queue depth,
serialization stalls) under the concurrent workload — verifying the RFC §8.2 contention
posture empirically.

All gate ceilings are **advisory** in this phase: the rig records measured numbers to
`eval/PROFILE.md` and only fails on a configured regression once a baseline is
committed (advisory-then-promote, D-126). The always-on CI cut (sqlite) is fast and
deterministic; the Postgres + long-duration cuts are on-demand like `make slo`.

### Targets the rig probes first (from the decision log)

The harness is generic, but the first investigations target the known suspects so the
follow-up fix-phases have somewhere to start:

- **vindex sidecar** — per-tenant lazy-build vs incremental-upsert goroutine
  interleaving; the `refreshSidecar` path reachable only under specific interleavings.
- **Sweeps as periodic goroutines** (lifecycle/episodes/causal/trust/proactive) — the
  idle-CPU suspects.
- **Pipeline stages** and the **boot supervisor drain** — the P2 contract surface.
- **Gateway batching** — fan-out/fan-in goroutine accounting.

## Files added or changed

```text
internal/bench/profile/profile_test.go      # the load+profile rig (-tags=profile)
internal/bench/profile/workload.go          # mixed ingest/retrieve/sweep driver
internal/telemetry/runtime_sampler.go       # MemStats/NumGoroutine sampler + gauges
internal/telemetry/runtime_sampler_test.go
internal/api/pprof.go                        # auth wrapper around the pprof mux (reuses internal/auth)
cmd/stowage/main.go                          # start/Shutdown pprof listener next to mcpHTTP (D-074 pattern)
internal/config/config.go                    # server.pprof_listen + sampler knobs
internal/config/profiles.go                  # profile placements (all default safe)
internal/<goroutine-pkgs>/main_test.go       # goleak.VerifyTestMain per package
eval/PROFILE.md                              # committed reference baselines
scripts/smoke/phase-p1.sh                    # smoke checks
docs/plans/phase-p1-profiling-harness.md     # this file
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `server.pprof_listen` | `""` (disabled) | Loopback `host:port` for the auth-gated pprof listener; empty = no listener (zero-config preserved). |
| `telemetry.runtime_sample_interval` | profile-defaulted (`0`=off for `assistant`; coarse for `fleet`) | Interval for the MemStats/NumGoroutine sampler; `0` disables. |

## Acceptance criteria (binding)

1. `server.pprof_listen` empty ⇒ `serve` starts no pprof listener and zero-config boot
   is unchanged (smoke); set ⇒ `/debug/pprof/` reachable **only** with a valid admin
   key, 401 otherwise (constant-time check); never mounted on the public API mux.
2. The runtime sampler emits `stowage_goroutines` + heap gauges and a typed resource
   event when enabled; is non-blocking; default-off for `assistant`.
3. `goleak.VerifyTestMain` is wired into every goroutine-launching package listed in
   Workstream C, advisory, with documented ignore-rules; `make test` stays green under
   `-race`.
4. The `-tags=profile` rig boots a full stack (sqlite cut), drives the concurrent
   workload, and writes CPU/heap/goroutine/block/mutex profile artifacts — with block and
   mutex sampling explicitly enabled (`SetBlockProfileRate`/`SetMutexProfileFraction`) so
   those two are non-empty, and reset afterward.
5. The goroutine-stability gate computes S0/S1/S2 (S2 after `Stack.Close(ctx)` + settle)
   and records them; **post-drain S2 ≤ S0 + ε** holds for the sqlite cut (if it does not,
   that is a leak finding filed for a P-series follow-up, and the baseline records it as a
   known gap rather than silently passing).
6. The idle gate's deterministic signals (idle alloc + goroutine delta) are asserted in
   the always-on CI cut; the noisy idle CPU-time ceiling is recorded under `make profile`
   only. Both are written to `eval/PROFILE.md`.
7. Reference baselines (idle heap, idle goroutines, goroutines @ N sessions, alloc/op on
   the ingest and retrieve hot paths) are committed in `eval/PROFILE.md` with a
   one-command reproduction.
8. New knobs ship with tuned defaults, placement in every profile, docs, and a smoke
   check (D-034). `make preflight` + `make drift-audit` + `make check-mirror` green.

## Smoke script

`scripts/smoke/phase-p1.sh`:

- `OK pprof disabled by default` — `serve` with empty `server.pprof_listen` exposes no `/debug/pprof`.
- `OK pprof requires admin key` — with it set, `/debug/pprof/` returns 401 without a key, 200 with.
- `OK runtime gauges registered` — `stowage_goroutines` present on the metrics endpoint when sampler on.
- `OK profile rig builds` — `go test -tags=profile -run xxx ./internal/bench/profile/` compiles.
- `OK goleak wired` — at least one package's `main_test.go` calls `goleak.VerifyTestMain`.
- `OK PROFILE.md present` — `eval/PROFILE.md` exists with the baseline table.
- `SKIP postgres profile cut` when `STOWAGE_TEST_PG_DSN` unset.

## Test plan

- **Unit:** the runtime sampler (gauge values move, non-blocking, disabled when interval 0);
  the pprof auth gate (401/200 table); config validation for the new knobs.
- **Golden:** the resource-sample event shape (`events/v1` contract).
- **Integration (§17):** the `-tags=profile` rig is itself a cross-subsystem integration
  test — real store + real pipeline + real sweeps + mock gateway, under `-race`, proving
  drain returns goroutines to baseline (a P2 failure mode). Postgres cut with a real DSN
  for the backend-under-load contention check.
- **Bench:** the rig captures `BenchmarkXxx`-style alloc/op on the ingest and retrieve
  hot paths feeding `eval/PROFILE.md` (run on demand via `make profile`, not a per-PR
  gate — like `make bench`/`make slo`).
- **Fuzz:** none (no new parse/decode surface).

## Risks & mitigations

- **pprof as an exposure.** Mitigated by off-by-default, loopback-default, admin-key
  gate, separate listener with explicit timeouts, and a smoke check that proves the
  gate bites. Documented as a deliberate operator exposure when bound non-loopback.
- **goleak flakiness on framework goroutines.** Mitigated by advisory-first rollout and
  narrowly-scoped, documented ignore-rules; promotion to a hard gate is per-package and
  deliberate, only after that package's baseline is clean.
- **Rig non-determinism / CI flakiness.** The always-on CI cut is sqlite + mock gateway,
  bounded duration, with advisory ceilings; the noisy long-duration and Postgres cuts
  are on-demand (`make profile`), kept out of the per-PR matrix exactly as the SLO gate
  is (D-095).
- **Scope creep into fixing.** Explicitly bounded: this phase **builds + baselines**;
  fixes are scoped P-series follow-ups gated by the baseline. Acceptance criterion 5
  makes "found a leak" a recorded finding, not a phase failure.

## Glossary additions

- **Profiling harness** — the `internal/bench/profile/` load+profile rig that drives a
  concurrent ingest/retrieve/sweep workload and captures CPU/heap/goroutine/block/mutex
  profiles, asserting goroutine-stability and idle ceilings.
- **Goroutine-stability gate** — the post-boot / steady-state / post-drain
  `NumGoroutine` triple-sample check; `post-drain ≤ post-boot + ε` is the P2
  drain-on-shutdown contract made measurable.
- **Idle gate** — the zero-traffic CPU/alloc ceiling check that proves sweeps and
  tickers do not impose a polling tax (the brief-01 anti-pattern rebuttal).
- **Resource sample** — the `events/v1` event + Prometheus gauges carrying
  `NumGoroutine`/`MemStats` readings emitted by the runtime sampler.

## Decisions filed

- **D-126** — Performance & resource hardening track; harness-first structure; pprof
  off-by-default auth-gated separate listener; advisory-then-promote gating; resource
  profiling is a sibling of (not folded into) the latency SLO.
