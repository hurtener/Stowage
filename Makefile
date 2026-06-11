SHELL := /bin/bash
BIN   := bin/stowage
PKG   := ./...
VERSION ?= dev
LDFLAGS := -s -w -X github.com/hurtener/stowage/internal/version.Version=$(VERSION)

.PHONY: build test coverage bench vet lint drift-audit check-mirror preflight install-hooks clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/stowage

test:
	CGO_ENABLED=1 go test -race $(PKG)

coverage:
	CGO_ENABLED=1 go test -race -coverprofile=coverage.out $(PKG)
	@go tool cover -func=coverage.out | tail -1

bench:
	go test -bench=. -benchmem -run=^$$ $(PKG)

vet:
	go vet $(PKG)

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed — skipping (CI runs it)"

drift-audit:
	scripts/drift-audit.sh

check-mirror:
	@diff -q AGENTS.md CLAUDE.md && echo "mirror OK"

preflight: build check-mirror drift-audit
	@for s in scripts/smoke/phase-*.sh; do [ -e "$$s" ] || continue; bash "$$s"; done
	@echo "preflight OK"

install-hooks:
	ln -sf ../../scripts/hooks/pre-commit .git/hooks/pre-commit
	@echo "pre-commit hook installed"

clean:
	rm -rf bin coverage.out
