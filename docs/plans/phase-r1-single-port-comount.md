# Phase r1 — single-port MCP co-mount (deploy enablement)

- **Status:** shipped
- **Owning subsystem(s):** `cmd/stowage` (serve wiring), `internal/config`
- **RFC sections:** §2 (one binary, three surfaces), §9.5 (thin tiered surfaces)
- **Depends on phases:** a3 (MCP opt-in honesty, D-133), ae11 (method-aware MCP handshake auth, D-152)
- **Informing briefs:** none — this is deployment-enablement plumbing, not a product-capability phase. The motivating context is external (deploying Stowage as the console's memory capability onto single-port PaaS free tiers — Render/Neon); see `docs/deploy-render.md`.

## Goal

`stowage serve` can expose the MCP-over-HTTP surface on the **same port** as the
REST API (under `/mcp`), so Stowage deploys as the console's memory capability on
single-port PaaS free tiers (Render, Heroku, Fly) that give one port per service.
The default two-listener shape (D-074) is unchanged; this adds one opt-in mode.

## Brief findings incorporated

- None (no brief). The two-listener rationale (D-074) and the identical pprof
  rationale (D-126) are the load-bearing prior art: MCP streams and must not
  inherit the REST `WriteTimeout`/body-limit middleware.

## Findings I'm departing from

- **D-074 said "never a path prefix on the api listener."** This phase adds a
  path-prefix mode *without weakening the invariant D-074 protects* — see D-155.
  The invariant (MCP not gated by the REST `WriteTimeout`) is preserved by leaving
  the shared server's `WriteTimeout` unset and re-imposing the REST bound
  per-request. Settled in **D-155**.

## Design

A new config knob `server.mcp_mount` — `"separate"` (default) | `"shared"`:

- **separate** — D-074 unchanged. MCP is opt-in via `server.mcp_listen` and binds
  its own port.
- **shared** — co-mount MCP on the `server.listen` port under `/mcp`.
  `server.mcp_listen` must be empty (mutually exclusive; config rejects the pair).

In `runServe` (`cmd/stowage/main.go`) the MCP handler is built for either mode
(same `mcpserver.Services` over the same stack + pipeline, same `mcpAuthHandler`).
In shared mode a single `http.Server` on `server.listen` serves a root mux:

- `/mcp` and `/mcp/` → `mcpRootRewrite(mcpHTTPHandler)` — normalizes the request
  path to `/` (MCP dispatches JSON-RPC on the body, not the URL; the ae11 auth
  classifier peeks the body), so a `/mcp` request is byte-identical at the handler
  to the same request on a dedicated listener.
- `/` → `restDeadlineHandler(read, write, srv)` — the api server's own
  middleware-wrapped handler, with the per-request read/write deadline re-imposed
  via `http.ResponseController`.

The shared server sets **no** `ReadTimeout`/`WriteTimeout` (so MCP streams);
`ReadHeaderTimeout` guards both subtrees. Shutdown order: combined listener first
(drains both subtrees), then `srv.Shutdown` (post-HTTP retriever/pipeline cleanup;
its own `httpSrv.Shutdown` no-ops because it never served), then `p.Drain`.

## Files added or changed

```text
internal/config/config.go            # MCPMount field, default, allKeys, envKeys, get/setByPath, Validate, FillZeroDefaults
internal/config/config_test.go       # TestMCPMount* (validation, default, env override)
internal/config/testdata/explain_default.golden  # + server.mcp_mount row
cmd/stowage/main.go                  # runServe co-mount wiring; mcpRootRewrite + restDeadlineHandler helpers
scripts/smoke/phase-r1.sh            # co-mount smoke (jwt mode, one port, REST + MCP)
docs/decisions.md                    # D-155
docs/deploy-render.md                # the 5-minute Render + Neon deploy recipe
Dockerfile                           # CGo-free distroless image
render.yaml                          # Render blueprint
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `server.mcp_mount` | `separate` | `separate` = two-listener (D-074); `shared` = co-mount MCP on `server.listen` at `/mcp`. Env: `STOWAGE_SERVER_MCP_MOUNT`. In every profile via `Defaults()`. |
| `server.mcp_trust_proxy` | `false` | When `true`, relax the MCP transport's DNS-rebinding localhost guard for deployment behind a trusted reverse proxy (cross-origin + Content-Type protection stay on). Env: `STOWAGE_SERVER_MCP_TRUST_PROXY`. Required behind Render/Heroku/Fly (D-156). |

## Acceptance criteria (binding)

1. `server.mcp_mount` defaults to `separate`; the zero-config single-port REST-only
   shape and the h6 single-surface default are unchanged. *(phase-a3, phase-h6)*
2. `server.mcp_mount=shared` co-mounts MCP on the `server.listen` port: `/healthz`
   (REST) and `/mcp` (MCP) both answer on one port. *(phase-r1 AC-1..3)*
3. In jwt mode the co-mounted `/mcp` serves the bearer-less handshake
   (initialize/tools-list) and still gates `tools/call`. *(phase-r1 AC-2..4)*
4. `shared` + a non-empty `server.mcp_listen` fails config validation. *(phase-r1 AC-6, unit test)*
5. Behind a proxy (loopback local addr + public Host), the MCP surface 403s by default and is served
   with `server.mcp_trust_proxy=true` — only the DNS-rebinding guard is relaxed (D-156). *(phase-r1 AC-7/8)*
6. CGo-free build; `-race` clean on `cmd`, `config`, `api`, `mcpserver`.

## Smoke script

`scripts/smoke/phase-r1.sh` — build, mint test JWT+JWKS, boot `serve` in jwt mode
with `mcp_mount=shared`, assert REST `/healthz` + MCP `/mcp` on one port, the
strict `tools/call` gate, the discoverability log line, and the shared+mcp_listen
rejection.

## Test plan

- **Unit:** `TestMCPMountValidation`, `TestMCPMountDefaultSeparate`,
  `TestMCPMountEnvOverride` (`internal/config`); explain golden updated.
- **Integration:** `phase-r1.sh` boots the real binary end-to-end in jwt mode over
  one port (real MCP handler, real auth), covering the console's connection shape.
- **Regression:** `phase-a3.sh` (MCP opt-in honesty) and `phase-ae11.sh` (dedicated
  listener handshake) still pass — both shapes coexist.

## Risks & mitigations

- **Streaming truncation on the shared port** → the shared server sets no
  `WriteTimeout`; only the REST subtree gets a per-request write deadline (D-155).
- **Path routing to the MCP handler** → `mcpRootRewrite` normalizes to `/`; proven
  by the jwt-mode handshake succeeding through `/mcp` in the smoke.
- **Auth drift between shapes** → both use the same `mcpAuthHandler`; the smoke
  asserts the strict `tools/call` gate on the co-mount.

## Glossary additions

- **Co-mount (shared)** — exposing the MCP surface on the REST port under `/mcp`.

## Decisions filed

- **D-155** — Single-port MCP co-mount (`server.mcp_mount=shared`).
- **D-156** — `server.mcp_trust_proxy` relaxes the MCP DNS-rebinding localhost guard behind a trusted proxy.
