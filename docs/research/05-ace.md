# Brief 05 — ACE: Agentic Context Engineering (arXiv 2510.04618)

> Public source: "Agentic Context Engineering: Evolving Contexts for
> Self-Improving Language Models" (reviewed 2026-06-10). Stowage builds the ACE
> loop in as a server capability (RFC §6a, D-018).

## The architecture

Three roles: **Generator** produces reasoning trajectories; **Reflector**
distills concrete insights from successes *and errors* through iterative
refinement; **Curator** synthesizes lessons into compact delta entries merged
**deterministically** (non-LLM logic) into the existing context.

## Context collapse — why monolithic rewrites are forbidden

Iteratively rewriting an accumulated context with an LLM progressively
compresses it into shorter, less informative summaries. Documented case: an
18,282-token context (66.7 % accuracy) collapsed to 122 tokens in one rewrite
step, dropping accuracy to 57.1 % — *below* the 63.7 % no-context baseline.
Consequence for Stowage: playbook assembly contains no LLM call; all evolution
happens through itemized delta reconciliation (RFC §6a.3).

## The bullet model (≈ Stowage's memory model)

Contexts are structured, itemized bullets, each with a unique ID and
helpful/harmful counters; the Generator marks which bullets helped or misled.
Updates are small candidate sets merged deterministically — enabling
localization, fine-grained retrieval, and parallel merges. Grow-and-refine:
new bullets append, existing ones update in place (counter increments),
embedding-based dedup prunes redundancy proactively or lazily.

Mapping: bullets ≈ memories; helpful/harmful ≈ the six utility counters
(`use`/`save` vs `fail`/`noise`, strictly richer); delta merge ≈ reconciliation
commit; grow-and-refine ≈ dedupe sweep. What Stowage adds on top: provenance,
trust gates, decay, scopes/grants.

## Results worth quoting in the eval design

+10.6 % average on AppWorld agents (DeepSeek-V3.1 with ACE matching a
production GPT-4.1 agent: 59.4 % vs 60.3 %); +8.6 % finance (FiNER/Formula);
+15.0 % medical reasoning (DDXPlus); 82.3 % lower adaptation latency and
75.1 % fewer rollouts than GEPA; **91.8 % KV-cache hit rate** because the
playbook is append-biased and stable under prompt caching. Works **label-free**
when execution feedback is available — for Stowage, Harbor task outcomes are
that feedback (RFC §10).

## Online vs offline adaptation

Offline: optimize the context on a training split (multi-epoch — revisit old
queries as the playbook matures → Stowage's re-reflection sweep). Online: the
context evolves sequentially during evaluation — each sample triggers
generate → reflect → curate before the next. The eval harness includes
online-adaptation scenarios (RFC §12, Phase 19).

## What Stowage deliberately does differently

- Reflection output goes through full reconciliation (dedup, trust gates,
  supersede) rather than ACE's curator-only merge — strategies obey the same
  lifecycle as every other memory.
- Playbooks are scoped and shareable via grants — fleet-level ACE, not
  single-agent ACE.
- The playbook is a *view* over the store, not the store itself: drill-down to
  verbatim provenance remains available from every bullet (P1).
