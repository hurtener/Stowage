package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// ErrCommandReplay means this scoped receipt already committed. Read and compare
// its request digest before replaying; uniqueness alone is not proof of equality.
var ErrCommandReplay = errors.New("command already committed")

// ErrCommandConflict means evidence disappeared or a target changed. The entire
// command, including the reserved receipt, is rolled back.
var ErrCommandConflict = errors.New("command precondition changed")

// CommandGuard reuses the event table for durable idempotency. The receipt,
// source checks, optimistic target checks, and CommitSet effects share one DB
// transaction. All snapshots are resolved by the service, never by the model.
type CommandGuard struct {
	Receipt Event
	Source  Record
	Targets []Memory
}

// MemoryRevision includes semantic state but excludes observation counters.
// Unlike a timestamp alone, it detects edits within the same millisecond.
func MemoryRevision(m Memory) string {
	v := struct {
		ID, Tenant, Project, User, Session, Kind, Content, Context, Status string
		Trust, Hash, Supersedes, SupersededBy, Privacy, Episode            string
		ValidFrom, ValidUntil, CreatedAt, UpdatedAt                        int64
	}{m.ID, m.TenantID, m.ProjectID, m.UserID, m.SessionID, m.Kind,
		m.Content, m.Context, m.Status, m.TrustSource, m.ContentHash,
		m.SupersedesID, m.SupersededByID, m.PrivacyZone, m.EpisodeID,
		m.ValidFrom, m.ValidUntil, m.CreatedAt, m.UpdatedAt}
	b, _ := json.Marshal(v) // only strings and integers: cannot fail
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
