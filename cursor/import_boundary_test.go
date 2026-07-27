package cursor

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const rootImportPath = "github.com/agent-dance/agent-adaptor"

// TestPackageFilesDoNotImportLegacyRoot protects production and test code
// from drifting back through historical module-root aliases.
func TestPackageFilesDoNotImportLegacyRoot(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
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
			if path == rootImportPath {
				t.Errorf("file %s imports legacy root %q", name, path)
			}
		}
	}
}
