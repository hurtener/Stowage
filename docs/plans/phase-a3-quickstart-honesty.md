# Phase a3 — Quickstart honesty & MCP opt-in clarity

- **Status:** shipped
- **Owning subsystem(s):** `README.md`, `docs/`, `cmd/stowage`
- **RFC sections:** §9.2 (MCP deployment shape), §9.4 (five-minute rule), §9.5 (tiered surfaces)
- **Depends on phases:** a1 (gateway default makes the one-secret claim true), h6/D-074 (MCP co-mount)
- **Informing briefs:** `docs/research/06-mempalace.md` (benchmarks/adoption as marketing — copy must be trustworthy); `docs/research/02-predecessor-ccmem.md` (small, honest surface)

## Goal

After this phase, the README quickstart and getting-started doc match what the binary actually
does: the HTTP API is the single default surface, the MCP tool surface is **opt-in** via
`server.mcp_listen`, and the one secret reaches the default OpenRouter stack. `stowage serve`
logs a one-line hint when MCP is off so the knob is discoverable. MCP stays opt-in (D-074
reaffirmed, not reversed).

## Brief findings incorporated

- **Benchmarks/adoption as marketing (brief 06).** The front-door copy is a trust surface; a
  claim the binary doesn't honor undermines the whole "run it, don't trust us" posture.
- **Small, honest surface (brief 02).** Don't imply surfaces that aren't on by default.

## Findings I'm departing from

- None. This reaffirms D-074 (MCP opt-in) rather than changing it; the owner chose "keep opt-in,
  make it explicit" over "on by default".

## Design

- **README quickstart** (`README.md`): the serve step states HTTP-only-by-default + MCP opt-in via
  `server.mcp_listen`; the secret step names the OpenRouter default and points other providers at
  `provider`/`base_url` (and `mock` for offline). The surfaces table and Deploy section mark MCP
  co-mount opt-in.
- **getting-started** (`docs/getting-started.md`): the "what you get" list no longer implies a
  co-mounted MCP listener by default; a dedicated paragraph explains the opt-in knob and the
  startup hint. (The glossary "Co-mounted server" entry was already accurate — no change.)
- **Startup hint** (`cmd/stowage/main.go`, `runServe`): when `cfg.Server.MCPListen == ""`, log one
  info line naming the knob. No change to the default single-port shape (D-074).

## Files added or changed

```text
README.md                                   # quickstart + surfaces table + deploy: MCP opt-in, real one-secret
docs/getting-started.md                     # MCP opt-in wording + startup-hint note
cmd/stowage/main.go                          # MCP-disabled startup hint (else branch)
scripts/smoke/phase-a3.sh                    # new smoke
docs/plans/phase-a3-quickstart-honesty.md    # this plan
docs/decisions.md                            # D-133
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| _(none)_ | — | Docs + one startup log line; no new config. |

## Acceptance criteria (binding)

1. README quickstart names `STOWAGE_GATEWAY_API_KEY` and states MCP is opt-in via `server.mcp_listen`; it no longer implies an auto co-mount.
2. `stowage serve` with `server.mcp_listen` empty logs the one-line MCP-disabled hint; with it set, the existing co-mount path is unchanged.
3. `scripts/smoke/phase-h6.sh` (single-surface default, AC-4) still passes unchanged.

## Smoke script

`scripts/smoke/phase-a3.sh` — README honesty (names the key, marks MCP opt-in, no auto-co-mount
claim); the startup hint fires when `mcp_listen` empty; phase-h6 still green.

## Test plan

Smoke-only (docs + one log line). `phase-a3.sh` runs the built binary for the hint and re-runs
`phase-h6.sh` to prove no single-surface regression.

## Risks & mitigations

- **Doc drift recurs** → the smoke asserts the specific honest phrasings, so a future edit that
  re-introduces the auto-co-mount claim fails the gate.

## Glossary additions

- None (the "Co-mounted server" and "Five-minute rule" entries already cover this).

## Decisions filed

- D-133: Quickstart copy tracks shipped defaults; MCP stays opt-in (reaffirms D-074).
