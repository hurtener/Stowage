# Phase h6 — Co-mount MCP-over-HTTP onto `stowage serve` (one process, both surfaces, one stack)

- **Status:** draft | approved | in-progress | shipped
- **Owning subsystem(s):** `cmd/stowage` (runServe), `internal/config` (one new knob), `internal/mcpserver` (reused as-is), `internal/api` (unchanged)
- **RFC sections:** §9.2 (deployment shape — the canonical one-process server), §9.5 (one logic core, thin tiered surfaces)
- **Depends on phases:** h1 (`boot.StartPipeline`), h3/h4 (the MCP surface), h5 — all shipped
- **Informing briefs:** 01 (Python predecessor surface-sprawl / operability), 02 (config knob-paralysis cautionary tale)
- **Program:** D-073 named follow-up (the co-mount implementation). New decision: **D-074**.

## Goal

When this phase is done, a single `stowage serve` process exposes **both** the
HTTP API and the MCP-over-HTTP surface over **one** `boot.Stack` and **one**
`boot.StartPipeline` — one result cache, one lifecycle sweep set, no cross-process
cache-staleness. This realizes the D-073 canonical deployment shape (a write via
HTTP is immediately reflected by an MCP retrieve, because both share the same
cache and pipeline). `stowage mcp` (stdio + standalone-http) is unchanged.

## Brief findings incorporated

- **Brief 02 (knob paralysis):** add at most ONE knob, with a tuned default and a
  profile placement — co-mount is configured by a single listen address, not a
  cluster of flags.

## Findings I'm departing from

- **Single-port path-prefix (`/mcp` on the api listener) is rejected** — not a
  departure from the RFC, a design finding: the api `http.Server` sets a
  `WriteTimeout` (correct for REST) and a body-limit + request-logging middleware
  chain; the MCP HTTP transport streams and deliberately runs with **no**
  `WriteTimeout` (`runMCP` sets only `ReadHeaderTimeout`). Mounting MCP under the
  api server would let `WriteTimeout` truncate MCP streams and wrap MCP in REST
  middleware. So co-mount uses **two listeners over one shared stack** — the
  shared `Stack`+`StartPipeline` (not a shared port) is what delivers the D-073
  cache-coherence win.

## Design

In `runServe`, after `boot.StartPipeline` (which already owns the one pipeline),
**also** build the MCP server from the *same* stack and serve it on a second
listener — mirroring `runMCP`'s HTTP mode but sharing `stk` and `p`:

```go
// existing: api server over cfg.Server.Listen, fed by p.In / p.Stage.
// NEW (only when cfg.Server.MCPListen != ""):
mcpSvc := &mcpserver.Services{
    Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc,
    GrantsSvc: stk.GrantsSvc, PipelineIn: p.In, Log: stk.Log,
    ScopeFn: mcpserver.CtxScopeFn(), // tenant from the authenticated key
}
mcpSrv, _ := mcpserver.New(serverInfo, mcpSvc)
mcpHTTP := &http.Server{
    Addr: cfg.Server.MCPListen,
    Handler: mcpserver.KeyringMiddleware(stk.Store.Keys(), mustHTTPHandler(mcpSrv)),
    ReadHeaderTimeout: 10 * time.Second, // no WriteTimeout — MCP streams
}
// serve in a goroutine; on shutdown: mcpHTTP.Shutdown THEN api srv.Shutdown THEN p.Drain.
```

Both surfaces enqueue ingest onto the **same** `p.In` and read through the **same**
`stk.Retriever` (one cache). Shutdown order honors the ingress-before-Drain
invariant (h1): stop BOTH HTTP listeners (await both `Shutdown`s) before
`p.Drain` closes the ingest channel. The MCP server is built only when
`MCPListen` is set, so default `stowage serve` is byte-for-byte unchanged.

### Why this is correct (one logic core)
No capability is re-implemented — the co-mounted MCP is the *same* `mcpserver`
handlers (h3/h4/h5 work) over the *same* core; this phase is pure process
wiring. The cache-coherence guarantee is structural: one `stk.Retriever.Cache()`,
invalidated in the reconcile core (Wave-B checkpoint), shared by both surfaces.

## Files added or changed

```text
cmd/stowage/main.go         # runServe: optional second MCP listener over the shared stack; dual-shutdown
internal/config/*.go        # Server.MCPListen knob + validation + profile placement + config explain
scripts/smoke/phase-h6.sh   # NEW — one `serve` process answers REST + MCP; HTTP write visible via MCP retrieve
test/integration/comount_test.go  # NEW
docs/plans/README.md ; docs/glossary.md ; example config
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `server.mcp_listen` | `""` (off) | When set (e.g. `:8081`), `stowage serve` also serves MCP-over-HTTP on this address over the same stack. Empty = single-surface serve (unchanged zero-config). Placed in all three profiles; surfaced by `stowage config explain`; documented in the example config (D-034). |

**OPEN QUESTION for the owner — the default.** Recommendation: **default empty
(opt-in)** so zero-config `stowage serve` keeps binding exactly one port (no
surprise second bound port), and the canonical both-surfaces shape is one
documented config line. Alternative: a default port (on-by-default) to make the
canonical shape the literal default. Default chosen: **opt-in (empty)** unless you
say otherwise — recorded before implementation.

## Acceptance criteria (binding)

1. With `server.mcp_listen` set, ONE `stowage serve` process answers both a REST
   call (`POST /v1/records`, `POST /v1/retrieve`) and an MCP `CallTool`
   (`memory_ingest`/`memory_retrieve`) — proven by the smoke + integration test.
2. **Cache-coherence (the point):** a memory written/derived via the HTTP surface
   is visible to an MCP `memory_retrieve` with no stale window — same cache, same
   pipeline (integration test asserts it).
3. Both listeners share ONE `boot.Stack` + ONE `boot.StartPipeline` (no second
   pipeline/sweep set); shutdown stops both listeners before `p.Drain` (no
   send-on-closed-channel panic — h1 invariant); `-race` clean.
4. Default (`mcp_listen` empty) `stowage serve` is unchanged (single listener);
   the new knob has a tuned default + profile placement + `config explain` +
   example-config docs (D-034).
5. `stowage mcp` (stdio + standalone http) is unchanged.

## Smoke script

`scripts/smoke/phase-h6.sh` — build; `stowage serve` with `server.mcp_listen` set
(sqlite + mock gateway); ingest via REST, retrieve via MCP stdio?—no, via the MCP
HTTP listener (or assert the listener answers `initialize`); assert an
HTTP-ingested+flushed memory is returned by an MCP retrieve on the second port.
SKIP-graceful pre-build.

## Test plan

- **Integration (§17):** `comount_test.go` — boot one serve process (httptest on
  both the api handler and the MCP handler over a shared stack), ingest via REST →
  flush → MCP retrieve returns it; assert cache-coherence (no stale read);
  `-race`; cover a shutdown path (both listeners drain, no panic).
- **Unit:** config validation for `mcp_listen` (valid addr / empty / bad).

## Risks & mitigations

- *Risk:* dual-listener shutdown races the ingest-channel close. *Mitigation:* await
  BOTH `Shutdown`s before `p.Drain` (h1 ingress-before-Drain invariant); race test.
- *Risk:* a second bound port surprises operators. *Mitigation:* default empty
  (opt-in); documented.
- *Risk:* knob sprawl. *Mitigation:* exactly one knob, profile-placed, documented.

## Glossary additions

- **Co-mounted server** — one `stowage serve` process serving both the HTTP API and
  MCP-over-HTTP over a single `boot.Stack`+`StartPipeline` (D-073/D-074).

## Decisions filed

- **D-074** — `stowage serve` optionally co-mounts MCP-over-HTTP on a second
  listener (`server.mcp_listen`) over the same `boot.Stack`+`StartPipeline`,
  realizing the D-073 canonical one-process/both-surfaces shape with one shared
  cache + pipeline (no cross-process staleness). Two listeners (not one
  path-prefixed port) because MCP streams and must not inherit the REST
  `WriteTimeout`/middleware. `stowage mcp` standalone is unchanged.
