# Phase h1 — `boot.StartPipeline`: pipeline + lifecycle parity across all entrypoints

- **Status:** ~~draft | approved | in-progress~~ **shipped**
- **Owning subsystem(s):** `internal/boot`, `cmd/stowage`, `sdk/stowage`, (consumers of) `internal/pipeline`, `internal/reconcile`, `internal/lifecycle`
- **RFC sections:** §4.1 (write path — MCP `memory_ingest` and SDK are co-equal ingest entries), §9.2 (`stowage mcp` standalone server), §9.3 (SDK in-process embeds *the whole server*), §10 (channels + supervision, run standalone)
- **Depends on phases:** 06 (buffers), 07 (extraction), 08 (reconcile), 14 (sweeps), 17 (MCP), 18 (SDKs) — all shipped
- **Informing briefs:** 03 (Engram pipeline shape: buffer→extract→reconcile→commit, memory-as-infrastructure), 01 (Python predecessor: custom-orchestration / drift pain points)
- **Program:** D-067 Wave A (flagship correctness fix). Pre-reserved decision: **D-068**.

## Goal

When this phase is done, a record ingested through **any** live entrypoint —
`stowage serve` (HTTP), `stowage mcp` (stdio or http), or `sdk/stowage`
(embedded) — flows through the identical buffer→extract→reconcile→commit pipeline
and is subject to the identical lifecycle sweeps (decay/dedupe/rollup/re-enqueue/
confirm) and embedding backfill, on both sqlite and Postgres. The post-boot
"turn a static stack into a live system" wiring exists in exactly **one** place —
`boot.StartPipeline` — consumed by all three entrypoints, so they cannot drift
again. This closes the flagship parity blocker: today `stowage mcp` boots the
stack and accepts `memory_ingest` but starts no stages, so MCP-ingested records
durably append, enqueue into a consumer-less channel, fill it, then silently drop
— while the tool reports success (`cmd/stowage/main.go:425-487` vs the full wiring
at `:603-637`; `internal/boot/boot.go:18` godoc documents the gap).

## Brief findings incorporated

- **Brief 03 (Engram):** the pipeline is a fixed four-stage flow
  (buffer→extract→reconcile→commit); "memory is infrastructure" means every
  consumer surface gets the *same* derivation, not a per-surface variant. The fix
  centralizes the flow so each surface inherits it identically.
- **Brief 01 (Python predecessor):** custom per-surface orchestration was a
  drift/maintenance sink; a single supervised wiring path is the corrective.

## Findings I'm departing from

- None at the design level. One **scope boundary** is deliberately deferred to
  Wave D, not departed from: whether a single `stowage serve` process should
  expose *both* HTTP and MCP over one stack (strictly one logic core) is a
  deployment-shape RFC decision (D-073). This phase makes each entrypoint run the
  pipeline via the shared helper; it does **not** merge `serve` and `mcp` into one
  process. Both outcomes satisfy "MCP runs the pipeline"; the canonical shape is
  ratified later.

## Design

### The seam
Add to `internal/boot`:

```go
// StartPipeline wires and starts the live derivation system on top of an opened
// Stack: the buffer/extract/reconcile stages, the lifecycle Manager (all sweeps),
// and the embedding BackfillSweep. It is the single canonical post-boot wiring
// shared by `stowage serve`, `stowage mcp`, and `sdk/stowage` (NewEmbedded).
//
// The returned Pipeline exposes the buffer stage's input channel (for the ingest
// handlers / SDK), the *pipeline.Stage (for flush/branch control verbs), and a
// Drain(ctx) that stops every stage and the sweeps in dependency order.
func StartPipeline(ctx context.Context, stk *Stack, cfg config.Config) (*Pipeline, error)

type Pipeline struct {
    In       chan<- pipeline.Item // ingest enqueue (fire-and-forget, P2)
    Stage    *pipeline.Stage      // buffer stage: FlushKey / FlushBranch control
    // ... internal stage/sweep handles
}
func (p *Pipeline) Drain(ctx context.Context) error
```

`StartPipeline` is the **only** place buffer/extract/reconcile/lifecycle/backfill
stages are constructed for live serving. `boot.Open` stays unchanged (it builds
the static stack; it does not serve). The godoc on `boot.Open` ("stages started
by serve + sdk embedded — not mcp") is corrected to point at `StartPipeline`.

### Entrypoint refactors (behavior-preserving for serve + embedded)
- `cmd/stowage` `runServe`: replace the hand-wired stage block
  (`main.go:603-637`) with `p, err := boot.StartPipeline(ctx, stk, cfg)`; pass
  `p.In` to the API `Services`, `p.Stage` via `SetStage`; `defer p.Drain`.
- `cmd/stowage` `runMCP`: **new** — call `boot.StartPipeline`, pass `p.In` to
  `mcpserver.Services.PipelineIn`, `defer p.Drain`. (The MCP buffer-control verbs
  themselves are Wave B; this phase only makes ingested records *progress*.)
- `sdk/stowage` `NewEmbedded` (`embedded.go:118-144`): replace its hand-wired
  four-stage block with `boot.StartPipeline`; retain `p.In` and `p.Stage` on the
  embedded client; `Close` calls `p.Drain`.

### Concurrency / shutdown
`StartPipeline` starts each stage as the existing supervised goroutine pools
(unchanged semantics — bounded queues, per-stage retry, dead-letter, channel-depth
backpressure per RFC §4.1.4). `Drain` stops in reverse dependency order (sweeps →
reconcile → extract → buffer) and is idempotent. Reusable-artifact discipline
(CLAUDE.md §5): no per-request state on the returned `Pipeline`; safe under
concurrent ingest.

### Backfill on every live path
`BackfillSweep` (embed recovery, today `cmd/stowage/main.go:613` serve-only) moves
inside `StartPipeline`, so embedded and MCP recover dropped/degraded embeddings
identically (closes the D-036-scenario divergence).

## Files added or changed

```text
internal/boot/pipeline.go        # NEW — StartPipeline + Pipeline + Drain
internal/boot/boot.go            # godoc correction (Open no longer "the" wiring point)
cmd/stowage/main.go              # runServe + runMCP -> StartPipeline
sdk/stowage/embedded.go          # NewEmbedded -> StartPipeline; Close -> Drain
scripts/smoke/phase-h1.sh        # NEW — MCP ingest->memory E2E
test/integration/pipeline_parity_test.go  # NEW — serve/mcp/embedded ingest->retrieve parity
docs/plans/README.md             # hardening-track row
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| (none) | — | No new knobs. `StartPipeline` reuses `cfg.Profile` triggers (D-042) and `lifecycle.DefaultProfile()`; D-034 knob guardrail not engaged. |

## Acceptance criteria (binding)

1. `boot.StartPipeline` exists and is the **only** constructor of
   buffer/extract/reconcile/lifecycle/backfill stages for live serving — `grep`
   shows no `pipeline.New|NewExtractStage|reconcile.New|lifecycle.New|BackfillSweep`
   call sites remain in `cmd/stowage` or `sdk/stowage` outside the helper.
2. `runServe`, `runMCP`, and `NewEmbedded` all obtain their live system from
   `boot.StartPipeline` and `Drain` it on shutdown.
3. **Flagship E2E:** a record ingested via `stowage mcp` (both stdio and `--http`)
   becomes a retrievable memory (after a flush/extract cycle) — proven by
   `scripts/smoke/phase-h1.sh` and the integration test.
4. Lifecycle sweeps run on the MCP and embedded paths (the lifecycle Manager is
   started by `StartPipeline` on all three).
5. `BackfillSweep` runs on serve, mcp, and embedded (no longer serve-only).
6. Graceful drain on shutdown for all three entrypoints (no goroutine leak; race
   detector clean).
7. **Behavior preservation:** existing serve and embedded tests pass unmodified;
   `test/integration/pipeline_parity_test.go` proves ingest→retrieve yields the
   same memory + provenance on serve, mcp, and embedded, on a real driver, under
   `-race` (CLAUDE.md §17).

## Smoke script

`scripts/smoke/phase-h1.sh` — build; boot `stowage mcp --http` against embedded
sqlite with the mock gateway; ingest a record via the MCP `memory_ingest` tool;
trigger/await flush; `memory_retrieve` and assert the memory is present (FAIL if
zero, which is today's behavior); confirm clean shutdown. SKIP gracefully if the
binary/tooling isn't built.

## Test plan

- **Integration (§17):** `pipeline_parity_test.go` — real sqlite driver; drive
  ingest→retrieve through serve (httptest), mcp (in-proc transport), and embedded;
  assert identical reconciled memory + provenance; cover ≥1 failure mode (gateway
  degraded → record still appended, re-enqueued); `-race`.
- **Unit:** `StartPipeline` returns a working `In`/`Stage`; `Drain` is idempotent
  and stops all stages (goroutine-count assertion).
- **Behavior-preservation:** existing `cmd/stowage` + `sdk/stowage` suites
  unmodified and green.

## Risks & mitigations

- *Risk:* refactoring `runServe`/`NewEmbedded` regresses the two working paths.
  *Mitigation:* behavior-preservation bar (AC-7) — existing tests unmodified; the
  helper is extracted from the current serve wiring verbatim.
- *Risk:* MCP stdio has a fixed tenant (`StdioScopeFn`); ingested memories land in
  one scope. *Mitigation:* expected and correct for stdio; the E2E uses that scope.
- *Risk:* drain ordering deadlock. *Mitigation:* reverse-dependency drain + race
  test + bounded shutdown context.

## Glossary additions

- **StartPipeline** — the single canonical post-boot wiring that turns an opened
  `Stack` into a live derivation system (stages + sweeps + backfill), shared by
  all three entrypoints.

## Decisions filed

- **D-068** — `boot.StartPipeline` is the single post-boot wiring seam; `stowage
  mcp` and `sdk/stowage` drive the identical pipeline + lifecycle + backfill as
  `stowage serve`. Closes the flagship parity blocker (D-067). Behavior-changing
  (MCP ingest now produces memories) ⇒ shipped as its own `fix` PR with this E2E.
