package lifecycle_test

import (
	"testing"

	"github.com/hurtener/stowage/internal/leakcheck"
)

func TestMain(m *testing.M) {
	leakcheck.Run(m, leakcheck.Advisory)
}
