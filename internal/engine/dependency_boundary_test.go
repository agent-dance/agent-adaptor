package engine

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestInternalProductionDoesNotImportRoot protects the required dependency
// direction. Internal packages consume driver/public leaf contracts or their
// own private types; they never depend back on the application-facing root.
func TestInternalProductionDoesNotImportRoot(t *testing.T) {
	t.Parallel()
	const rootImport = "github.com/agent-dance/agent-adaptor"
	internalRoot := filepath.Clean("..")

	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("decode import in %s: %v", path, err)
				continue
			}
			if importPath == rootImport {
				t.Errorf("%s imports root %q; depend on driver/public leaf contracts or private implementation types directly", path, rootImport)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal production sources: %v", err)
	}
}
