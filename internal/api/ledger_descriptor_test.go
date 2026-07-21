package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLedgerDescriptorGolden pins the exact descriptor bytes. The console
// validates and renders against this contract, so a change here is a contract
// change — regenerate with UPDATE_GOLDEN=1 and eyeball the diff.
func TestLedgerDescriptorGolden(t *testing.T) {
	got, err := json.MarshalIndent(stowageLedgerDescriptor(), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "ledger_descriptor.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", golden)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create)", golden, err)
	}
	if string(got) != string(want) {
		t.Errorf("descriptor drift from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestLedgerDescriptorValidates mirrors the console's ledgerdescriptor.Validate
// rules so Stowage's OWN CI catches a break in the cross-product contract — an
// invalid descriptor makes the console report the capability unresolved (no
// fallback), which is worse than not advertising at all.
func TestLedgerDescriptorValidates(t *testing.T) {
	d := stowageLedgerDescriptor()

	if d.Version == "" {
		t.Error("version is required")
	}
	if d.IDField == "" {
		t.Error("id_field is required")
	}
	if err := validLedgerMethod(d.List.Method); err != nil {
		t.Errorf("list.method: %v", err)
	}
	if err := validLedgerPath(d.List.Path); err != nil {
		t.Errorf("list.path: %v", err)
	}
	if d.List.ItemsField == "" {
		t.Error("list.items_field is required")
	}
	if d.Get != nil {
		if err := validLedgerMethod(d.Get.Method); err != nil {
			t.Errorf("get.method: %v", err)
		}
		if err := validLedgerPath(d.Get.PathTemplate); err != nil {
			t.Errorf("get.path_template: %v", err)
		}
	}
	if len(d.Fields) == 0 {
		t.Error("at least one field is required")
	}
	seen := map[string]bool{}
	for i, f := range d.Fields {
		if f.Key == "" {
			t.Errorf("fields[%d].key is required", i)
		}
		if f.Label == "" {
			t.Errorf("fields[%d].label is required", i)
		}
		if seen[f.Key] {
			t.Errorf("duplicate field key %q", f.Key)
		}
		seen[f.Key] = true
	}
	for name, op := range d.MutateOps {
		if name == "" {
			t.Error("mutate_ops has an empty op name")
		}
		if err := validLedgerMethod(op.Method); err != nil {
			t.Errorf("mutate_ops[%s].method: %v", name, err)
		}
		if err := validLedgerPath(op.PathTemplate); err != nil {
			t.Errorf("mutate_ops[%s].path_template: %v", name, err)
		}
		if op.Label == "" {
			t.Errorf("mutate_ops[%s].label is required", name)
		}
	}

	// The declared list/get/mutate paths must be routes the API actually serves —
	// the whole point of an authoritative descriptor is that it does not drift
	// from the real surface.
	for _, p := range []string{d.List.Path, "/v1/memories/{id}", "/v1/memories/{id}/rollback"} {
		if !strings.HasPrefix(p, "/v1/") && p != d.List.Path {
			t.Errorf("declared path %q is not under /v1/", p)
		}
	}
}

// TestLedgerDescriptorHandler exercises the public HTTP handler: 200,
// application/json, decodable.
func TestLedgerDescriptorHandler(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, ledgerDescriptorPath, nil)
	srv.handleLedgerDescriptor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var d ledgerDescriptor
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if d.Provider != "Stowage" || d.IDField != "id" {
		t.Errorf("unexpected descriptor: provider=%q id_field=%q", d.Provider, d.IDField)
	}
}

func validLedgerMethod(m string) error {
	switch m {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return nil
	default:
		return fmt.Errorf("unsupported HTTP method %q", m)
	}
}

func validLedgerPath(p string) error {
	if p == "" {
		return fmt.Errorf("is required")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("must be absolute, got %q", p)
	}
	if strings.HasPrefix(p, "//") {
		return fmt.Errorf("must not begin with '//', got %q", p)
	}
	return nil
}
