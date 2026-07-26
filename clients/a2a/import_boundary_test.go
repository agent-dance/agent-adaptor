package a2a_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestA2AImportsStayLocalized(t *testing.T) {
	t.Parallel()

	repoRoot := repositoryRoot(t)
	allowed := []string{
		filepath.Join("clients", "a2a"),
		filepath.Join("bridges", "a2a"),
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".omx":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(importPath, "github.com/a2aproject/a2a-go") {
				continue
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			if !underAny(rel, allowed) {
				t.Fatalf("A2A SDK import escaped localized packages: %s imports %s", rel, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo imports: %v", err)
	}
}

func TestClientPackageDoesNotImportCoreOrConcreteAdapters(t *testing.T) {
	t.Parallel()

	root := filepath.Join(repositoryRoot(t), "clients", "a2a")
	forbidden := []string{
		"github.com/agent-dance/agent-adaptor",
		"github.com/agent-dance/agent-adaptor/claude",
		"github.com/agent-dance/agent-adaptor/codebuddy",
		"github.com/agent-dance/agent-adaptor/codex",
		"github.com/agent-dance/agent-adaptor/cursor",
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
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
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, forbiddenImport := range forbidden {
				if importPath == forbiddenImport || strings.HasPrefix(importPath, forbiddenImport+"/") {
					t.Fatalf("%s imports forbidden dependency %s", path, forbiddenImport)
				}
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

func underAny(path string, roots []string) bool {
	clean := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
