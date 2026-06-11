# Phase 01 — Scaffold & CI

- **Status:** in-progress
- **Owning subsystem(s):** repo infrastructure (Makefile, CI, scripts, hooks)
- **RFC sections:** §2 (what Stowage is), §15 (phasing)
- **Depends on phases:** —
- **Informing briefs:** 01 (predecessor weight as the anti-goal), 02 (surface/
  config sprawl cautions), 06 (single-binary positioning)

## Goal

A fresh clone builds, tests, lints, and gates itself: `make preflight` and CI
enforce the binding norms (mirror, drift-audit, race-clean tests, CGo-free
build) before any product code exists. Much of this landed at bootstrap; this
phase formalizes it, closes gaps, and proves it from a clean checkout.

## Brief findings incorporated

- Brief 01: the predecessor's 76k-line accretion happened without mechanical
  drift gates — drift-audit and the mirror check run in CI from day zero.
- Brief 06: the single CGo-free binary is a differentiator — `make build` pins
  `CGO_ENABLED=0` and CI builds it on every push.

## Findings I'm departing from

- None. (Deviation note: the per-package coverage band checker described in
  CLAUDE.md §11 is deferred to Phase 02, when the first real package with a
  threshold exists; `make coverage` currently prints total coverage only.
  Recorded here per §4.3.)

## Design

Already in-tree from bootstrap: Makefile (build/test/coverage/bench/vet/lint/
drift-audit/check-mirror/preflight/install-hooks), `.github/workflows/ci.yml`
(build-test, hygiene, lint jobs), `scripts/drift-audit.sh` (mirror, forbidden
names, required artifacts, plan-brief citations, decision-ID uniqueness),
`scripts/smoke/_template.sh`, pre-commit hook, `.golangci.yml`,
`.editorconfig`, `cmd/stowage` stub binary + `internal/version`.

This phase adds: this plan; `scripts/smoke/phase-01.sh`; a `.github/`
PR template carrying the §14 checklist; `git config advice` none — and runs
the fresh-clone proof.

## Files added or changed

```text
docs/plans/phase-01-scaffold.md      (this file)
scripts/smoke/phase-01.sh
.github/pull_request_template.md
```

## Config keys added

None.

## Acceptance criteria (binding)

1. `make preflight` passes on a fresh `git clone` with only Go 1.26 installed.
2. CI is green on the PR (build-test, hygiene, lint jobs).
3. `make check-mirror` fails when AGENTS.md and CLAUDE.md differ (negative test).
4. `scripts/drift-audit.sh` fails when a forbidden predecessor name is
   introduced (negative test).
5. `bin/stowage version` prints the build version; unknown subcommands exit 2.

## Smoke script

`scripts/smoke/phase-01.sh` — builds the binary, runs version, checks mirror,
drift-audit, required-artifact presence, and the two negative tests in a temp
copy.

## Test plan

No Go packages with logic yet (stub main is exercised by smoke). Negative
tests for the gates run inside the smoke script against a temp clone of the
worktree.

## Risks & mitigations

- CI/lint version skew (golangci-lint-action `latest` vs local) → config kept
  minimal; pin if it flakes.

## Glossary additions

None.

## Decisions filed

None (bootstrap decisions D-001–D-015 already cover this surface).
