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

// TestInternalProductionDoesNotImportLegacyRoot protects the dependency
// direction required for the v1 cutover. Internal implementation packages
// must consume driver/public leaf contracts (or engine's own true types), not
// the legacy root aliases that are scheduled for deletion.
func TestInternalProductionDoesNotImportLegacyRoot(t *testing.T) {
	t.Parallel()
	const legacyRoot = "github.com/agent-dance/agent-adaptor"
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
			if importPath == legacyRoot {
				t.Errorf("%s imports legacy root %q; depend on driver/public leaf contracts or engine true types directly", path, legacyRoot)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal production sources: %v", err)
	}
}
