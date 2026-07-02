package mcpserver_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hurtener/dockyard/runtime/tool"

	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/traces"
)

// buildSchemas extracts the JSON Schema bytes for the named tool.
// It returns (inputSchemaJSON, outputSchemaJSON, error).
func buildSchemas(name string) (inJSON, outJSON []byte, err error) {
	type inOutSchema interface {
		MarshalJSON() ([]byte, error)
	}
	marshal := func(s inOutSchema) ([]byte, error) {
		return json.MarshalIndent(s, "", "  ")
	}

	switch name {
	case "memory_ingest":
		b := tool.New[mcpserver.IngestInput, mcpserver.IngestOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_retrieve":
		b := tool.New[mcpserver.RetrieveInput, mcpserver.RetrieveOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_playbook":
		b := tool.New[mcpserver.PlaybookInput, mcpserver.PlaybookOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_episodes":
		b := tool.New[mcpserver.EpisodesInput, mcpserver.EpisodesOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_browse":
		b := tool.New[mcpserver.BrowseInput, mcpserver.BrowseOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_causal":
		b := tool.New[mcpserver.CausalInput, mcpserver.CausalOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_verify":
		b := tool.New[mcpserver.VerifyInput, mcpserver.VerifyOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_review":
		b := tool.New[mcpserver.ReviewInput, mcpserver.ReviewOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_trace":
		b := tool.New[mcpserver.TraceInput, traces.Bundle](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_drilldown":
		b := tool.New[mcpserver.DrilldownInput, mcpserver.DrilldownOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_feedback":
		b := tool.New[mcpserver.FeedbackInput, mcpserver.FeedbackOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_assert":
		b := tool.New[mcpserver.AssertInput, mcpserver.AssertOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_topics":
		b := tool.New[mcpserver.TopicsInput, mcpserver.TopicsOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_get":
		b := tool.New[mcpserver.GetInput, mcpserver.GetOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_rollback":
		b := tool.New[mcpserver.RollbackInput, mcpserver.RollbackOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_resolve":
		b := tool.New[mcpserver.ResolveInput, mcpserver.ResolveOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_flush":
		b := tool.New[mcpserver.FlushInput, mcpserver.FlushOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_branch":
		b := tool.New[mcpserver.BranchInput, mcpserver.BranchOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_grants":
		b := tool.New[mcpserver.GrantsInput, mcpserver.GrantsOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_agent_policy":
		b := tool.New[mcpserver.AgentPolicyInput, mcpserver.AgentPolicyOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_suggestions":
		b := tool.New[mcpserver.SuggestionsInput, mcpserver.SuggestionsOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_views":
		b := tool.New[mcpserver.ViewsInput, mcpserver.ViewsOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)

	case "memory_proactive_config":
		b := tool.New[mcpserver.ProactiveConfigInput, mcpserver.ProactiveConfigOutput](name)
		in, out, e := b.Schemas()
		if e != nil {
			return nil, nil, e
		}
		if inJSON, err = marshal(in); err != nil {
			return nil, nil, err
		}
		outJSON, err = marshal(out)
	}

	return inJSON, outJSON, err
}

// TestSchemaGoldens generates JSON Schema for every tool's input and output and
// compares against the goldens in testdata/. Set UPDATE_GOLDEN=1 to regenerate.
// This is the D-061 contract gate: any rename of a contract type field causes
// the golden to drift and the test fails (AC-6).
func TestSchemaGoldens(t *testing.T) {
	update := os.Getenv("UPDATE_GOLDEN") == "1"

	tools := []string{
		"memory_ingest",
		"memory_retrieve",
		"memory_playbook",
		"memory_episodes",
		"memory_browse",
		"memory_causal",
		"memory_verify",
		"memory_review",
		"memory_trace",
		"memory_drilldown",
		"memory_feedback",
		"memory_assert",
		"memory_topics",
		"memory_get",
		"memory_rollback",
		"memory_resolve",
		"memory_flush",
		"memory_branch",
		"memory_grants",
		"memory_agent_policy",
		"memory_views",
		"memory_suggestions",
		"memory_proactive_config",
	}

	for _, name := range tools {
		name := name // capture
		t.Run(name, func(t *testing.T) {
			inJSON, outJSON, err := buildSchemas(name)
			if err != nil {
				t.Fatalf("buildSchemas(%q): %v", name, err)
			}

			inPath := filepath.Join("testdata", name+".input.schema.json")
			outPath := filepath.Join("testdata", name+".output.schema.json")

			if update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(inPath, append(inJSON, '\n'), 0o644); err != nil {
					t.Fatalf("write %s: %v", inPath, err)
				}
				if err := os.WriteFile(outPath, append(outJSON, '\n'), 0o644); err != nil {
					t.Fatalf("write %s: %v", outPath, err)
				}
				t.Logf("updated golden: %s, %s", inPath, outPath)
				return
			}

			// Compare to existing goldens.
			checkGolden(t, inPath, inJSON)
			checkGolden(t, outPath, outJSON)
		})
	}
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("golden file %s missing — run UPDATE_GOLDEN=1 go test ./internal/mcpserver/ -run TestSchemaGoldens to generate", path)
	}
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}

	// Normalize: both should be valid JSON; compare canonical forms.
	gotNorm, err := normalizeJSON(got)
	if err != nil {
		t.Fatalf("normalize generated JSON for %s: %v", path, err)
	}
	wantNorm, err := normalizeJSON(want)
	if err != nil {
		t.Fatalf("normalize golden JSON for %s: %v", path, err)
	}

	if gotNorm != wantNorm {
		t.Errorf("schema drift detected for %s\n--- golden ---\n%s\n--- got ---\n%s", path, wantNorm, gotNorm)
	}
}

func normalizeJSON(b []byte) (string, error) {
	// Trim trailing newline for comparison.
	for len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	normalized, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}
