# Brief 04 — CL-Bench (arXiv 2606.05661)

> Public source: "Continual Learning Bench: Evaluating Frontier AI Systems in
> Real-World Stateful Environments" (Asawa et al.), reviewed 2026-06-10. A
> benchmark paper — its value to Stowage is the empirical catalogue of how
> memory systems fail, and the gain metric.

## Headline finding

Naive in-context learning with full conversation history **consistently
outperformed dedicated memory systems**. "The bottleneck in current memory
systems lies not in storage capacity but in extraction and retrieval
fidelity." Systems that maintained verbatim interaction records outperformed
those using compressed representations.

## Failure modes catalogued

1. **Lossy extraction with no recovery path.** Automatic summarization loses
   information the system cannot recover at retrieval time; retrieved summaries
   lack the critical details novel scenarios need.
2. **Stale memories actively harm.** Under environment/task distribution shift,
   stored information became misleading; memory-augmented agents sometimes
   performed *worse* than memoryless baselines (negative gain).
3. **Retrieval misses.** Agents frequently failed to retrieve relevant prior
   experience, or failed to reuse knowledge across similar instances.
4. **Recency over-reliance.** No effective mechanism balancing recent
   experience against long-term patterns; agents oscillated on repeated
   scenarios.

## Prescriptions (→ Stowage design)

- **Hybrid memory**: full-context buffers for recent interactions + abstracted
  indexes over older history. → P1: verbatim records forever, memories as the
  abstraction layer, provenance **drill-down** as the recovery path (RFC §4.2.7).
- **Multiple retrieval paths** (recency, similarity, task relevance,
  outcome-based) instead of one fixed heuristic. → concurrent lanes + fusion +
  retrieval profiles (RFC §4.2).
- **Downweight/prune under shift evidence.** → fail/noise counters feeding
  decay; cluster-level shift detection emitting `memory.shift_suspected`
  (RFC §6, v1.x).
- **Outcome feedback.** Track whether injected memories actually helped. →
  `/v1/feedback` and the use/fail counters (RFC §4.2.8).

## The gain metric (→ Phase 27)

`Gain = Performance(with memory) − Performance(without memory)`, controlled for
base-model capability, measured on both novel and repeated tasks. Positive gain
indicates memory enabled generalization; negative gain means memory degraded
the agent. Stowage ships a gain harness in-tree and treats negative gain on the
standard scenarios as a release blocker (D-014). The benchmark's six domains
(software engineering, signal processing, forecasting, database querying,
game-playing, demand forecasting) inform the harness's scenario design.
