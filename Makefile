SHELL := /bin/bash
BIN   := bin/stowage
PKG   := ./...
VERSION ?= dev
LDFLAGS := -s -w -X github.com/hurtener/stowage/internal/version.Version=$(VERSION)

.PHONY: build test coverage bench slo profile eval-ci vet lint drift-audit check-mirror preflight install-hooks clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/stowage

test:
	CGO_ENABLED=1 go test -race $(PKG)

coverage:
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out $(PKG)
	@go tool cover -func=coverage.out | tail -1
	@bash scripts/coverage-check.sh coverage.out

bench:
	go test -bench=. -benchmem -run=^$$ $(PKG)

# eval-ci runs the deterministic CI eval harness (Phase 13).
# Uses the mock gateway; no external network calls.
# Checks the benchmark gate and the gate-bite test.
eval-ci:
	CGO_ENABLED=1 go test -race -v -timeout=5m -run 'TestEvalCI|TestEvalCIGateBites' ./eval/harness/

# slo runs the SLO measurement rig against a live postgres instance.
# Requires STOWAGE_TEST_PG_DSN to be set; skips gracefully when absent.
# The gate BITES (D-095): a p99 over -slo.maxp99 (default = the 150 ms binding
# target, D-031) fails the build. Record the reference-hardware numbers in eval/SLO.md.
slo:
	CGO_ENABLED=1 go test -tags=slo -v -run TestSLO ./internal/bench/slo/ \
	  $(if $(STOWAGE_TEST_PG_DSN),-slo.dsn "$(STOWAGE_TEST_PG_DSN)",)

# profile runs the P1 load+profile rig (D-126). SQLite cut, no external deps.
# Captures CPU/heap/goroutine/block/mutex profiles and the goroutine-stability
# + idle gates (advisory by default; -profile.strict makes them bite). Write
# the baseline to eval/PROFILE.md with -profile.write-baseline.
profile:
	CGO_ENABLED=1 go test -tags=profile -v -run 'TestProfile' ./internal/bench/profile/ $(PROFILE_ARGS)

vet:
	go vet $(PKG)

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed — skipping (CI runs it)"; fi

drift-audit:
	scripts/drift-audit.sh

check-mirror:
	@diff -q AGENTS.md CLAUDE.md && echo "mirror OK"

preflight: build check-mirror drift-audit
	@rc=0; for s in scripts/smoke/phase-*.sh; do [ -e "$$s" ] || continue; bash "$$s" || rc=1; done; \
	  if [ "$$rc" -ne 0 ]; then echo "preflight FAILED — a smoke script reported failure"; exit 1; fi
	@echo "preflight OK"

install-hooks:
	ln -sf ../../scripts/hooks/pre-commit .git/hooks/pre-commit
	@echo "pre-commit hook installed"

clean:
	rm -rf bin coverage.out
