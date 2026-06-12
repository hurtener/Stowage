module github.com/hurtener/stowage/adapters/harbor

go 1.26.3

require (
	github.com/hurtener/Harbor v1.3.1
	github.com/hurtener/stowage v0.0.0
)

// replace points to the repo root for both local development and CI checkout.
// In CI the adapter job runs with `cd adapters/harbor && go build ./... && go test ./...`
// from a checkout where the stowage root is at `../..` relative to this file.
// For a released version of the adapter, this replace is removed and the stowage
// version pin is updated to match. See adapters/harbor/README.md.
replace github.com/hurtener/stowage => ../..
