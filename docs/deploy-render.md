# Deploy Stowage to Render (free) + Neon (free) — ~5 minutes

This is the reproducible path for putting Stowage — the memory server — online for
free and wiring it into a running Pengui console as the **memory capability** its
runtimes call. Nothing is installed on your machine; the cloud builds the image.

**Why this is easy for Stowage specifically.** Stowage is a *verifier*: in `jwt`
mode it reads the console's public keys over HTTPS and holds **no private key on
disk**. So the classic Render free-tier wall — a secret file mounts group-readable
(`0640`) but the code wants owner-only (`0600`) — **does not apply here**. Every
secret is a plain environment variable. And all durable state lives in Neon
Postgres, so Render's ephemeral filesystem never loses anything.

**The one wrinkle, already solved.** Render free exposes exactly **one port** per
service, but the console reaches Stowage over the **MCP** surface, which normally
binds its own second port. Set **`STOWAGE_SERVER_MCP_MOUNT=shared`** and Stowage
co-mounts MCP on the API port under `/mcp` — one port serves both the REST API and
the MCP tools (D-155). The blueprint sets this for you.

## What you need before you start (all free)

- A **Neon** account (Postgres).
- A **Render** account, connected to this GitHub repo.
- Your **OpenRouter API key** (Stowage's intelligence gateway — embeddings + LLM).
- A **Pengui console already running** (the identity provider). This guide uses
  `https://pengui-white-label.onrender.com`. Confirm its JWKS loads:
  `https://pengui-white-label.onrender.com/.well-known/jwks.json`.

---

## 1. Neon — one project, one database (~60 seconds)

Stowage is a single service with a single `Store`, so it needs **one** database
(no per-subsystem split). It creates its own tables on first boot — migrations are
forward-only and apply idempotently on startup, so point it at an empty database.

1. Neon → **New Project** (name it `stowage`). Note its region.
2. Click **Connect** and copy the pooled connection string. It looks like:

   ```
   postgresql://USER:PASSWORD@ep-xxx-pooler.REGION.aws.neon.tech/neondb?sslmode=require&channel_binding=require
   ```

   That is your `STOWAGE_STORE_DSN`. `neondb` (the default database) is fine — you
   do not need to create another. The pgx driver accepts this DSN verbatim,
   including `-pooler`, `sslmode=require`, and `channel_binding=require`.

   > If boot logs show a driver error mentioning `channel_binding`, drop that one
   > parameter and keep `sslmode=require`.

---

## 2. Render — create the service (~2 minutes)

Two equivalent ways. **Path B (Blueprint)** is the fastest; Path A is the most
explicit.

### Path B — Blueprint (one-click, uses `render.yaml`)

Render Dashboard → **New → Blueprint** → pick this repo → **Apply**. Render reads
the repo-root `render.yaml`, shows the `stowage` service, and prompts for the four
secrets below (marked `sync: false` — never in git). Fill them, Apply, watch the
build log until **Live** turns green.

### Path A — New Web Service (manual)

Render Dashboard → **New → Web Service** → connect this repo. Render detects the
`Dockerfile` (Docker runtime). Set:

- **Name:** `stowage`
- **Instance Type:** Free
- **Health Check Path:** `/healthz`
- **Dockerfile Path:** `./Dockerfile`

Then add the environment variables:

| Env var | Value |
|---|---|
| `STOWAGE_SERVER_MCP_MOUNT` | `shared` — co-mount MCP on the one port under `/mcp` (D-155) |
| `STOWAGE_STORE_DRIVER` | `postgres` |
| `STOWAGE_STORE_DSN` | the Neon DSN from step 1 |
| `STOWAGE_GATEWAY_API_KEY` | your OpenRouter key (the one zero-config secret) |
| `STOWAGE_AUTH_MODE` | `jwt` |
| `STOWAGE_AUTH_JWKS_URL` | `https://pengui-white-label.onrender.com/.well-known/jwks.json` |
| `STOWAGE_AUTH_ISSUER` | the console's token issuer (from the console's capability panel) |
| `STOWAGE_AUTH_AUDIENCE` | the audience the console mints for memory calls (from the capability panel) |

**Don't set `PORT`.** The image already binds `0.0.0.0:10000`, which is Render's
default web port, so it's detected automatically. (If your service ever reports a
port-scan timeout, set `STOWAGE_SERVER_LISTEN=0.0.0.0:$PORT`'s value explicitly —
e.g. `0.0.0.0:10000` — to match what Render assigned.)

Create the service. First build is a few minutes (Go pulls modules from the public
proxy — no setup). When it's live:

```bash
curl https://stowage.onrender.com/healthz
# {"status":"ok"}
```

> **Want to smoke-test the deploy before wiring identity?** Boot once in the
> default `keyring` auth mode: omit the four `STOWAGE_AUTH_*` vars. `/healthz`
> answers and the server runs standalone; add the `STOWAGE_AUTH_*` vars and
> redeploy to switch to console-issued `jwt` identity.

---

## 3. Register it in the console (~1 minute)

Stowage now serves **both** surfaces on one URL:

- REST API + health: `https://stowage.onrender.com/`
- **MCP tools (what the console connects to): `https://stowage.onrender.com/mcp`**

In the Pengui Console → **Capabilities → add the memory (Stowage) capability**:

- **MCP endpoint** — `https://stowage.onrender.com/mcp`
- **Audience** — must match `STOWAGE_AUTH_AUDIENCE` you set in step 2.
- Activate it on the runtime(s) that should have memory.

The console then pushes the memory MCP connection + run-completion ingest hook to
the runtime, and every memory call carries a per-user, console-minted bearer that
Stowage verifies against the JWKS it fetched at boot.

---

## 4. What to expect (free-tier behavior — this is normal)

- **Cold starts.** Render free sleeps the service after ~15 min idle; Neon
  autosuspends after ~5 min. The first request after idle takes ~30–60 s while both
  wake. To the console a sleeping Stowage looks briefly offline until the first
  request wakes it — retry once.
- **Durable across deploys.** Everything lives in Neon; a deploy or spin-down loses
  nothing on Render's ephemeral disk. (The image's `/data` SQLite path is only used
  if you *don't* set `STOWAGE_STORE_DRIVER=postgres` — for a managed deploy you
  always do.)
- **One service, one Neon database.** Independent of any other deployment.

---

## 5. If something goes wrong (the errors name their own fix)

| In the Render logs | Meaning | Fix |
|---|---|---|
| `jwks ... unreachable` / boot loop | the console isn't up, or `STOWAGE_AUTH_JWKS_URL` is wrong | Confirm the console's `/.well-known/jwks.json` loads over HTTPS; Stowage fails loud (D-147) rather than start with unverifiable identity |
| DB error mentioning `channel_binding` | the driver rejected that DSN param | Remove `&channel_binding=require` from `STOWAGE_STORE_DSN` (keep `sslmode=require`), redeploy |
| `relation ... does not exist` on first calls | migrations didn't apply | They apply on boot; check the boot log for a migrate error and that the DSN points at a reachable, empty database |
| `invalid config: config.server.mcp_listen: must be empty when server.mcp_mount=shared` | both co-mount modes set | `shared` owns the one port — unset `STOWAGE_SERVER_MCP_LISTEN` |
| console can't reach the MCP endpoint | wrong path or port | The endpoint is `https://<app>.onrender.com/**mcp**` (co-mount lives at `/mcp`), not the bare host |
| `token_expired` / `auth_rejected` in the console | short-lived bearer aged out | Console-side re-mint; retry the request |
| gateway/embedding errors | the OpenRouter key didn't land | Set `STOWAGE_GATEWAY_API_KEY` in Render → Environment, redeploy |

---

## 6. Moving off the free tier

The Docker image is the portable artifact: the same `Dockerfile` runs on any
container host. To take Stowage to a paid Render tier (or elsewhere) with a **real
disk and a real network**, drop `STOWAGE_SERVER_MCP_MOUNT=shared` and use the
default **two-listener** shape (`server.mcp_listen`, D-074) — separate ports for
REST and MCP, the production-recommended layout. Everything else — Postgres DSN,
jwt identity, the OpenRouter key — carries over unchanged.
