package claude

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const legacyRootImport = "github.com/agent-dance/agent-adaptor"

// TestPackageRootImportBoundary keeps both production and test code on the
// final v1 leaf contracts. Reintroducing the historical module root anywhere
// in this package is a compile-boundary regression.
func TestPackageRootImportBoundary(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", name, err)
				continue
			}
			if path == legacyRootImport {
				t.Errorf("file %s imports legacy root %q", name, path)
			}
		}
	}
}
