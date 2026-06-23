# Contributing to Stowage

Thanks for considering a contribution. Stowage is a doc-driven, multi-phase build with a
deliberately high hygiene bar — the rules below keep the design coherent as it grows.

## The one source you must read

All contributor norms — for humans *and* AI agents — are binding and live in **[`CLAUDE.md`](CLAUDE.md)**
(mirrored verbatim in `AGENTS.md`). It is the contract. Start there. The essentials:

- **Authoritative sources, in order:** `RFC-001-Stowage.md` → `docs/plans/phase-NN-*.md` →
  `CLAUDE.md` → research briefs → code comments. When a plan and the RFC drift, the RFC wins.
- **The five binding properties (P1–P5)** in `CLAUDE.md §1` are non-negotiable: fidelity first,
  fire-and-forget writes, scopes enforced at write *and* read, memory must forget, one
  intelligence seam (no local models, CGo-free).
- **Settled decisions** live in `docs/decisions.md` (`D-NNN`). Don't re-litigate them silently;
  a change to a settled decision is an RFC PR **plus** a superseding decision entry.

## Before you push

```bash
make preflight   # build + every per-phase smoke + drift-audit (the merge gate)
make test        # go test -race ./...
make lint        # golangci-lint
make coverage    # per-package coverage band gate
make check-mirror  # AGENTS.md == CLAUDE.md
```

The pre-merge checklist is `CLAUDE.md §14`. In short: drift-audit + mirror + preflight pass;
`-race` tests and lint clean; touched-package coverage meets its band; a new CLI command /
endpoint / MCP tool / config key ships with a smoke check in the **same** PR; new vocabulary lands
in `docs/glossary.md`; a new architectural decision is filed in `docs/decisions.md`.

## Conventions

- **Go 1.26, CGo-free** in the shipped artifact (`-race` test runs are the one exception).
  `gofmt -s`, `go vet`, and `golangci-lint` clean. `log/slog` only.
- **Commits:** imperative, scoped (`feat(pipeline): …`, `fix(retrieval): …`, `docs: …`). Small
  and coherent. Commits are unsigned in this repo. End commit messages with the co-author trailer
  the project uses.
- **Branches:** never commit to `main`; use `feat/…`, `chore/…`, `docs/…`. Squash-merge unless
  history is meaningful; CI green is mandatory.
- **PRs:** reference the RFC section(s) and the phase; state any plan deviation and update the
  plan in the same PR.

## Starting a phase

Follow `CLAUDE.md §16` — read the master plan entry, the cited RFC sections, the informing briefs
(`docs/research/INDEX.md`), and the decisions log; then copy `docs/plans/_template.md` and the
smoke skeleton, and run `make drift-audit` + `make preflight` before committing.

## Security

See `CLAUDE.md §7`: no hardcoded secrets (including in tests), store-layer scope isolation,
explicit HTTP hardening, constant-time key comparison, gateway-payload-only egress. Report
sensitive issues privately rather than in a public issue.

## License

By contributing, you agree your contributions are licensed under the project's
[Apache-2.0](LICENSE) license.
