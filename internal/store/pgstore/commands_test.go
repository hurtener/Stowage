package pgstore_test

import (
	"testing"

	"github.com/hurtener/stowage/internal/store/conformance"
)

func TestCommandGuardContract(t *testing.T) {
	st, closeStore := openStore(t)
	defer closeStore()
	conformance.RunCommands(t, st)
}
