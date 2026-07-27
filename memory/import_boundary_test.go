package memory_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

const legacyRootImport = "github.com/agent-dance/agent-adaptor"

// TestProductionImportBoundary prevents the v1 memory implementation from
// regaining a dependency on the deleted root-package SessionStore API.
func TestProductionImportBoundary(t *testing.T) {
	var _ threadstore.Store = memory.NewStore()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	packageDir := filepath.Dir(currentFile)
	packages, err := parser.ParseDir(token.NewFileSet(), packageDir, func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse memory package: %v", err)
	}
	for _, pkg := range packages {
		for filename, file := range pkg.Files {
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("decode import in %s: %v", filename, err)
				}
				if path == legacyRootImport {
					t.Fatalf("production file %s imports deleted legacy root package %q", filepath.Base(filename), path)
				}
			}
		}
	}
}
