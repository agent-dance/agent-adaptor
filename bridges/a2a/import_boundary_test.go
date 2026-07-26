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

func TestBridgePackageDoesNotImportConcreteAdaptersOrA2AClient(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(thisFile)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	forbidden := []string{
		"github.com/agent-dance/agent-adaptor/claude",
		"github.com/agent-dance/agent-adaptor/codebuddy",
		"github.com/agent-dance/agent-adaptor/codex",
		"github.com/agent-dance/agent-adaptor/cursor",
		"github.com/agent-dance/agent-adaptor/clients/a2a",
	}
	fset := token.NewFileSet()
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
			pathValue := strings.Trim(imp.Path.Value, `"`)
			for _, forbiddenImport := range forbidden {
				if pathValue == forbiddenImport {
					t.Fatalf("%s imports forbidden dependency %s", path, forbiddenImport)
				}
			}
		}
	}
}
