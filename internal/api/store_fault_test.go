package api_test

// store_fault_test.go — fault-injecting store.Store decorators that let HTTP
// tests exercise a handler's store-error (500) branch WITHOUT breaking
// authentication (authMiddleware also calls through s.st.Keys(), so closing
// the whole store — as TestReadyz_StoreUnreachable does for the unauthed
// /readyz probe — would fail auth before the handler is ever reached).
//
// Each decorator embeds a real store.Store/sub-store and overrides exactly
// the one method under test, delegating everything else (including
// auth.Keyring.Lookup) to the real, working implementation.

import (
	"context"
	"errors"
	"time"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// errInjectedFault is the canary error returned by every fault below.
var errInjectedFault = errors.New("injected store fault (test)")

// faultyKeyring wraps a real auth.Keyring, delegating Lookup (so
// authMiddleware keeps working) while optionally failing List/Revoke.
type faultyKeyring struct {
	auth.Keyring
	failList   bool
	failRevoke bool
}

func (k *faultyKeyring) List(tenantID string) ([]auth.Key, error) {
	if k.failList {
		return nil, errInjectedFault
	}
	return k.Keyring.List(tenantID)
}

func (k *faultyKeyring) Revoke(id string, at time.Time) error {
	if k.failRevoke {
		return errInjectedFault
	}
	return k.Keyring.Revoke(id, at)
}

// faultyTopicViewStore wraps a real store.TopicViewStore, optionally failing
// ListViews (used to drive views_handler.go's respondViewError default/500
// branch, which no legitimate validation/conflict error can reach).
type faultyTopicViewStore struct {
	store.TopicViewStore
	failList bool
}

func (v *faultyTopicViewStore) ListViews(ctx context.Context, scope identity.Scope, subjectKind, subjectID string) ([]store.TopicView, error) {
	if v.failList {
		return nil, errInjectedFault
	}
	return v.TopicViewStore.ListViews(ctx, scope, subjectKind, subjectID)
}

// faultyOpsStore wraps a real store.OpsStore, optionally failing
// DeleteUserData (handleDSAR's store-error branch).
type faultyOpsStore struct {
	store.OpsStore
	failDeleteUserData bool
}

func (o *faultyOpsStore) DeleteUserData(ctx context.Context, scope identity.Scope) (store.DSARCounts, error) {
	if o.failDeleteUserData {
		return store.DSARCounts{}, errInjectedFault
	}
	return o.OpsStore.DeleteUserData(ctx, scope)
}

// faultyStore wraps a real store.Store, injecting the faults configured on
// it into the relevant sub-store accessor while delegating every other
// method (including Records/Memories/etc.) to the real store unchanged.
type faultyStore struct {
	store.Store
	failKeysList       bool
	failKeysRevoke     bool
	failListViews      bool
	failDeleteUserData bool
}

func (f *faultyStore) Keys() auth.Keyring {
	return &faultyKeyring{Keyring: f.Store.Keys(), failList: f.failKeysList, failRevoke: f.failKeysRevoke}
}

func (f *faultyStore) TopicViews() store.TopicViewStore {
	return &faultyTopicViewStore{TopicViewStore: f.Store.TopicViews(), failList: f.failListViews}
}

func (f *faultyStore) Ops() store.OpsStore {
	return &faultyOpsStore{OpsStore: f.Store.Ops(), failDeleteUserData: f.failDeleteUserData}
}
