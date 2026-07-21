package api

import "net/http"

// Ledger descriptor — the memory-ledger self-description seam consumed by the
// Pengui console's memory-ledger admin UI (RFC §9.5; D-157).
//
// The console fetches GET /.well-known/pengui-memory-ledger to learn HOW to
// render and mutate this memory server's ledger — which endpoint lists records,
// which field is the id, what each record field means, and which mutate actions
// exist. Without it the console falls back to a descriptor it hardcodes for
// Stowage (source "builtin:stowage"), which can silently drift from Stowage's
// real API as the API evolves. Serving our OWN descriptor makes Stowage the
// authoritative source of its ledger shape (source "well-known").
//
// The endpoint is PUBLIC (no auth, like /healthz): the descriptor is
// non-sensitive API-shape metadata (paths + field names already in the RFC and
// OpenAPI), and the .well-known convention is public discovery. The console
// probes it both authenticated and (on a mint failure) unauthenticated, so a
// public endpoint guarantees it always resolves the real descriptor. The wire
// shape mirrors the console's ledgerdescriptor.Descriptor (schema version "1");
// it is validated by the console on fetch, so it must stay well-formed — an
// invalid descriptor makes the console report the capability unresolved rather
// than falling back (worse than a 404). ledger_descriptor_golden_test.go pins
// the exact bytes.

// ledgerDescriptorPath is the well-known URL the console fetches. Pengui-branded
// because it is Pengui's contract (D-157); the descriptor FORMAT is generic.
const ledgerDescriptorPath = "/.well-known/pengui-memory-ledger"

// ledgerSchemaVersion is the descriptor schema version this server authors
// against — must match the console's ledgerdescriptor.SchemaVersion.
const ledgerSchemaVersion = "1"

// ledgerDescriptor is the wire contract (mirrors ledgerdescriptor.Descriptor).
type ledgerDescriptor struct {
	Version   string                  `json:"version"`
	Provider  string                  `json:"provider"`
	IDField   string                  `json:"id_field"`
	List      ledgerListOp            `json:"list"`
	Get       *ledgerGetOp            `json:"get,omitempty"`
	Fields    []ledgerField           `json:"fields"`
	MutateOps map[string]ledgerMutate `json:"mutate_ops,omitempty"`
}

type ledgerListOp struct {
	Method          string `json:"method"`
	Path            string `json:"path"`
	ItemsField      string `json:"items_field"`
	NextCursorField string `json:"next_cursor_field,omitempty"`
}

type ledgerGetOp struct {
	Method       string `json:"method"`
	PathTemplate string `json:"path_template"`
}

type ledgerField struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Type      string `json:"type,omitempty"`
	ManagedBy string `json:"managed_by,omitempty"`
	Editable  bool   `json:"editable,omitempty"`
	Primary   bool   `json:"primary,omitempty"`
}

type ledgerMutate struct {
	Method       string         `json:"method"`
	PathTemplate string         `json:"path_template"`
	Body         map[string]any `json:"body,omitempty"`
	Label        string         `json:"label"`
	Destructive  bool           `json:"destructive,omitempty"`
}

// stowageLedgerDescriptor returns Stowage's authoritative ledger descriptor. It
// mirrors the REAL memory API: list = GET /v1/memories ({"memories":[...],
// "next_cursor":"..."} — browse_handler.go), get = GET /v1/memories/{id}, the
// record fields are memoryJSON's keys (memories_handler.go), and the mutate ops
// are the ones the API actually implements — confirm/reject (PATCH with an
// {"action":...} body) and rollback (POST) — never a fixed quartet assumed to
// all exist. Stowage has no DELETE /v1/memories/{id} (verbatim records are never
// deleted outside the retention/DSAR cascade, P1), so no delete op is declared.
func stowageLedgerDescriptor() ledgerDescriptor {
	return ledgerDescriptor{
		Version:  ledgerSchemaVersion,
		Provider: "Stowage",
		IDField:  "id",
		List: ledgerListOp{
			Method:          "GET",
			Path:            "/v1/memories",
			ItemsField:      "memories",
			NextCursorField: "next_cursor",
		},
		Get: &ledgerGetOp{
			Method:       "GET",
			PathTemplate: "/v1/memories/{id}",
		},
		Fields: []ledgerField{
			{Key: "id", Label: "ID", Type: "string"},
			{Key: "kind", Label: "Kind", Type: "enum", ManagedBy: "system"},
			{Key: "content", Label: "Content", Type: "string", ManagedBy: "system", Primary: true},
			{Key: "context", Label: "Context", Type: "string", ManagedBy: "system"},
			{Key: "status", Label: "Status", Type: "enum", ManagedBy: "system", Primary: true},
			{Key: "importance", Label: "Importance", Type: "number"},
			{Key: "confidence", Label: "Confidence", Type: "number"},
			{Key: "trust_source", Label: "Source", Type: "string"},
			{Key: "stability", Label: "Stability", Type: "number"},
			{Key: "valid_from", Label: "Valid from", Type: "timestamp"},
			{Key: "valid_until", Label: "Valid until", Type: "timestamp"},
			{Key: "episode_id", Label: "Episode", Type: "string"},
			{Key: "supersedes_id", Label: "Supersedes", Type: "string"},
			{Key: "superseded_by_id", Label: "Superseded by", Type: "string"},
			{Key: "created_at", Label: "Created", Type: "timestamp", Primary: true},
			{Key: "updated_at", Label: "Updated", Type: "timestamp"},
		},
		MutateOps: map[string]ledgerMutate{
			"confirm": {
				Method: "PATCH", PathTemplate: "/v1/memories/{id}",
				Body: map[string]any{"action": "confirm"}, Label: "Confirm",
			},
			"reject": {
				Method: "PATCH", PathTemplate: "/v1/memories/{id}",
				Body: map[string]any{"action": "reject"}, Label: "Reject", Destructive: true,
			},
			"rollback": {
				Method: "POST", PathTemplate: "/v1/memories/{id}/rollback",
				Label: "Roll back", Destructive: true,
			},
		},
	}
}

// handleLedgerDescriptor serves the ledger descriptor (public, no auth — D-157).
func (s *Server) handleLedgerDescriptor(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, stowageLedgerDescriptor())
}
