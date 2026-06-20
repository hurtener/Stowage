# Phase 21 — Hardening & launch (terminal v0.1 gate)

- **Status:** approved
- **Owning subsystem(s):** repo-wide — `internal/api` (HTTP hardening + DSAR surface
  audit), `internal/auth` / `internal/config` (secret + env-indirection audit),
  `internal/store` (scope-enforcement + cascade-delete audit), `Makefile` /
  `.github` (cross-compile matrix + checksums), `scripts/` (forbidden-names
  history sweep, five-minute smoke), `docs/` + repo root (external docs, CHANGELOG,
  LICENSE)
- **RFC sections:** §13 (security & privacy), §9.4 (the five-minute rule), §14
  (non-goals — the audit confirms we did not drift into them), §15 (this is the
  terminal gate the phasing names)
- **Depends on phases:** all (01–27 + the h1–h7 hardening track) — this is the
  terminal gate that runs after Phase 27 (master plan README §"Hardening & launch").
- **Informing briefs:** 06 (mempalace — "competitors win users in the first five
  minutes"; benchmark-led launch positioning), 02 (CC-mem — the 50+-knob
  "config paralysis" cautionary tale that the five-minute rule answers), 01 (the
  Python predecessor — surface-sprawl + the clean-room predecessor-hygiene rule
  the history sweep enforces).

## Goal

When this phase is done, Stowage is **launchable as a public v0.1 open-source
repository**: a repo-wide security pass has verified every §13 property holds (no
hardcoded secrets, store-layer scope enforcement, explicit HTTP hardening, DSAR
export + cascading delete, gateway-payload-only egress); the binary cross-compiles
CGo-free for darwin/linux × amd64/arm64 with published checksums; the repository
carries an Apache-2.0 LICENSE (OQ-5 resolved → D-097), external-audience docs, and
a Keep-a-Changelog CHANGELOG; a **full-git-history** forbidden-names sweep confirms
the Python predecessor's project name appears nowhere ever; and a scripted
**five-minute-rule** smoke proves a fresh machine reaches first-memory-stored-and-
retrieved with one secret env var in under five minutes. Nothing in this phase adds
a capability — it is the gate that proves the existing system is safe, portable,
documented, and adoptable.

## Brief findings incorporated

- **06 (mempalace): the first five minutes decide adoption.** The five-minute-rule
  smoke (§9.4) is a *binding acceptance criterion*, not a doc aspiration: a fresh
  environment, one secret (`STOWAGE_GATEWAY_API_KEY`), `stowage serve`, then an
  ingest + retrieve round-trip, all scripted and timed < 5 min.
- **06: benchmarks are the launch artifact.** The launch is gated on the SOTA
  benchmark report (RFC OQ-5, §12) — `eval/REPORT.md` + the operator-run public
  numbers. This phase does not re-run benchmarks (operator-run track); it confirms
  the report + reproduction commands are launch-ready and linked from the README.
- **02 (CC-mem): config paralysis kills adoption.** The security/hardening pass
  must not introduce new knobs; `stowage config explain` and the zero-config start
  are re-verified as the adoption surface, and the knob guardrail (D-034) is audited
  repo-wide (no undocumented knob shipped).
- **01 (Python predecessor): clean-room hygiene is absolute.** The predecessor's
  project name must appear **nowhere**, including deleted lines — so the sweep runs
  over the *entire git history*, not just the working tree (CLAUDE.md
  "Predecessor hygiene").

## Findings I'm departing from

- **None of substance.** The master-plan Phase-21 line item lists "security pass,
  docs, release matrix, public-repo audit, license (OQ-5), five-minute-rule smoke";
  this plan realizes exactly that, adding only the explicit decomposition into
  auditable criteria. The one settled question is **OQ-5 → Apache-2.0** (D-097),
  chosen for the permissive + patent-grant fit for Go infrastructure and the
  four-part ecosystem's interop (Portico/Harbor/Dockyard); the RFC's BSL-style
  alternative is recorded as considered-and-rejected in D-097.

## Design

This phase is **audit + artifact**, executed as a sequence of small PRs, each with
its own gate. No production behavior changes except where the security audit finds
a real defect (fixed in the same PR, with a regression test).

### 21.1 Security pass (§13) — a repo-wide audit with a checklist artifact

A reviewer (and an adversarial subagent pass) verifies each §13 property against
the code and records the result in `docs/security-audit.md` (the artifact), with
file:line evidence per item:

1. **No hardcoded secrets, anywhere (incl. tests).** Mechanical sweep
   (`scripts/drift-audit.sh` extended with a secret-pattern grep over the working
   tree) + manual review. Secrets use `env.VAR` indirection and fail closed at boot
   (re-verify `internal/config` validation + `internal/auth`).
2. **Scope enforcement in the store layer (P3).** Confirm no unscoped query API
   exists — every `Store` query builder takes a scope; the conformance suite already
   proves cross-tenant isolation. The audit records the enumeration.
3. **HTTP transport hardening is explicit** (timeouts, body limits, Origin/
   Content-Type, cross-origin on the SSE surface), never inherited from an SDK
   default. Audit `internal/api` server construction + the co-mounted MCP listener
   (D-074).
4. **API keys compared in constant time; never logged.** Re-verify `internal/auth`.
5. **DSAR-style export + cascading delete per (tenant, user)** (§13). Audit the
   retention/DSAR cascade path: confirm it exists and is the ONLY path that
   deletes/mutates verbatim records (P1), is scope-enforced, and emits events. If a
   gap is found, it is a real finding fixed in this phase (with an integration test).
6. **Gateway payloads are the only data leaving the box.** Confirm no package
   outside `internal/gateway` makes outbound network calls (the P5 lint + a grep
   audit); redaction-profile hooks are documented as the v1.x extension point.

Output: `docs/security-audit.md` with a PASS/finding line per item + the §13/§14
checklist marked repo-wide.

### 21.2 The five-minute-rule smoke (§9.4)

`scripts/smoke/phase-21-fiveminute.sh`: from a clean state (temp dir, fresh sqlite),
with exactly one secret env var set to the **mock** gateway (CI-safe; no paid call):

1. Build (or use `bin/stowage`), start `stowage serve` on an ephemeral port.
2. `POST /v1/records` (ingest), flush, wait for a memory, `POST /v1/retrieve`,
   assert the memory comes back.
3. Assert wall-clock from server-start to first-retrieve is well under five minutes
   (the CI assertion is generous — the point is "it works with one env var and no
   config file," not a latency benchmark).
4. `stowage config explain` prints the effective config + provenance (default |
   profile | scope | env) — the anti-config-paralysis surface (§9.4).

This is the binding adoption criterion; it runs in `make preflight` like every other
smoke (mock gateway, deterministic).

### 21.3 Cross-compile matrix + checksums (release artifacts)

A `make release` target (and a `.github` release workflow, not a per-PR gate)
cross-compiles the CGo-free static binary for **darwin/linux × amd64/arm64**
(`CGO_ENABLED=0 GOOS/GOARCH` matrix), emits `SHA256SUMS`, and verifies each artifact
runs `stowage version`. The per-PR CI gains a `cross-build` job that compiles all
four targets (build-only, fast) so a CGo regression is caught at PR time. No artifact
publishing in this repo (that is the release workflow's job, run on a tag).

### 21.4 Public-repo audit: LICENSE + full-history forbidden-names sweep

1. **LICENSE** — add `Apache-2.0` (`LICENSE` file + SPDX headers policy documented;
   `NOTICE` if required). OQ-5 → D-097.
2. **Forbidden-names history sweep** — `scripts/forbidden-history-sweep.sh` greps the
   **entire git history** (`git log -p --all` / `git grep` over all refs) for the
   Python predecessor's project name and the internal CC-memory system name; any hit
   is a launch blocker. Wire it into `drift-audit` (working tree, every PR) and as a
   standalone history pass run in this phase + the release workflow. (Working-tree
   forbidden-names is already in `drift-audit`; this adds the history dimension.)
3. **Repo hygiene** — `.gitignore` covers `.env`/secrets/results; no committed
   secrets in history (covered by 21.1's sweep over history); `README` + `CONTRIBUTING`
   present for an external audience.

### 21.6 Full-cycle LIVE acceptance script (real models, 100% of consumer routes)

The launch-readiness counterpart to the five-minute smoke (21.2, mock + minimal):
a single operator-run script that boots `stowage serve` against the **real** gateway
(bifrost → OpenRouter: embed + complete + rerank all active, D-075) and drives a
realistic multi-session usage cycle, asserting correct behavior end-to-end across
**every consumer-facing route** — HTTP, MCP-over-HTTP, and `stowage` CLI.

`scripts/acceptance/full-cycle-live.sh` (operator-run; sources `.env` for
`OPENROUTER_API_KEY`; **never CI**, never prints the key):

1. **Boot.** Start `stowage serve` with the bifrost stack (the `fullmode_test.go`
   env block) on ephemeral API + MCP ports; `Probe` the gateway fail-fast; mint a
   runtime API key (`/v1/keys` or config) for the auth'd requests.
2. **Simulate conversations.** Ingest a scripted multi-session, multi-topic
   conversation (a user's preferences, decisions, a correction that should
   supersede, a gotcha) via `POST /v1/records` with `buffer_key`/`session_id`;
   explicit `POST /v1/buffers/{key}/flush`; **wait** for real extraction to commit
   memories (poll active count / quiescence).
3. **Retrieve + assert knowledge.** `POST /v1/retrieve` (balanced and precise/rerank
   profiles) for several queries; **assert** the expected facts come back, citations
   resolve, the support summary is present, and a contradiction was superseded (the
   correction wins). Assert graceful fields (degraded=false with the gateway up).
4. **Exercise the full consumer surface** (assert 2xx + shape on each):
   records ingest, buffers flush, retrieve (both profiles), feedback (use/save/
   fail/noise + wrong_citation), memory get + **drill-down to verbatim**, memory
   assert, topics CRUD, scopes settings (`config explain`), episodes
   (list/get/window/arc), playbook (`GET /v1/playbook`), verify (claim entailment),
   traces (reasoning-trace + export), grants/groups (team share within tenant),
   suggestions (proactive offer accept/dismiss), events SSE stream, health/version.
5. **Same capabilities over MCP-over-HTTP** (the co-mounted listener, D-074):
   JSON-RPC `initialize` + `tools/list` + `tools/call` for the memory tools
   (ingest/retrieve/feedback/episodes/playbook/verify/...), asserting parity with
   the HTTP results (the D-067 one-core promise, exercised live).
6. **Forgetting + lifecycle.** Trigger/observe a supersede + a rollback round-trip;
   assert the chain and the event trail.
7. **Report.** Print a per-route PASS/FAIL table + the models used + wall time; exit
   non-zero on any failed assertion. Each step has bounded waits + retries (real LLM
   latency), like real usage.

This is the deliverable that proves the *whole system behaves correctly with all
internal LLM/embedding/rerank models active* — the launch acceptance gate an operator
runs before tagging v0.1. It reuses the HTTP/MCP routes only (no test-only hooks), so
it doubles as living end-to-end documentation of the consumer API.

### 21.5 External-audience docs + CHANGELOG

- **README** rewritten for a public audience: what Stowage is (the five binding
  properties), the five-minute quickstart, the benchmark report link, the SDK/HTTP/
  MCP surfaces, the architecture seams, and the licence. No internal phase jargon.
- **CHANGELOG.md** — Keep a Changelog format, a `0.1.0` entry summarizing the v0.1
  capability set (this is the first public cut).
- **`docs/` external set** — getting-started, configuration (profiles + the knob
  guardrail), operations (serve/postgres/MCP co-mount), and a security page derived
  from 21.1. CONTRIBUTING points at CLAUDE.md's normatives.

## Files added or changed

```text
docs/plans/phase-21-launch-hardening.md   # this plan
docs/security-audit.md                    # 21.1 artifact (PASS/finding per §13 item)
docs/security.md                          # external security page (derived)
docs/getting-started.md                   # external quickstart (five-minute rule)
LICENSE                                    # Apache-2.0 (OQ-5 → D-097)
CHANGELOG.md                              # 0.1.0 entry (Keep a Changelog) — may already exist; finalize
README.md                                 # public-audience rewrite
CONTRIBUTING.md                           # points at CLAUDE.md normatives
Makefile                                  # + release (cross-compile matrix + SHA256SUMS), + cross-build
.github/workflows/ci.yml                  # + cross-build job (4 targets, build-only)
.github/workflows/release.yml             # tag-triggered artifact build + checksums (new)
scripts/smoke/phase-21-fiveminute.sh      # the five-minute-rule smoke (mock, in CI)
scripts/acceptance/full-cycle-live.sh     # 21.6 full-cycle LIVE acceptance (real models, all routes, operator-run)
scripts/forbidden-history-sweep.sh        # full-history predecessor-name sweep
scripts/drift-audit.sh                    # + secret-pattern grep; reference the history sweep
internal/...                              # ONLY if 21.1 surfaces a real security finding (+ regression test)
docs/decisions.md                         # D-097 (OQ-5 → Apache-2.0)
```

## Config keys added

None. The knob guardrail (D-034) forbids it, and this phase is audit + artifact.
(`make release`/`cross-build` are build targets, not runtime config.)

## Acceptance criteria (binding)

1. **§13 + §14 checklists pass repo-wide**, recorded in `docs/security-audit.md`
   with file:line evidence; any finding is fixed in this phase with a regression
   test. DSAR export + cascading-delete-per-(tenant,user) is confirmed present,
   scope-enforced, P1-respecting (only cascade path that touches verbatim), and
   event-emitting.
2. **CGo-free cross-compile** succeeds for darwin/linux × amd64/arm64
   (`CGO_ENABLED=0`); `make release` emits the four artifacts + `SHA256SUMS`; each
   runs `stowage version`. CI's `cross-build` job compiles all four at PR time.
3. **Full-git-history forbidden-names sweep is green** (`scripts/forbidden-history-
   sweep.sh` over all refs) — the Python predecessor's name and the internal
   CC-memory system name appear nowhere in history; wired into `drift-audit`
   (working tree) + run history-wide here.
4. **The five-minute-rule smoke passes** (`scripts/smoke/phase-21-fiveminute.sh`):
   one secret env var, `stowage serve`, ingest→retrieve round-trip, under the time
   bound, mock-gateway-deterministic, in `make preflight`. `stowage config explain`
   surfaces effective config + provenance.
5. **LICENSE (Apache-2.0) present**; README + CHANGELOG (0.1.0) + external docs are
   launch-ready and cross-reference the benchmark report (`eval/REPORT.md`).
6. **The full-cycle LIVE acceptance script passes** (`scripts/acceptance/full-cycle-
   live.sh`) against the real bifrost/OpenRouter gateway (embed + complete + rerank
   active): every consumer route — HTTP, MCP-over-HTTP, CLI — returns correct,
   asserted results across a realistic multi-session cycle (ingest → real extraction
   → retrieve+assert → feedback → drill-down → episodes/playbook/verify/traces/grants/
   suggestions → supersede+rollback), exit-0. Operator-run (never CI); proves the
   whole system behaves correctly with all internal models active.
7. **`make preflight` (build + every smoke + drift-audit, now incl. the new checks)
   and `go test -race ./...` are green**; the §14 checklist confirms no drift into a
   declared non-goal.

## Smoke script

`scripts/smoke/phase-21-fiveminute.sh` — one line each:
- `stowage serve` starts on an ephemeral port with only `STOWAGE_GATEWAY_API_KEY`
  set (mock).
- ingest returns 202; flush returns 202; a memory becomes active within the bound.
- retrieve returns the ingested memory; round-trip well under five minutes.
- `stowage config explain` prints effective config with value provenance.
- `make release` builds all four CGo-free targets + `SHA256SUMS` (invoked from the
  smoke in build-only form, or asserted by the `cross-build` CI job).
- `scripts/forbidden-history-sweep.sh` exits 0 (no predecessor-name hits in history).

## Test plan

- **Security pass:** the audit is review-driven (artifact = `docs/security-audit.md`)
  + an adversarial subagent pass; any code finding ships with a unit/integration
  regression test (e.g. a DSAR cascade integration test with real drivers if a gap
  is found, per §17).
- **Five-minute smoke:** scripted, mock gateway, deterministic, in `make preflight`.
- **Cross-compile:** the `cross-build` CI job is the test (compile = pass).
- **History sweep:** `scripts/forbidden-history-sweep.sh` asserted exit-0 in CI.
- **No new package** ⇒ no new coverage threshold; touched packages (only if a
  finding is fixed) keep their band.

## Risks & mitigations

- **A late security finding forces real code change at the gate.** Mitigation: the
  audit is decomposed so each §13 item is independently verifiable; a finding is a
  small, well-scoped PR with a regression test, not a redesign. The five binding
  properties were enforced phase-by-phase, so a *structural* finding is unlikely —
  the audit mostly confirms.
- **History sweep false positives** (a legitimate substring). Mitigation: the sweep
  matches the exact predecessor project name(s) as whole tokens, documented in the
  script; a hit is reviewed, not auto-trusted.
- **Five-minute smoke flakiness under CI load** (timing). Mitigation: the time bound
  is generous (the criterion is "works with one env var, no config file", not a
  latency SLO — that is the `make slo` gate, D-095); the assertion is on success +
  a loose upper bound.
- **Cross-compile surfaces a hidden CGo dependency.** Mitigation: `CGO_ENABLED=0` has
  been pinned since Phase 01 and the sqlite driver is pure-Go (D-022); the matrix
  build makes any regression loud at PR time via `cross-build`.

## Glossary additions

- **Five-minute rule** — the binding adoption criterion (§9.4): a fresh environment
  with one secret env var reaches first-memory-stored-and-retrieved in under five
  minutes, scripted and smoke-gated (`scripts/smoke/phase-21-fiveminute.sh`).
- **Forbidden-names history sweep** — the launch-blocking check that the predecessor
  systems' names appear nowhere in the *entire git history* (not just the working
  tree), enforcing the clean-room predecessor-hygiene rule.
- **Release matrix** — the CGo-free cross-compile set (darwin/linux × amd64/arm64)
  with published `SHA256SUMS`, produced by `make release` / the release workflow.
- **Full-cycle live acceptance** — the operator-run script
  (`scripts/acceptance/full-cycle-live.sh`) that drives the running server through a
  realistic usage cycle over every consumer route (HTTP + MCP + CLI) with the real
  LLM/embedding/rerank models active, asserting end-to-end correctness — the launch
  acceptance gate run before tagging v0.1.

## Decisions filed

- **D-097: OQ-5 resolved — Stowage ships under Apache-2.0.** Permissive + explicit
  patent grant; the ecosystem-friendly fit for Go memory infrastructure and the
  Portico/Harbor/Dockyard interop. The BSL-style cloud-protective alternative
  (RFC OQ-5) is considered and rejected for v0.1: a source-available licence would
  dampen the open-source adoption the benchmark-led launch (brief 06) is built to
  win, and the managed-cloud control plane is explicitly a *separate* codebase
  (§14) where commercialization protections belong, not this repo.
