## What

<!-- One paragraph. Reference the RFC section(s) and the phase: e.g. "Phase 03, RFC §8". -->

## Plan deviations

<!-- None, or each deviation + the plan-file update in this PR (CLAUDE.md §4.3). -->

## Pre-merge checklist (CLAUDE.md §14)

- [ ] `make preflight` passes (build + smoke + drift-audit + mirror)
- [ ] `go test -race ./...` and `golangci-lint run` clean
- [ ] Coverage on touched packages ≥ the phase target
- [ ] New CLI command / endpoint / MCP tool / config key has a smoke check here
- [ ] Reusable artifact changed ⇒ concurrent-reuse test under `-race`
- [ ] Cross-subsystem seam opened/consumed ⇒ integration test (§17)
- [ ] New vocabulary in `docs/glossary.md`; new decisions in `docs/decisions.md`
- [ ] Benchmark gate green (from Phase 13 on)
