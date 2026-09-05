package harbor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	. "github.com/hurtener/stowage/adapters/harbor"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// Complete the legacy fake's Client contract; unexpected explicit calls fail.
func (f *fakeClient) Remember(context.Context, stowage.RememberRequest) (stowage.MemoryReceipt, error) {
	return stowage.MemoryReceipt{}, fmt.Errorf("unexpected explicit remember")
}
func (f *fakeClient) Correct(context.Context, stowage.CorrectRequest) (stowage.MemoryReceipt, error) {
	return stowage.MemoryReceipt{}, fmt.Errorf("unexpected explicit correction")
}

type explicitFake struct {
	fakeClient
	remembered stowage.RememberRequest
	corrected  stowage.CorrectRequest
}

func (f *explicitFake) Remember(_ context.Context, r stowage.RememberRequest) (stowage.MemoryReceipt, error) {
	f.remembered = r
	return stowage.MemoryReceipt{MemoryID: "m", Outcome: "saved"}, nil
}
func (f *explicitFake) Correct(_ context.Context, r stowage.CorrectRequest) (stowage.MemoryReceipt, error) {
	f.corrected = r
	return stowage.MemoryReceipt{MemoryID: "new", Outcome: "corrected"}, nil
}

func TestAgentToolsSmallCatalogAndBoundSource(t *testing.T) {
	client := &explicitFake{}
	descs := Tools(client)
	want := map[string]bool{"stowage_retrieve": true, "stowage_inspect": true, "stowage_remember": true, "stowage_correct": true, "stowage_playbook": true}
	if len(descs) != 5 {
		t.Fatalf("unexpected tool count: %d", len(descs))
	}
	for _, d := range descs {
		if !want[d.Tool.Name] {
			t.Fatalf("runtime/curator tool leaked: %s", d.Tool.Name)
		}
		delete(want, d.Tool.Name)
		switch d.Tool.Name {
		case "stowage_remember":
			if _, err := d.Invoke(context.Background(), json.RawMessage(`{"quote":"Use Go."}`)); err == nil || !strings.Contains(err.Error(), "source_required") {
				t.Fatalf("missing source accepted: %v", err)
			}
			ctx := WithMemorySource(context.Background(), "source", "command-1")
			if _, err := d.Invoke(ctx, json.RawMessage(`{"quote":"Use Go.","kind":"decision"}`)); err != nil {
				t.Fatal(err)
			}
			if client.remembered.SourceRecordID != "source" || client.remembered.IdempotencyKey != "command-1" {
				t.Fatal("host binding lost")
			}
			if _, err := d.Invoke(ctx, json.RawMessage(`{"quote":"Use Go.","source_record_id":"other"}`)); err == nil {
				t.Fatal("conflicting source accepted")
			}
		case "stowage_correct":
			ctx := WithMemorySource(context.Background(), "new-source", "command-2")
			if _, err := d.Invoke(ctx, json.RawMessage(`{"memory_id":"old","expected_revision":"revision","quote":"Use Go."}`)); err != nil {
				t.Fatal(err)
			}
			if client.corrected.SourceRecordID != "new-source" || client.corrected.MemoryID != "old" {
				t.Fatal("correction not forwarded")
			}
		case "stowage_retrieve":
			value, err := d.Invoke(context.Background(), json.RawMessage(`{"query":"previous decisions"}`))
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "fake-resp") || !strings.Contains(string(b), "Prior-context") {
				t.Fatalf("fallback lost useful result: %s", b)
			}
		}
	}
	if len(client.ingests) != 0 {
		t.Fatal("agent fabricated ingestion records")
	}
}
