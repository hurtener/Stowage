# Phase 02 — Config, identity, telemetry, keys, profiles

- **Status:** done
- **Owning subsystem(s):** `internal/config`, `internal/identity`,
  `internal/telemetry`, `internal/auth`
- **RFC sections:** §9.4 (five-minute rule), §5.3 (scopes), §9.1 (keys), §11
  (observability)
- **Depends on phases:** 01
- **Informing briefs:** 02 (50-knob config paralysis — the anti-goal), 06
  (env-first simplicity), 01 (12-factor env-prefix pattern proven in the Python
  predecessor → here: `STOWAGE_*`)

## Goal

The four foundation packages every later phase consumes: typed fail-loud
config with profiles and provenance-aware explain; the identity scope type and
ctx plumbing; slog + Prometheus telemetry with secret redaction; the API-key
model with constant-time verification. After this phase, `stowage config
explain` works with zero config file and exactly one secret env var.

## Brief findings incorporated

- Brief 02: config paralysis is a real adoption killer → profiles bundle every
  tunable; zero-config defaults; the knob guardrail applies from this phase on.
- Brief 01: env-prefix 12-factor config worked well in the predecessor; we keep
  the pattern (`STOWAGE_*`) with fail-closed `env.VAR` secret indirection.

## Findings I'm departing from

- **STOWAGE_GATEWAY_API_KEY not in envKeys:** The plan's env-override table
  implied a direct `STOWAGE_GATEWAY_API_KEY` → `gateway.api_key` config
  override. On implementation, that would write the literal secret value into
  the config field, causing Validate() to fail (D-030 secret-literal guard)
  and Explain() to show "[REDACTED]" instead of the env-ref+status the spec
  describes. Resolution: `STOWAGE_GATEWAY_API_KEY` is the **target** env var
  that the default reference `env.STOWAGE_GATEWAY_API_KEY` resolves to via
  ResolveEnvRef — it is not a config-level override. To point the API key at a
  different env var, set `gateway.api_key: env.MY_KEY` in a config file.
- **Plan file forbidden-name scrub:** The draft plan contained a backtick
  reference to the Python predecessor's project name in the informing-briefs
  line, which caused `make drift-audit` to fail. Replaced with "the Python
  predecessor" per CLAUDE.md predecessor hygiene rules.

## Design

### `internal/config`

```go
type Config struct {
    Profile string        // "assistant" | "coding-agent" | "fleet" (default "assistant")
    Server  ServerConfig  // listen addr, timeouts, body limits
    Store   StoreConfig   // driver: "sqlite" (default) | "postgres"; DSN; migrate mode
    Gateway GatewayConfig // driver: "mock" (default until Phase 04 flips it) | "bifrost";
                          // base URL, APIKey (env-ref), chat model, embed model, dims
    Telemetry TelemetryConfig // log level/format, metrics addr
}
```

- `Defaults()` returns a fully working config (sqlite at `./data/stowage.db`,
  mock gateway, assistant profile).
- `Load(ctx, path string)` — path optional; YAML if present, then env
  overrides (`STOWAGE_SECTION_FIELD`), then validation. goccy/go-yaml (Harbor's
  choice).
- Secret fields are declared `secret:"true"` and hold **references** —
  `env.VAR_NAME` — resolved via `Resolve()` at construction by the consumer;
  a secret field containing a literal (not `env.`) fails validation
  (fail-closed; keys never live in config files — D-030).
- `Validate()` returns every error with its key path
  (`config.gateway.api_key: must use env.VAR indirection`), joined.
- **Profiles**: `Profiles()` map; a profile is a set of tunable defaults
  (initially: log format, future knobs join here). Merge order:
  defaults < profile < file < env. Each value's origin is tracked in a
  `Provenance` map for `Explain()`.
- `Explain(w io.Writer)` prints every effective value + origin
  (default|profile|file|env), secrets redacted to `env.VAR_NAME (set|UNSET)`.
- CLI: `stowage config explain [--config path]` wired in `cmd/stowage`.

### `internal/identity`

```go
type Scope struct{ Tenant, Project, User, Session string }
```
`Validate()` (tenant required; lower levels optional but contiguous — no
session without user), `String()` canonical form, `WithScope(ctx)/FromContext
(ctx)`, and `ErrScopeMissing`. No store coupling.

### `internal/telemetry`

`New(cfg) (*slog.Logger, *prometheus.Registry, error)`: JSON handler (prod) /
text (dev); a `RedactingHandler` that masks attr values for keys in a
deny-set (`api_key`, `authorization`, `dsn`, `secret`); helpers for
identity-stamped loggers (`telemetry.With(ctx, logger)` adds scope attrs).

### `internal/auth`

```go
type Key struct {
    ID        string    // "sk_" + ULID-ish public id
    TenantID  string
    Role      Role      // RoleAgent | RoleAdmin
    Hash      [32]byte  // SHA-256 of the secret part
    CreatedAt time.Time
    RevokedAt *time.Time
}
```
- `Generate(tenant, role)` returns (Key, plaintext) — plaintext =
  `sk_<id>_<43 chars base64url (256-bit)>`, shown once.
- `Verify(keyring, plaintext)` parses id, looks up, constant-time compares
  SHA-256 (`crypto/subtle`), checks revocation. High-entropy random secrets ⇒
  plain SHA-256 + constant-time compare is sufficient (no KDF needed).
- `Keyring` interface (Lookup/Insert/Revoke) with an in-memory implementation;
  the store-backed driver arrives in Phase 03 against the same interface +
  conformance test.

### Coverage band gate (deferred from Phase 01)

`scripts/coverage-check.sh` reads `coverage.out` per package against
`scripts/coverage.json` thresholds; wired into `make coverage` and a new CI
step. Initial bands: the four new packages at 80.

## Files added or changed

```text
internal/config/{config.go, profiles.go, explain.go, config_test.go, ...}
internal/identity/{identity.go, identity_test.go}
internal/telemetry/{telemetry.go, redact.go, telemetry_test.go}
internal/auth/{key.go, keyring.go, key_test.go}
cmd/stowage/main.go                  (config explain subcommand)
scripts/coverage-check.sh, scripts/coverage.json
Makefile                             (coverage target wires the checker)
.github/workflows/ci.yml             (coverage step)
scripts/smoke/phase-02.sh
go.mod                               (+ goccy/go-yaml, prometheus/client_golang)
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `profile` | `assistant` | preset bundle |
| `server.listen` | `:7160` | |
| `store.driver` / `store.dsn` | `sqlite` / `./data/stowage.db` | |
| `gateway.driver` | `mock` | flips to bifrost guidance in Phase 04 |
| `gateway.api_key` | `env.STOWAGE_GATEWAY_API_KEY` | env-ref enforced |
| `gateway.model` / `gateway.embed_model` / `gateway.embed_dims` | sensible defaults | pinned per index (Phase 04 validates) |
| `telemetry.log_level` / `log_format` / `metrics_listen` | `info` / per-profile / `:7161` | |

## Acceptance criteria (binding)

1. `Load` with no file and no env returns a valid, working default config.
2. A secret field with a literal value fails validation; with `env.VAR` unset,
   `Resolve` fails closed with the var name in the error.
3. Validation errors carry full key paths; multiple errors are joined.
4. `stowage config explain` prints every value with provenance; switching
   profile changes the documented values (table test); secrets never printed.
5. Identity ctx round-trip; contiguity validation matrix.
6. `Verify` is constant-time (uses `crypto/subtle`; test asserts revoked and
   wrong-secret both fail; fuzz on key parsing).
7. Redacting handler masks deny-set attrs (test greps rendered output).
8. `make coverage` enforces 80 on the four packages; CI runs it.

## Smoke script

phase-02.sh: `stowage config explain` works env-only; exits non-zero with a
key-path message on an invalid config file; coverage-check runs.

## Test plan

Table-driven validation matrix; golden for `Explain` output; fuzz target on
key parsing (CLAUDE.md §11); race-run on keyring concurrent use.

## Risks & mitigations

- Knob sprawl from day one → only the keys above; the guardrail applies to
  every addition.
- YAML dep weight → goccy/go-yaml is Harbor-proven and pure Go.

## Glossary additions

None (profile already defined).

## Decisions filed

None new (implements D-030, D-034).
