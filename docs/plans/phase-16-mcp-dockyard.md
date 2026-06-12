# Phase 16 — MCP server via Dockyard runtime

- **Status:** draft
- **Owning subsystem(s):** `internal/mcpserver` (new), `cmd/stowage` (`mcp`)
- **RFC sections:** §9.2, D-015/D-018 (seven tools), D-020
- **Depends on phases:** 15
- **Informing briefs:** docs/research/03-engram.md (tool-surface design),
  docs/research/01-predecessor-python.md (MCP integration patterns)
- **Grounding (verified 2026-06-12):** Dockyard runtime is a pure library —
  `github.com/hurtener/dockyard` is PUBLIC, tagged v1.7.3;
  `runtime/{server,tool,obs}` import as normal deps; schemas generate
  at registration from Go types (`tool.New[In,Out]("name").Describe().
  Handler().Register(srv)`); `srv.ServeStdio(ctx)` / `srv.HTTPHandler(nil)`
  (secure defaults); NO dockyard.app.yaml or codegen needed at runtime
  (evidence: examples/backend-tools-only). The `dockyard generate/validate`
  manifest workflow is for scaffolded projects — embedding skips it
  (deviation from the original "Dockyard validate gates" framing in D-020 —
  record amendment: contract goldens in-repo replace it).

## Design

- `internal/mcpserver`: typed contracts for the **seven tools** (D-015/18):
  `memory_ingest`, `memory_retrieve`, `memory_playbook` (registered;
  returns a typed not-implemented-until-Phase-19 error result),
  `memory_drilldown`, `memory_feedback`, `memory_assert`, `memory_topics`.
  Input/output structs mirror the HTTP v1 envelopes 1:1 (citations,
  response_id, degraded flags included) — one source of truth: thin handlers
  delegating to the SAME internal services the HTTP handlers use (extract a
  small `internal/api` service facade if handlers are currently
  HTTP-coupled; no logic duplication).
- **Auth/scoping**: stdio mode = single-tenant by config (the embedded/
  desktop posture: tenant from config, no bearer); HTTP mode reuses the
  Phase 05 key middleware in front of Dockyard's HTTPHandler. `memory_assert`
  = the PATCH assert/correct/quarantine surface (user_stated writes).
- `stowage mcp [--stdio|--http addr]` boots store+gateway+pipeline exactly
  like serve (shared boot helper — refactor cmd boot into one function used
  by both).
- **Contract goldens**: each tool's generated input/output JSON Schema
  (via the public `tool.Builder.Schemas()`) golden-pinned in
  `internal/mcpserver/testdata/` — our drift gate replacing `dockyard
  validate` (D-061).
- Smoke: stdio session driving each tool via JSON-RPC over a pipe
  (initialize → tools/list asserts 7 → call each happy-path with temp
  sqlite + scripted mock gateway; playbook expects the typed
  not-implemented).

## Acceptance criteria (binding)

1. `tools/list` over stdio returns exactly 7 tools with schemas matching the
   goldens.
2. Each tool round-trips its happy path over stdio (smoke) hitting the real
   pipeline (ingest→flush→extract→commit visible via memory_retrieve).
3. Tool handlers share service code with HTTP (no duplicated business
   logic — review gate); envelope parity test: memory_retrieve output ==
   HTTP /v1/retrieve for the same state (golden).
4. HTTP transport serves behind key auth; stdio is config-tenant scoped;
   cross-tenant impossible in both (tests).
5. memory_playbook returns the typed stub error; memory_assert performs
   assert/correct/quarantine with user_stated trust.
6. Schema goldens fail on contract drift (mutation: rename a field → golden
   test fails).
7. eval-ci green; CGo-free build with the new dep; coverage ≥80 mcpserver;
   race ×3; smokes 01–16.

## Files added or changed

```text
internal/mcpserver/{server.go, contracts.go, handlers.go, *_test.go, testdata/}
cmd/stowage/main.go (mcp subcommand + shared boot)
internal/api (service facade extraction if needed — minimal)
go.mod (+ github.com/hurtener/dockyard v1.7.3)
scripts/{coverage.json, smoke/phase-16.sh}
```

## Decisions filed

- D-061: Dockyard integration = runtime-library embedding (public v1.7.3);
  manifest/codegen workflow skipped; in-repo schema goldens are the contract
  gate. Amends D-020's "validate gates" wording.
