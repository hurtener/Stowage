// Package grants implements the team-sharing domain (RFC §5.3, D-016).
//
// A grant gives a named group read or contribute access to a slice of an owner
// scope, capped by a privacy zone ceiling (D-060). Grants are enforced in the
// store layer (P3) and in the retrieval layer as a defense-in-depth predicate
// (AC-1).
//
// Zone ordering (documented once, D-060): public < work < personal < intimate.
// Only public and work are grantable; personal+ are rejected at creation.
//
// Contribute mode (D-059): a contributor writes into the pool owner's scope;
// the pool owner's trust gates govern supersedes; contributor content enters
// as ≤ agent_suggested.
package grants

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ErrCrossTenantGrant is returned when a grant creation would cross tenant
// boundaries. Cross-tenant grants are unconstructible (AC-5 isolation invariant).
var ErrCrossTenantGrant = errors.New("grants: cross-tenant grants are not allowed")

// ErrInvalidZoneCeiling is returned when zone_ceiling is not public or work.
var ErrInvalidZoneCeiling = errors.New("grants: zone_ceiling must be 'public' or 'work'")

// ErrInvalidAccess is returned when access is not read or contribute.
var ErrInvalidAccess = errors.New("grants: access must be 'read' or 'contribute'")

// ErrNotCovered is returned when a contribute request is not covered by a grant.
var ErrNotCovered = errors.New("grants: no active contribute grant covers this scope")

// validCeilings is the set of valid zone_ceiling values (AC-2, D-060).
// personal and intimate are intentionally excluded.
var validCeilings = map[string]bool{
	"public": true,
	"work":   true,
}

// Service provides the grants domain logic. It wraps the GrantStore seam and
// adds validation, contribute-mode checks, and event emission.
// It is safe for concurrent use.
type Service struct {
	st  store.GrantStore
	ev  store.EventStore
	log *slog.Logger
}

// New creates a Service backed by the given grant store and event store.
// ev may be nil (event emission disabled; used in tests).
func New(st store.GrantStore, ev store.EventStore, log *slog.Logger) *Service {
	return &Service{
		st:  st,
		ev:  ev,
		log: log.With("subsystem", "grants"),
	}
}

// --- Group management ---

// CreateGroup creates a new group within the tenant.
func (s *Service) CreateGroup(ctx context.Context, scope identity.Scope, name string) (*store.Group, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	g := store.Group{
		ID:        ulid.Make().String(),
		TenantID:  scope.Tenant,
		Name:      name,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.st.CreateGroup(ctx, scope, g); err != nil {
		return nil, fmt.Errorf("grants: create group: %w", err)
	}
	s.emitEvent(ctx, scope, "group.created", g.ID, fmt.Sprintf(`{"group_id":%q,"name":%q}`, g.ID, name))
	return &g, nil
}

// GetGroup returns a group by ID.
func (s *Service) GetGroup(ctx context.Context, scope identity.Scope, id string) (*store.Group, error) {
	return s.st.GetGroup(ctx, scope, id)
}

// ListGroups returns all groups in the tenant.
func (s *Service) ListGroups(ctx context.Context, scope identity.Scope) ([]store.Group, error) {
	return s.st.ListGroups(ctx, scope)
}

// DeleteGroup removes a group and its membership rows.
func (s *Service) DeleteGroup(ctx context.Context, scope identity.Scope, id string) error {
	if err := s.st.DeleteGroup(ctx, scope, id); err != nil {
		return fmt.Errorf("grants: delete group: %w", err)
	}
	s.emitEvent(ctx, scope, "group.deleted", id, fmt.Sprintf(`{"group_id":%q}`, id))
	return nil
}

// AddMember adds a user to a group. Idempotent.
func (s *Service) AddMember(ctx context.Context, scope identity.Scope, groupID, userID string) (*store.GroupMember, error) {
	m := store.GroupMember{
		ID:        ulid.Make().String(),
		GroupID:   groupID,
		UserID:    userID,
		TenantID:  scope.Tenant,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.st.AddMember(ctx, scope, m); err != nil {
		return nil, fmt.Errorf("grants: add member: %w", err)
	}
	s.emitEvent(ctx, scope, "group.member.added", groupID,
		fmt.Sprintf(`{"group_id":%q,"user_id":%q}`, groupID, userID))
	return &m, nil
}

// RemoveMember removes a user from a group.
func (s *Service) RemoveMember(ctx context.Context, scope identity.Scope, groupID, userID string) error {
	if err := s.st.RemoveMember(ctx, scope, groupID, userID); err != nil {
		return fmt.Errorf("grants: remove member: %w", err)
	}
	s.emitEvent(ctx, scope, "group.member.removed", groupID,
		fmt.Sprintf(`{"group_id":%q,"user_id":%q}`, groupID, userID))
	return nil
}

// ListMembers returns all members of a group.
func (s *Service) ListMembers(ctx context.Context, scope identity.Scope, groupID string) ([]store.GroupMember, error) {
	return s.st.ListMembers(ctx, scope, groupID)
}

// --- Grant management ---

// CreateGrantInput carries the validated inputs for grant creation.
type CreateGrantInput struct {
	// OwnerScope is the scope whose memories are being shared.
	// OwnerScope.Tenant must match the caller's tenant (cross-tenant = error).
	OwnerScope       identity.Scope
	GroupID          string
	Access           string // "read" | "contribute"
	TopicFilter      string
	KindFilter       string
	ZoneCeiling      string // "public" | "work"
	RedactionProfile string
}

// CreateGrant creates a grant after validation.
// Validated invariants:
//   - OwnerScope.Tenant must equal callerScope.Tenant (cross-tenant rejected).
//   - ZoneCeiling must be "public" or "work" (personal+ rejected, AC-2).
//   - Access must be "read" or "contribute".
func (s *Service) CreateGrant(ctx context.Context, callerScope identity.Scope, in CreateGrantInput) (*store.Grant, error) {
	// Cross-tenant rejection (AC-2 — unconstructible).
	if in.OwnerScope.Tenant != callerScope.Tenant {
		return nil, ErrCrossTenantGrant
	}
	// Zone ceiling validation (AC-2, D-060).
	if !validCeilings[in.ZoneCeiling] {
		return nil, ErrInvalidZoneCeiling
	}
	// Access validation.
	if in.Access != "read" && in.Access != "contribute" {
		return nil, ErrInvalidAccess
	}

	now := time.Now().UnixMilli()
	g := store.Grant{
		ID:               ulid.Make().String(),
		TenantID:         in.OwnerScope.Tenant,
		ProjectID:        in.OwnerScope.Project,
		UserID:           in.OwnerScope.User,
		SessionID:        in.OwnerScope.Session,
		GroupID:          in.GroupID,
		Access:           in.Access,
		TopicFilter:      in.TopicFilter,
		KindFilter:       in.KindFilter,
		ZoneCeiling:      in.ZoneCeiling,
		RedactionProfile: in.RedactionProfile,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	// Use the owner scope for the store call (grants live under the owner's tenant).
	ownerTenantScope := identity.Scope{Tenant: in.OwnerScope.Tenant}
	if err := s.st.CreateGrant(ctx, ownerTenantScope, g); err != nil {
		return nil, fmt.Errorf("grants: create grant: %w", err)
	}
	s.emitEvent(ctx, callerScope, "grant.created", g.ID,
		fmt.Sprintf(`{"grant_id":%q,"group_id":%q,"access":%q,"zone_ceiling":%q}`,
			g.ID, g.GroupID, g.Access, g.ZoneCeiling))
	return &g, nil
}

// GetGrant returns a grant by ID within the tenant.
func (s *Service) GetGrant(ctx context.Context, scope identity.Scope, id string) (*store.Grant, error) {
	return s.st.GetGrant(ctx, scope, id)
}

// ListGrants returns all grants for the tenant.
func (s *Service) ListGrants(ctx context.Context, scope identity.Scope) ([]store.Grant, error) {
	return s.st.ListGrants(ctx, scope)
}

// RevokeGrant revokes a grant; effective immediately on next EffectiveScopes call (D-060).
func (s *Service) RevokeGrant(ctx context.Context, scope identity.Scope, id string) error {
	now := time.Now().UnixMilli()
	if err := s.st.RevokeGrant(ctx, scope, id, now); err != nil {
		return fmt.Errorf("grants: revoke grant: %w", err)
	}
	s.emitEvent(ctx, scope, "grant.revoked", id, fmt.Sprintf(`{"grant_id":%q}`, id))
	return nil
}

// --- Effective scopes resolution ---

// EffectiveScopes resolves all scopes the caller may read, including granted
// scopes. The first element is always the caller's own scope (ZoneCeiling="").
// Subsequent elements are granted scopes with zone ceilings ≤ work.
// This issues at most one SQL query (≤1 extra query per retrieve, D-060).
func (s *Service) EffectiveScopes(ctx context.Context, callerScope identity.Scope) ([]store.ScopedQuery, error) {
	return s.st.EffectiveScopes(ctx, callerScope)
}

// --- Contribute-mode ---

// CheckContributeGrant checks whether the caller has an active contribute grant
// covering targetScope. Returns nil if covered, ErrNotCovered otherwise.
// Also checks same-tenant (cross-tenant contribute is not allowed).
func (s *Service) CheckContributeGrant(ctx context.Context, callerScope identity.Scope, targetScope identity.Scope, callerUserID string) error {
	if callerScope.Tenant != targetScope.Tenant {
		return ErrCrossTenantGrant
	}
	if callerUserID == "" {
		return ErrNotCovered
	}

	// Resolve the caller's effective scopes; look for a contribute grant matching targetScope.
	scopes, err := s.st.EffectiveScopes(ctx, identity.Scope{
		Tenant: callerScope.Tenant,
		User:   callerUserID,
	})
	if err != nil {
		return fmt.Errorf("grants: check contribute: %w", err)
	}

	// Check if any granted scope matches the target (with contribute access).
	// We need to also verify the access type, so we query grants directly.
	grants, err := s.st.ListGrants(ctx, identity.Scope{Tenant: callerScope.Tenant})
	if err != nil {
		return fmt.Errorf("grants: check contribute list: %w", err)
	}

	// Build a set of group IDs the caller belongs to.
	members, err := s.buildMemberGroupSet(ctx, callerScope.Tenant, callerUserID)
	if err != nil {
		return fmt.Errorf("grants: check contribute members: %w", err)
	}

	for _, gr := range grants {
		if gr.RevokedAt != 0 {
			continue
		}
		if gr.Access != "contribute" {
			continue
		}
		if !members[gr.GroupID] {
			continue
		}
		// Check if this grant covers the target scope.
		if grantCoversScope(gr, targetScope) {
			return nil
		}
	}

	// Suppress unused variable warning for scopes (used for logging/tracing if needed).
	_ = scopes

	return ErrNotCovered
}

// buildMemberGroupSet returns the set of group IDs the user belongs to in the tenant.
func (s *Service) buildMemberGroupSet(ctx context.Context, tenantID, userID string) (map[string]bool, error) {
	groups, err := s.st.ListGroups(ctx, identity.Scope{Tenant: tenantID})
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool)
	scope := identity.Scope{Tenant: tenantID}
	for _, g := range groups {
		members, err := s.st.ListMembers(ctx, scope, g.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.UserID == userID {
				result[g.ID] = true
				break
			}
		}
	}
	return result, nil
}

// grantCoversScope checks if a grant covers the given target scope.
// The grant's (project_id, user_id, session_id) must match the target scope's
// corresponding fields (empty grant fields match any value).
func grantCoversScope(gr store.Grant, target identity.Scope) bool {
	if gr.TenantID != target.Tenant {
		return false
	}
	if gr.ProjectID != "" && gr.ProjectID != target.Project {
		return false
	}
	if gr.UserID != "" && gr.UserID != target.User {
		return false
	}
	if gr.SessionID != "" && gr.SessionID != target.Session {
		return false
	}
	return true
}

// --- Zone ceiling enforcement (defense-in-depth, AC-1) ---

// ApplyCeiling filters memories to only those at or below the zone ceiling.
// Personal and intimate are NEVER returned for granted reads, even if ceiling
// is mis-stored or bypassed (defense-in-depth read predicate, AC-1).
// Returns memories whose privacy_zone is within the allowed set.
func ApplyCeiling(mems []store.Memory, ceiling string) []store.Memory {
	if ceiling == "" {
		return mems // no ceiling = caller's own scope; no filtering
	}
	ceilOrd, ok := store.ZoneOrder[ceiling]
	if !ok {
		// Unknown ceiling: defense-in-depth — return nothing.
		return nil
	}
	// Hard cap at work (AC-1): personal and intimate NEVER cross grants.
	if ceilOrd > store.ZoneOrder["work"] {
		ceilOrd = store.ZoneOrder["work"]
	}
	out := mems[:0:0]
	for _, m := range mems {
		zoneOrd, exists := store.ZoneOrder[m.PrivacyZone]
		if !exists {
			continue // unknown zone — skip (defense-in-depth)
		}
		if zoneOrd <= ceilOrd {
			out = append(out, m)
		}
	}
	return out
}

// --- Internal helpers ---

func (s *Service) emitEvent(ctx context.Context, scope identity.Scope, eventType, subjectID, payload string) {
	if s.ev == nil {
		return
	}
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      eventType,
		SubjectID: subjectID,
		Payload:   payload,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.ev.Emit(ctx, scope, ev); err != nil {
		s.log.WarnContext(ctx, "grants: emit event failed",
			slog.String("type", eventType), slog.Any("err", err))
	}
}
