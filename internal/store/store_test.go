package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
)

func TestErrors(t *testing.T) {
	tests := []struct {
		err  error
		name string
	}{
		{store.ErrNotFound, "ErrNotFound"},
		{store.ErrConflict, "ErrConflict"},
		{store.ErrChecksumMismatch, "ErrChecksumMismatch"},
		{store.ErrDriverNotRegistered, "ErrDriverNotRegistered"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatal("error must not be nil")
			}
			if tc.err.Error() == "" {
				t.Fatal("error message must not be empty")
			}
			// Sentinel errors wrap themselves.
			wrapped := errors.New("wrapped: " + tc.err.Error())
			_ = wrapped
		})
	}
}

func TestRegistryDriverNotRegistered(t *testing.T) {
	cfg := config.StoreConfig{Driver: "no-such-driver", DSN: "irrelevant"}
	_, err := store.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for unregistered driver")
	}
	if !errors.Is(err, store.ErrDriverNotRegistered) {
		t.Errorf("expected ErrDriverNotRegistered, got: %v", err)
	}
}

func TestRegistryRegisterAndOpen(t *testing.T) {
	// Register a dummy driver that always returns ErrNotFound on open.
	const driverName = "test-dummy-store"
	store.Register(driverName, func(_ context.Context, _ config.StoreConfig) (store.Store, error) {
		return nil, store.ErrNotFound
	})
	cfg := config.StoreConfig{Driver: driverName, DSN: "irrelevant"}
	_, err := store.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error from dummy factory")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound from dummy, got: %v", err)
	}
}
