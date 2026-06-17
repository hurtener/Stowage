package playbook_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPlaybookNoGatewayImport is the CLAUDE.md §6 LLM-free lint for this package
// (AC-1). It parses every non-test .go file in internal/playbook and fails if any
// imports an internal/gateway* package. Playbook assembly is deterministic and
// must never reach the intelligence seam (P5); evolution happens only via delta
// reconciliation of the underlying memories.
func TestPlaybookNoGatewayImport(t *testing.T) {
	t.Parallel()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(filename)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", pkgDir, err)
	}

	const gatewayPrefix = "github.com/hurtener/stowage/internal/gateway"

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path := ""
			if imp.Path != nil && len(imp.Path.Value) >= 2 {
				path = imp.Path.Value[1 : len(imp.Path.Value)-1]
			}
			if strings.HasPrefix(path, gatewayPrefix) {
				t.Errorf("internal/playbook imports forbidden gateway package %q in %s (CLAUDE.md §6 — playbook assembly is LLM-free)", path, name)
			}
		}
	}
}
