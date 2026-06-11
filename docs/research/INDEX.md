# Research briefs — reverse index

Briefs are authoritative for *context*, not design; decisions land in the RFC
and phase plans (CLAUDE.md §2). All briefs are written for this repository —
no predecessor files are vendored (D-003).

| Brief | Subject |
|-------|---------|
| [01](01-predecessor-python.md) | The Python predecessor: architecture, what to keep, what to fix |
| [02](02-predecessor-ccmem.md) | The CC-memory predecessor: scoring & lifecycle model |
| [03](03-engram.md) | Weaviate Engram: pipeline shape and memory-as-infrastructure framing |
| [04](04-cl-bench.md) | CL-Bench (arXiv 2606.05661): failure modes and the gain metric |

## Subsystem → briefs

| Subsystem | Informing briefs |
|-----------|------------------|
| records / fidelity layer | 04, 01 |
| pipeline (buffers, extraction) | 03, 01 |
| topics | 03 |
| reconciliation | 03, 02 |
| retrieval (lanes, fusion) | 01, 02, 04 |
| scoring & ranking | 02, 04 |
| lifecycle (decay, sweeps, supersede) | 02, 01, 04 |
| gateway (embeddings, LLM) | 01 (pain points), Harbor's bifrost driver pattern |
| store / vindex | 01, 02 (contention lessons) |
| scopes & privacy | 03, 01 |
| eval | 04, 02 (LoCoMo methodology) |
| API / MCP surface | 01, 02 (surface-sprawl cautionary tales) |
