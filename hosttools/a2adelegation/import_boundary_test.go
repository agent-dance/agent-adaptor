package a2adelegation_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDelegationPackagesDoNotImportConcreteAdapters(t *testing.T) {
	t.Parallel()

	repoRoot := repositoryRoot(t)
	packages := []string{
		filepath.Join(repoRoot, "hosttools", "a2adelegation"),
		filepath.Join(repoRoot, "bridges", "subagentstream"),
	}
	forbidden := []string{
		"github.com/agent-dance/agent-adaptor/claude",
		"github.com/agent-dance/agent-adaptor/codebuddy",
		"github.com/agent-dance/agent-adaptor/codex",
		"github.com/agent-dance/agent-adaptor/cursor",
	}
	forEachProductionImport(t, packages, func(path, importPath string) {
		for _, forbiddenImport := range forbidden {
			if importPath == forbiddenImport || strings.HasPrefix(importPath, forbiddenImport+"/") {
				t.Fatalf("%s imports forbidden concrete adapter %s", path, forbiddenImport)
			}
		}
	})
}

func TestHosttoolsDoNotImportLegacyRootOrInternalPackages(t *testing.T) {
	t.Parallel()

	repoRoot := repositoryRoot(t)
	packages := []string{
		filepath.Join(repoRoot, "hosttools", "a2adelegation"),
		filepath.Join(repoRoot, "hosttools", "sessionrecorder"),
	}
	const legacyRoot = "github.com/agent-dance/agent-adaptor"
	forEachProductionImport(t, packages, func(path, importPath string) {
		if importPath == legacyRoot {
			t.Fatalf("%s imports the deleted legacy root API", path)
		}
		if strings.Contains(importPath, "/internal/") {
			t.Fatalf("%s crosses an internal package boundary via %s", path, importPath)
		}
	})
}

func forEachProductionImport(t *testing.T, roots []string, check func(path, importPath string)) {
	t.Helper()
	fset := token.NewFileSet()
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("readdir %s: %v", root, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, entry.Name())
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, imp := range file.Imports {
				check(path, strings.Trim(imp.Path.Value, `"`))
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
