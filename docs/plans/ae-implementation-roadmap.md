# Roadmap — `ae*` autonomous implementation (agent-identity & read-time scoping)

> **This is a living tracking file. The orchestrator agent MUST update the
> checkboxes and the wave status as work completes** — it is the source of truth
> for how far the autonomous build has progressed. Mark a box `- [x]` only when
> its gate is actually met (not when work starts).
>
> **Design is frozen.** The plans (`docs/plans/phase-ae*.md`), the charter
> (`docs/plans/track-adoption-ergonomics.md`), and the decisions
> (`docs/decisions.md`, D‑135–D‑151) are the specification. Implement them; do not
> re-litigate settled decisions. If a plan and code truth genuinely conflict,
> record a departure in the PR and update the plan in the same PR (never silent
> drift). The RFC wins over a plan; a plan wins over this file.

---

## Build protocol (per phase, then per wave)

**Roles**
- **Worker = Sonnet.** Each phase is implemented by a Sonnet worker against its
  frozen plan (core logic + all-tier surfaces + tests + smoke).
- **Reviewers = dual adversarial.** Every phase's implementation gets **two
  independent adversarial reviews** (separate agents, separate passes) hunting for
  correctness bugs, P1–P5 violations, charter/decision drift, parity gaps, and
  weak/mismatched tests. A finding is acted on only after it survives verification.
- **Orchestrator = fixer + gatekeeper.** The orchestrator applies the fixes from
  the adversarial reviews, keeps the plans/decisions coherent, and owns the merge
  decision. It does not hand a wave off until every gate below is green.

**Same-wave fix protocol.** All fixes for a wave's phases land **inside that
wave's PR** before it merges. A wave is never merged with known-open review
findings "to fix later." If a fix touches an earlier merged phase, it still lands
here (fix-forward in the current wave's PR).

**Mandatory live 3-surface validation (not just unit tests).** Before a wave's PR
is approved, every capability it adds is exercised **end-to-end against a real
gateway** on **all three surfaces — SDK, HTTP, MCP** — proving parity (identical
behavior/results per D‑067/D‑073), not just that unit tests pass. Use the root
`.env`: `OPENROUTER_API_KEY`, `embedded_model`, `rerank_model`, `learner_model`
(the real Bifrost/OpenRouter stack, D‑131). The live test does a real
ingest → extract → retrieve round trip so embeddings, rerank, and the learner LLM
actually run; the new capability of each phase is asserted through each surface.
(The gateway `mock` driver is only for hermetic unit tests, never a substitute for
the live 3-surface gate.)

**One PR per wave.** Each wave ships as a single PR containing all its phases.
- The PR is **approved by the orchestrator only once CI is green on the web**
  (all required checks **and** `build-test` — poll the full suite; do not merge on
  the required-checks subset alone).
- On green, the orchestrator merges (squash), **updates this roadmap** (checks the
  wave's boxes, records the PR number + merge SHA), and **proceeds to the next
  wave** under the identical procedure.

**Per-phase Definition of Done** (all must hold before the wave PR is approved):
1. Core logic implemented once; SDK + HTTP + MCP are thin callers (D‑067/D‑073).
2. `scripts/smoke/phase-aeN.sh` flips from **SKIP → OK** (`OK ≥ count(criteria)`,
   `FAIL = 0`); all prior phases' smokes still pass.
3. Coverage bands met for touched packages (`make coverage`); a new package is
   added to `scripts/coverage.json` in the same PR.
4. `-race` tests + `go vet` + `golangci-lint` clean; new store concern proven on
   **both** drivers via the conformance suite; §17 integration test where deps
   cross subsystems.
5. Both adversarial reviews' confirmed findings are resolved.
6. **Live 3-surface validation passes** (above).
7. `make drift-audit` + `make check-mirror` + `make preflight` green.

**Non-negotiable invariants to re-verify every phase** (drift tripwires):
- **P3:** scopes enforced in the store layer; no unscoped query API; **no `agent`
  column on any of the 12 scope tables**; `Scope.Agent` is read-time only and inert
  on writes/scope-`WHERE`.
- **D‑137:** precedence **JWT claim > `_meta` > arg** (`_meta` wins over the
  model-filled arg); default STRICT; two knobs (`identity.multiplexing` default
  `false`, `retrieval.read_posture` default `compatible`); tenant credential-pinned,
  mismatch fails closed (D‑138).
- **D‑150:** session **never filters and never ranks** a read (cross-session recall
  preserved); `Scope.Session` stays empty on the read path.
- **D‑151:** **one** `topic_views` junction table / `TopicViewStore` seam /
  `retrieval.agent_views.enabled` knob — ae1 creates it at migration 0013; ae9 adds
  semantics only (no new table/migration/enable-knob).
- **D‑139:** the read-time topic/agent/view filter **fails OPEN** and is a distinct
  function from grants' fail-**closed** `filterByTopic` — never harmonized.
- **D‑034:** every new knob ships with a tuned default, placement in every profile,
  docs, and a smoke check; zero-config start preserved.

---

## Wave board (orchestrator updates this)

Legend: `- [ ]` not done · `- [x]` done. Wave **Status:** `NOT STARTED` →
`IN PROGRESS` → `MERGED (PR #NN, <sha>)`.

### Wave 0 — ship-now (no auth, no scope-table migration) · one PR
**Status:** MERGED (PR #92, 1a10f57)
Internal order: **ae3 → ae4a**; ae5 and ae6 independent (ae6 is the filter keystone ae1/ae9 reuse).
- [x] **ae3** — shared render core (`RenderMode`; eval byte-frozen; inert `RenderMCP` superset) · D‑141
- [x] **ae4a** — lean MCP read (`Text` markdown, episode hook, drill = citation ULID; no new store code) · D‑142
- [x] **ae5** — browse (`ListByScopeRecent` inverted keyset, both drivers + conformance; superseded reuses `ListByStatus`) · D‑143
- [x] **ae6** — own-scope topic filter (discrete `MemoriesTopics` pre-`scoringK`-trim; **fail-open**; `topic_filter_scoring_k`) · D‑144/D‑139
- [x] Dual adversarial review complete · [x] live 3-surface validation · [x] CI green · [x] merged · [x] roadmap updated

### Wave 1 — additive read-time identity (+ Dockyard v1.8 bump) · one PR
**Status:** MERGED (PR #94, 2dfa7ec)
Depends on **W0** (ae1 reuses ae6's filter).
- [x] **ae1** — read-time `Scope.Agent` (read-path only, inert on writes); `dockyard v1.7.3→v1.8.0` + `server.RequestMeta`; creates the `topic_views` junction + `TopicViewStore` at migration 0013; `agent_id` field on SDK+HTTP, `_meta.agent_id` on MCP · D‑135/D‑146(shape per D‑151)
- [x] **ae2** — additive `_meta` intake (`user`/`session`/`agent`); `metaElseArg` (`_meta` wins); tenant credential-only, `_meta.tenant` mismatch fails closed; session → relevance sink not `Scope.Session` · D‑138 (impl D‑137)
- [x] Dual adversarial review · [x] live 3-surface validation · [x] CI green · [x] merged · [x] roadmap updated

### Wave 2 — auth foundation · one PR
**Status:** MERGED (PR #96, 9b4ea24)
Independent of W1; the C4 gate that unblocks W4.
- [x] **ae7** — Harbor-aligned JWT verifier (verify-never-mint; asymmetric-only + `WithValidMethods`; issuer exact-match; audience containment D‑136; JWKS fail-loud boot / fail-closed max-stale D‑147; keyring = zero-config default; test signer test-only) · D‑136/D‑147
- [x] Dual adversarial review · [x] live 3-surface validation · [x] CI green · [x] merged · [x] roadmap updated

### Wave 3 — curation & enrichment built on identity · one PR
**Status:** MERGED (PR #98, 8fbd534)
Depends on **W1 + W2** (ae8 ← ae2+ae7; ae9 ← ae1+ae6).
- [x] **ae8** — effective-scope resolver (`ResolveReadScope`, precedence JWT > `_meta` > args); the two D‑137 knobs; **adds no store WHERE** (populate/require `Scope.User`); strict refuses tenant-wide; `Scope.Session` empty on read (D‑150) · D‑148 (impl D‑137)
- [x] **ae9** — per-agent/per-key topic views on ae1's `topic_views` (no new table/migration/knob); subject = agent_id or verified key id; **fail-open**; view can only subtract · D‑149 (impl D‑139)
- [x] Dual adversarial review · [x] live 3-surface validation · [x] CI green · [x] merged · [x] roadmap updated

### Wave 4 — breaking removal (post-ae7/ae8) · one PR
**Status:** MERGED (PR #100, 4bc64ae)
Depends on **W2 + W3**. Pre-launch ⇒ **direct removal, no deprecation window** (see ae2b plan); gate on ae7+ae8 is correctness, not compat.
- [x] **ae2b** — remove `project_id`/`user_id` from the **14** MCP read-targeting contracts (the 13 named + `BrowseInput`/ae5, folded in per the plan's As-built deviations); identity from `_meta`/JWT; `project_id`→`_meta.project` (M1, key `"project"`); MCP-vs-HTTP divergence sanctioned · D‑140
- [x] Dual adversarial review · [x] live 3-surface validation · [x] CI green · [x] merged · [x] roadmap updated

**The `ae*` track (agent-identity & read-time scoping) is COMPLETE** — Waves 0–4 all merged (deferred: ae4b, ae10).

### Deferred (promote only on a confirmed need — not part of the automated sequence)
- [ ] **ae4b** — causal hook (batch `Store.LinksExist`, no N+1) · D‑145 — *deferred; smoke SKIPs*
- [ ] **ae10** — `layer`/`intent` read-shaping (own-or-drop, M2) · *deferred*

---

## Progress log (orchestrator appends one line per wave merge)

- Wave 0 merged as PR #92 (1a10f57), 2026-07-01; live 3-surface validation: pass (real OpenRouter/Bifrost — ae4a lean read/drill, ae5 browse, ae6 topic filter verified on SDK+HTTP+MCP). Dual adversarial review + fixes: mcpserver browse coverage (78.7→81.0%), judged-path CURRENT/SUPERSEDED partitioning restored, harbor-adapter fakeClient.Browse, phase-29/29c smoke fix-forward.
- Wave 1 merged as PR #94 (2dfa7ec), 2026-07-01; live 3-surface validation: pass (real gateway — ae1 agent→topic narrowing 4→2 with SDK/HTTP/MCP parity; ae2 `_meta.user` narrow + `_meta.tenant` fail-closed). Dual adversarial review + fixes: `PutAgentPolicy` empty-allow/deny footgun rejected at store layer, `json:"-"` on `Scope.Agent` (pipeline golden), RFC §8.1 amended for `topic_views`, `memory_proactive_config` get-arm `Scope.Agent`, SA5011 lint fix-forward, live-harness topic-snapshot race hardened.
- Wave 2 merged as PR #96 (9b4ea24), 2026-07-01; live JWT-auth validation: pass (real gateway — ingest→extract→retrieve JWT-gated on HTTP/MCP-over-HTTP/SDK-over-HTTP, rendered bodies match; negatives: no-token 401, expired 401/403, cross-tenant 0 items). Dual adversarial review (security/crypto + tests/config/parity): no auth-bypass/fail-open; fixed JWKS negative-kid refresh amplification (min-refresh window) + RSA exponent bound.
- Wave 3 merged as PR #98 (8fbd534), 2026-07-02; live 3-surface validation: pass (real gateway — ae8 strict isolation+refusal, ae9 topic-view narrowing 4→2 with SDK/HTTP/MCP parity + unbound pass-through). Dual adversarial review (correctness/P3 + tests/parity/coverage) caught a CRITICAL P3 blocker — MCP+JWT wasn't pinning Scope.User from the verified credential (within-tenant cross-user read leak); fixed + guarded by a negative isolation test. Also: api coverage 77.9→80.4%, two smoke-gate false-green/false-fail bugs, pgstore view-CRUD TOCTOU (SERIALIZABLE→ErrConflict), revive package-comment lint.
- Wave 4 merged as PR #100 (4bc64ae), 2026-07-02; **completes the `ae*` track.** Removed `project_id`/`user_id` from **14** MCP read/mutate contracts (the 13 the plan enumerated + `BrowseInput`/ae5 — folded in as a documented plan deviation so `memory_browse` isn't the lone arg-targeted read tool); `metaIdentity.Project` + `_meta.project` (key `"project"`, M1) aligned; HTTP/SDK untouched (D-140). Live validation: pass (real gateway — MCP `memory_retrieve` + `memory_browse` resolve identity purely from `_meta` with u1/u2 isolation, no-`_meta` stays tenant-wide, `_meta.project` narrowing, and D-140 MCP `_meta.user` ≡ HTTP `?user_id=` behavioural parity over the same rows). Dual adversarial review (correctness/P3/D-140 + tests/parity/smoke): both mergeable; fixed a glossary contradiction, added a §17 BrowseInput integration subtest, documented the AC-5 2-shape parity narrowing, and re-anchored 6 stale phase-30/B3 grep assertions the ae8 resolver consolidation had silently broken since W3 (phase-30 now 39/0). Orchestrator notes: a rogue in-flight worker kept re-reverting the BrowseInput removal (stopped via TaskStop); the live harness was fixed to seed identity fixtures directly + query a distinctive term (the explicit-flush path stamps memory scope from the credential, not per-record `user_id`).
