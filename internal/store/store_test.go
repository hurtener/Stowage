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

// TestTopicViewValidate covers (TopicView).Validate directly (ae9, D-149/D-151):
// subject_kind enum enforcement, subject_id required, view_name defaulting, and
// the empty-view rejection (ErrEmptyPolicy) — the same footgun guard
// PutAgentPolicy uses, extended to CreateView/UpdateView since the junction
// table cannot represent a view with zero allow/deny keys.
func TestTopicViewValidate(t *testing.T) {
	tests := []struct {
		name     string
		v        store.TopicView
		wantErr  error
		wantView string // expected ViewName after Validate (only checked on success)
	}{
		{
			name:    "invalid subject_kind",
			v:       store.TopicView{SubjectKind: "bogus", SubjectID: "a1", AllowTopics: []string{"x"}},
			wantErr: store.ErrInvalidSubjectKind,
		},
		{
			name:    "empty subject_kind",
			v:       store.TopicView{SubjectID: "a1", AllowTopics: []string{"x"}},
			wantErr: store.ErrInvalidSubjectKind,
		},
		{
			name:    "missing subject_id",
			v:       store.TopicView{SubjectKind: "agent", AllowTopics: []string{"x"}},
			wantErr: store.ErrSubjectIDRequired,
		},
		{
			name:    "empty view (no allow or deny)",
			v:       store.TopicView{SubjectKind: "agent", SubjectID: "a1"},
			wantErr: store.ErrEmptyPolicy,
		},
		{
			name:     "valid, view_name defaults to default",
			v:        store.TopicView{SubjectKind: "agent", SubjectID: "a1", AllowTopics: []string{"x"}},
			wantView: "default",
		},
		{
			name:     "valid key subject, deny-only, explicit view_name preserved",
			v:        store.TopicView{SubjectKind: "key", SubjectID: "sk_1", ViewName: "work", DenyTopics: []string{"secrets"}},
			wantView: "work",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.v
			err := v.Validate()
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Validate() = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if v.ViewName != tc.wantView {
				t.Errorf("ViewName after Validate() = %q, want %q", v.ViewName, tc.wantView)
			}
		})
	}
}
