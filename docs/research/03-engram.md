# Brief 03 — Weaviate Engram

> Public source: https://weaviate.io/product/engram (reviewed 2026-06-10).
> Engram is Weaviate's managed memory service for agents; Stowage adopts its
> pipeline shape and its framing (D-007), implemented self-hosted and
> storage-agnostic.

## The framing

"Memory isn't a feature you add to your agent. It's infrastructure that the
rest of your system depends on" — it needs storage-layer guarantees:
predictable performance, hard isolation, durability, lifecycle management.
Stowage's positioning sentence.

## The five concepts (all adopted)

1. **Topics as magnets.** What matters is configured in natural language;
   extraction only produces memories matching a topic. Control over relevance
   lives in config, not prompt hacks. → RFC §5.4.
2. **Active reconciliation.** New information triggers retrieval of related
   memories; an LLM tool call decides: deduplicate, rewrite to reflect updates,
   keep separate, or delete. "A memory system needs to *forget* stuff in order
   to be actually useful." → RFC §6, pillar P4.
3. **Buffers for multi-agent systems.** Memories collect across pipeline runs
   and context windows, then flush when trigger conditions are met — how a
   multi-agent system learns continuously without agents blocking each other.
   → RFC §4.1; pairs naturally with Harbor task groups (RFC §10).
4. **Scopes for proper isolation.** Project-wide shared memories; user-scoped
   with hard isolation (multi-tenancy); property-scoped soft isolation (e.g.
   per-conversation). Enforced at both write and read time. → pillar P3.
5. **Fire-and-forget API.** Pipelines run asynchronously; callers call and
   continue — no blocking on memory I/O, no manual background-task management.
   Pipeline stages: extract → reconcile → committed. → pillar P2.

## What Stowage does differently

- Engram inherits its guarantees from being built *inside* Weaviate; Stowage
  gets them from the store seam (sqlite/postgres) and stays a single deployable
  binary with no vector-DB dependency.
- Engram is a managed service; Stowage is self-hosted infrastructure for the
  Harbor ecosystem with an in-process embedding option.
- Stowage adds the fidelity layer (verbatim records + drill-down, brief 04) and
  the utility/decay scoring model (brief 02) on top of the Engram pipeline
  shape — public descriptions of Engram don't detail ranking or provenance
  recovery.
