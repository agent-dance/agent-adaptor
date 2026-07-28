package claude

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const rootAPIImport = "github.com/agent-dance/agent-adaptor"

// TestPackageRootImportBoundary keeps production code on the Driver SPI and
// leaf contracts. Tests may import the final root API for integration coverage.
func TestPackageRootImportBoundary(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
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
			if path == rootAPIImport {
				t.Errorf("production file %s imports consumer-facing root API %q", name, path)
			}
		}
	}
}
