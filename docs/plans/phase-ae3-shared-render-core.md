# Phase ae3 — shared render core (eval-mode vs MCP-mode)

- **Status:** implemented (see "As-built deviations" below)
- **Owning subsystem(s):** `internal/retrieval` (new render path); `eval/harness` and `internal/mcpserver` (call sites)
- **RFC sections:** §4.2 (read path), §9.2 (MCP surface), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** the shipped retrieval render/result path (`internal/retrieval.MemoryItem`/`Response`); the ae-track parent (D-135). No in-track dependency — lands **first** in Wave 0, **before ae4a**.
- **Informing briefs:** 06 (mempalace — benchmark-led positioning; the lean-reader axis where Stowage beats a 43k-token reader; gateway-free retrieval), 05 (ACE — context engineering, the context-collapse failure the lean context defends against), 02 (CC-memory predecessor — surface-sprawl cautionary tale → one renderer, not one-per-surface). Empirical reader-lever basis is eval-derived (the `| When:` assertion-date lever; CURRENT/SUPERSEDED sectioning, D-105/D-109/D-114), not a standalone brief.

## Goal

When this phase is done there is **exactly one** function that turns retrieval
results into reader-facing text — `retrieval.Render(mode, items)` — parameterized
by a two-value `RenderMode` (`RenderEval` / `RenderMCP`). The eval harness's two
string-coupled halves (`scoreQuestion`'s item tagging and `BuildReaderPrompt`'s
re-parse of the `"[OUTDATED…] "` marker) are replaced by a single typed seam, with
the eval reader prompt **byte-for-byte unchanged**. The `RenderMCP` mode exists,
its base body equals `RenderEval`, and its affordance slots (citation handle,
episode hook) are **wired but inert** — the empty superset ae4a fills. No MCP
behaviour changes in this phase; it is a pure refactor that plants the seam.

## Brief findings incorporated

- **06 (mempalace):** the differentiator is a *lean* reader context, not a 43k-token
  dump — so the renderer is the asset to centralize and later trim; gateway-free
  retrieval means the renderer is a pure, model-free function (no gateway call).
- **05 (ACE):** context collapse comes from uncontrolled context shape; a single
  audited renderer is the one place to keep the CURRENT/SUPERSEDED partition and the
  `| When:` date lever coherent.
- **02 (CC-memory):** surface sprawl is a named predecessor failure → one logic core,
  thin surfaces (D-067/D-073). The renderer must not fork per surface.

## Findings I'm departing from

- The charter framed ae3 as "split MCP's private renderer out." **Code truth:** MCP
  has *no* renderer — `makeRetrieveHandler` already emits a count-only `Text`
  (`"Retrieved %d item(s); response_id=%s"`) plus the full typed `Structured`
  (`internal/mcpserver/handlers.go:244`). So ae3 is not a *split*; it is *build the
  shared core, migrate eval onto it, and stand up the (inert) `RenderMCP` mode the
  MCP `Text` will adopt in ae4a*. Recorded in D-141 so the scope is honest.
- The string-coupling the charter targets is **inside eval**, not between eval and
  MCP: `scoreQuestion` (`eval/harness/runner.go:315-343`) writes the
  `"[OUTDATED — … ] "` marker, and `BuildReaderPrompt`
  (`eval/harness/judge.go:119-158`) re-parses it via
  `strings.HasPrefix(c, "[OUTDATED")` + `strings.Index(c, "] ")`. Killing *that*
  round-trip is the substantive refactor.

## Design

### The single entry point

New file `internal/retrieval/render.go`. `internal/retrieval` is the correct home:
it already owns `MemoryItem`/`Response` (`retrieval.go:83-111`) and there is **no**
render code there today (confirmed: zero `render`/`format` helpers in the package).

```go
// RenderMode selects the reader-facing shape. It is a two-value call-site
// argument, NOT a config knob (no D-034 entry) — see Risks.
type RenderMode int

const (
    RenderEval RenderMode = iota // the eval reader-prompt context block (byte-frozen)
    RenderMCP                     // the model-facing MCP body; superset of RenderEval
)

// RenderItem is the render-input projection. It decouples the renderer from
// BOTH call-site source types: the server maps retrieval.MemoryItem → RenderItem;
// eval maps its wire-decoded retrieveItem → RenderItem. The renderer never sees a
// store type or a wire type.
type RenderItem struct {
    Content             string
    OccurredAt          int64  // store.Memory.ValidFrom; 0 ⇒ omit the "| When:" suffix
    Stale               bool   // dual-visibility marker (D-105)
    SupersededByContent string // successor's current value, inline (D-114)
    SupersededByDate    int64  // successor's assertion date (D-114)

    // RenderMCP affordance slots — populated only on the server path (eval's
    // wire struct carries neither). INERT in ae3: Render emits nothing for them
    // regardless of mode. ae4a turns them on for RenderMCP only.
    Citation  string // per-item injection ULID = drill handle (ae4a)
    EpisodeID string // store.Memory.EpisodeID → episode hook (ae4a)
}

// Render is a pure function: no receiver, no package-level mutable state, no
// gateway call. Safe for concurrent reuse (proven under -race).
func Render(mode RenderMode, items []RenderItem) RenderResult
```

`RenderResult` exposes both shapes byte-identity requires:

```go
type RenderResult struct {
    // ContextBlock is the assembled CURRENT/SUPERSEDED sections with the
    // [N]/[S1] positional markers — what BuildReaderPrompt embeds in the user
    // prompt today. RenderEval reproduces it byte-for-byte.
    ContextBlock string
    // Lines is the per-item display projection (content + "| When:" + inline
    // stale tag) that scoreQuestion packs into QuestionResult.Items and the
    // currentOnly raw-content slice feeds the substring-hit metric. Preserved so
    // the eval pipeline result is unchanged.
    Lines       []string
    CurrentOnly []string
}
```

### The typed seam (the actual fix)

Today: `scoreQuestion` → `[]string` *with an inline `[OUTDATED…]` marker* →
`BuildReaderPrompt` *re-parses the marker* to re-derive `stale`. After ae3 the
seam between item projection and prompt assembly is the **typed `[]RenderItem`**
(the `Stale` bool travels as a bool); the `[OUTDATED…]` string is produced **once**,
inside `Render`, when it builds `ContextBlock`/`Lines`, and is **never re-parsed**.
This removes a fragile parse surface entirely (a quiet correctness + fuzz win:
`BuildReaderPrompt` no longer string-matches its own upstream output).

### Two mappers, one renderer

- **Server path (`internal/mcpserver`, and the shared mapper):** add
  `retrieval.RenderItemsFromMemoryItems([]MemoryItem) []RenderItem` next to `Render`.
  It carries `Citation` and `Memory.EpisodeID` into the slots (inert until ae4a).
  The retrieve handler builds its projection via this mapper. **`Text` output is
  byte-unchanged in ae3** — it keeps the count-only string; ae4a is the one-line flip
  to `Text = Render(RenderMCP, items).Body`. HTTP/SDK are untouched (they are
  renderer-free JSON mirrors today and stay so).
- **Eval path (`eval/harness`):** `scoreQuestion` maps its wire-decoded
  `retrieveItem` → `[]RenderItem` (slots zero — eval's struct has no citation/episode,
  so they are naturally inert for `RenderEval`). `BuildReaderPrompt` drops its
  partition/re-parse loop and calls `Render(RenderEval, items).ContextBlock`, then
  prepends the unchanged system text and appends the unchanged
  `Question`/`Question Date` tail. `QuestionResult.Items`/`currentOnly` come from
  `RenderResult.Lines`/`CurrentOnly`.

### Mode semantics in ae3

`RenderMCP` and `RenderEval` produce the **same base body** in this phase (the slots
emit nothing). A diff test pins the equality. ae4a is the only phase that makes the
two modes diverge (citation handle + episode hook on `RenderMCP`).

## Files added or changed

```text
internal/retrieval/render.go            # NEW — RenderMode, RenderItem, RenderResult, Render(), RenderItemsFromMemoryItems()
internal/retrieval/render_test.go       # NEW — unit/golden, mode-diff, concurrent-reuse (-race)
eval/harness/runner.go                  # CHANGED — scoreQuestion builds []RenderItem (no inline marker)
eval/harness/judge.go                   # CHANGED — BuildReaderPrompt calls Render(RenderEval,…); drops the [OUTDATED re-parse
internal/mcpserver/handlers.go          # CHANGED — build projection via shared mapper; Text byte-unchanged (ae4a flips)
scripts/smoke/phase-ae3.sh              # NEW
docs/plans/README.md                    # CHANGED — register the ae* track (first ae phase)
docs/decisions.md                       # CHANGED — D-141
docs/glossary.md                        # CHANGED — render mode, render item, context block
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| _(none)_ | — | `RenderMode` is a **two-value call-site argument**, deliberately **not** a D-034 config knob (a knob would invite a third surface-specific mode and re-fork the renderer — the exact sprawl this phase removes). |

## Acceptance criteria (binding)

1. **One render entry point.** `retrieval.Render` is the only function that emits the
   CURRENT/SUPERSEDED context body and the `| When:`/`[OUTDATED…]`/`[N]`/`[S1]`
   tokens. A grep/lint gate asserts none of those literal markers appear in
   `internal/mcpserver` or remain duplicated in `eval/harness` outside the
   `MemoryItem`/`retrieveItem`→`RenderItem` mappers.
2. **Eval byte-identical (M3, scoped to the eval call path).** `TestReaderPrompt_Golden`
   (`eval/harness/judge_test.go`) passes **unchanged**, and `TestEvalCI` scores do not
   move — the reader prompt user-string and the substring-hit inputs are byte-for-byte
   identical to pre-ae3.
3. **String-coupling removed.** No `strings.HasPrefix(…,"[OUTDATED")` or
   `strings.Index(c,"] ")` re-parse remains in `judge.go`; the item→prompt seam is the
   typed `[]RenderItem` (asserted by a test + grep).
4. **`RenderMCP` base == `RenderEval`** over a shared fixture in this phase (diff test).
5. **Slots inert.** `RenderMCP` output for a fixture carrying `Citation`/`EpisodeID`
   contains no citation handle or episode hook in ae3 (asserted); ae4a flips them on.
6. **Pure function.** `Render` has no receiver and no package-level mutable state; a
   concurrent-reuse test calls it from N goroutines on shared input under `-race`.
7. **`RenderMode` is not a config knob.** Grep asserts `RenderMode` does not appear in
   `internal/config`; no new config key ships.
8. **MCP `Text` byte-unchanged.** The retrieve handler still emits the count-only
   `Text`; the markdown flip is explicitly deferred to ae4a (a test pins the current
   `Text` shape so ae4a's change is visible).

## Smoke script

`scripts/smoke/phase-ae3.sh` — SKIPs gracefully until the files exist; then:
- assert `internal/retrieval/render.go` exists and defines `Render` + `RenderMode`.
- assert no `[OUTDATED` / `| When:` literal in `internal/mcpserver` (single entry point).
- assert `RenderMode` absent from `internal/config` (not a knob).
- `go test ./internal/retrieval/ -run Render` and the eval golden
  `go test ./eval/harness/ -run 'TestReaderPrompt_Golden|TestEvalCI'` pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Golden/unit (`render_test.go`):** `withDate` suffix; stale tag with and without
  `SupersededByContent`; CURRENT/SUPERSEDED sectioning; `[N]`/`[S1]` numbering; an
  empty-context case. The `RenderEval` golden mirrors the `TestReaderPrompt_Golden`
  fixture so the two cannot silently diverge. (Net coverage *gain*: `scoreQuestion`'s
  item render has no dedicated test today.)
- **Mode-diff test:** `Render(RenderEval, fx)` == `Render(RenderMCP, fx)` base body.
- **Concurrency (§5):** `Render` invoked from N goroutines on a shared `[]RenderItem`
  under `-race`.
- **Regression:** existing `TestReaderPrompt_Golden`, `TestReaderPrompt_SupersededSection`,
  `TestEvalCI`, `TestEvalCIGateBites` pass unchanged.
- **No fuzz target needed** — ae3 *removes* a parse surface (the `[OUTDATED]` re-parse)
  rather than adding one; note the removal in the PR.

## Risks & mitigations

- **Eval-golden drift.** The whole risk of a render lift. Mitigated by AC2 (golden +
  pipeline-score equality) and by keeping `RenderEval`'s `ContextBlock` byte-frozen;
  the inert MCP slots cannot move eval bytes because eval's `RenderItem` slots are zero.
- **Mode-flag sprawl.** Mitigated by AC7 — `RenderMode` is a call-site arg, not a knob.
- **`QuestionResult.Items` hidden coupling.** If anything serializes the item lines,
  changing the seam could shift them. Mitigated by `RenderResult.Lines`/`CurrentOnly`
  reproducing today's exact `[]string` (AC2 covers the pipeline result).
- **Eval-over-the-wire mismatch.** Eval decodes `retrieveItem` (no `Citation`/`EpisodeID`)
  over HTTP, so its slots are always zero — the affordances only ever populate on the
  server path. This is correct, not a gap; stated so ae4a's authors don't expect eval
  to exercise the slots.

## Glossary additions

- **Render mode** — the two-value `RenderEval`/`RenderMCP` argument to
  `retrieval.Render`; selects reader-facing shape without forking the renderer. Not a
  config knob.
- **Render item** — `retrieval.RenderItem`, the projection both call sites build so the
  renderer depends on neither the store type nor a wire type.
- **Context block** — the assembled CURRENT/SUPERSEDED reader sections (with `[N]`/`[S1]`
  positional markers) produced by `Render`.

## As-built deviations

- **`BuildReaderPrompt`'s signature changed** from `contexts []string` to
  `items []retrieval.RenderItem`, as anticipated. Its only direct callers are
  `JudgeQuestionWith` (production) and the golden tests — both in
  `eval/harness`, so this stayed in-file-list.
- **`JudgeQuestion`/`JudgeQuestionWith` kept their existing `contexts []string`
  signature**, unchanged, rather than threading `[]retrieval.RenderItem` further
  up the call graph. Their other callers (`gain.go`, `adapt.go`, `dataset.go`,
  `sweep_test.go`) are all outside this phase's file list and pass plain
  content strings — some of them (`dataset.go`'s judged-QA path, and the
  `fullmode`-tagged sweep) can carry the pre-rendered `"[OUTDATED …]"` marker
  baked into those strings by a prior `scoreQuestion` run. `JudgeQuestionWith`
  now wraps each string into `retrieval.RenderItem{Content: c}` (a trivial,
  non-parsing wrap — `renderItemsFromContexts` in `judge.go`) before calling
  `BuildReaderPrompt`, rather than re-detecting `Stale` from the marker text
  (which would just relocate the banned re-parse this phase removes, still
  inside `judge.go`, still failing AC3's intent even if it dodged the literal
  grep).
  - **Consequence:** the judged/full-eval path (`opts.Judge`, gated behind a
    live gateway) no longer partitions a pre-rendered marker string into a
    CURRENT/SUPERSEDED split — every item is treated as current for that path.
    **This is inert for everything AC2 requires**: `RunCI`
    (`TestEvalCI`/`TestEvalCIGateBites`) never calls the judge at all (verified
    by reading `RunCI`), so the CI/mock path this phase's byte-identity gate
    covers is unaffected. The regression is confined to the operator-gated,
    non-CI judged/sweep path.
  - **Follow-up:** if judged-mode stale sectioning is wanted, a later phase
    should thread `[]retrieval.RenderItem` through `JudgeQuestion`/
    `JudgeQuestionWith` and their four external callers — out of scope here to
    keep ae3's blast radius to its stated file list (CLAUDE.md §4.3).
  - **RESOLVED (wave-0 fix, two adversarial reviews):** the follow-up above
    landed. `QuestionResult` (`eval/harness/scores.go`) now carries a
    `RenderItems []retrieval.RenderItem` field alongside `Items []string`,
    populated by `runner.go`'s `scoreQuestion` from the same typed items it
    already builds for `retrieval.Render`. A new `JudgeQuestionWithItems`
    entry point (`judge.go`) calls `BuildReaderPrompt` directly on typed
    items — no string re-wrap, no marker re-parse. `dataset.go`'s judged path
    and `gain.go`'s `judgeOnOff` (memory-ON condition) now call
    `JudgeQuestionWithItems(..., qr.RenderItems)` instead of
    `JudgeQuestionWith(..., qr.Items)`, restoring the pre-ae3
    CURRENT/SUPERSEDED partition on the operator-gated judged/gain paths.
    `JudgeQuestionWith`'s `[]string` signature is preserved unchanged (now a
    thin delegate to `JudgeQuestionWithItems` via `renderItemsFromContexts`)
    for the genuinely-all-current callers: `adapt.go`'s playbook context, the
    gain memory-OFF condition, and the `fullmode`-tagged sweep (which reads a
    frozen `[]string` JSONL with no typed items available). See
    `eval/harness/judge_test.go`'s `TestJudgeQuestionWithItems_PartitionsStale`
    for the pinning test.
- `TestEvalCI`'s `answer_context_hit` score is confirmed **inherently flaky**
  independent of this change (0.93–0.98 across repeated runs on both the
  pre-ae3 and post-ae3 code, due to async-pipeline timing under load per the
  existing `RunCI` quiescence comments) — not a regression introduced here.

## Decisions filed

- **D-141** — Shared retrieval render core parameterized by `RenderMode`; one entry
  point in `internal/retrieval`, eval byte-frozen, `RenderMCP` an inert superset until
  ae4a; `RenderMode` is a call-site argument, not a config knob. (Records the charter
  scope correction: MCP had no renderer to split.)
