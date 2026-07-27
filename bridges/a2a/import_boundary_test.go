package a2a_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBridgeProductionImportBoundary is the architecture guard for every
// bridge package. Bridges may consume the final root API, but must not depend
// on the A2A client or concrete provider implementations.
func TestBridgeProductionImportBoundary(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	bridgesRoot := filepath.Dir(filepath.Dir(thisFile))
	forbiddenExact := map[string]struct{}{
		"github.com/agent-dance/agent-adaptor/clients/a2a": {},
	}
	forbiddenPrefixes := []string{
		"github.com/agent-dance/agent-adaptor/claude",
		"github.com/agent-dance/agent-adaptor/codebuddy",
		"github.com/agent-dance/agent-adaptor/codex",
		"github.com/agent-dance/agent-adaptor/cursor",
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(bridgesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			pathValue := strings.Trim(imported.Path.Value, `"`)
			if _, forbidden := forbiddenExact[pathValue]; forbidden {
				t.Errorf("%s imports forbidden dependency %s", path, pathValue)
			}
			for _, prefix := range forbiddenPrefixes {
				if pathValue == prefix || strings.HasPrefix(pathValue, prefix+"/") {
					t.Errorf("%s imports forbidden provider dependency %s", path, pathValue)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk bridge production files: %v", err)
	}
}
