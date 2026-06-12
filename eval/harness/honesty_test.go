package harness_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHarnessHonesty verifies AC-7: the eval/ scoring and fixture code does not
// import internal/pipeline for extraction-prompt construction purposes.
//
// The harness must rely on the product's default profile and pack selection —
// no per-dataset topic or prompt overrides are allowed. Concretely, the eval/
// runner, fixture, scores, gate files, and the dataset normalizers must not
// import github.com/hurtener/stowage/internal/pipeline.
//
// Exemption: server.go is the boot-infrastructure file that wires the full
// pipeline stages (Stage, ExtractStage, etc.). It legitimately imports the
// pipeline package. The honesty constraint applies to eval SCORING code only.
func TestHarnessHonesty(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// eval/ root is two levels up from eval/harness/
	evalRoot := filepath.Dir(filepath.Dir(filename))

	forbidden := []string{
		"github.com/hurtener/stowage/internal/pipeline",
	}

	// exemptSuffixes lists files that are boot infrastructure, not scoring logic.
	// These are allowed to import the pipeline package for stage wiring.
	exemptSuffixes := []string{
		"harness/server.go",
	}

	var violations []string
	err := filepath.Walk(evalRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// Skip this file itself.
		if strings.HasSuffix(path, "honesty_test.go") {
			return nil
		}
		// Skip boot-infrastructure files (stage wiring, not scoring logic).
		for _, exempt := range exemptSuffixes {
			if strings.HasSuffix(filepath.ToSlash(path), exempt) {
				return nil
			}
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return nil // skip unparseable files gracefully
		}
		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if importPath == bad || strings.HasPrefix(importPath, bad+"/") {
					violations = append(violations, path+": imports "+importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk eval/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("honesty violation: eval/ scoring code imports extraction-prompt code:\n  %s\n"+
			"The harness must not override extraction topics or prompts per-dataset (AC-7).\n"+
			"If this is boot-infrastructure code, add its suffix to exemptSuffixes in this test.",
			strings.Join(violations, "\n  "))
	}
}
