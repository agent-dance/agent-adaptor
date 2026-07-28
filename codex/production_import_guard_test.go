package codex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const moduleRootImport = "github.com/agent-dance/agent-adaptor"

const engineImport = moduleRootImport + "/internal/engine"

// TestProviderFilesDoNotDependOnRootAPI keeps Codex and CodeBuddy
// production code pointed at driver/, public leaf packages, and internal
// implementation packages. Integration tests may consume the final root API.
func TestProviderFilesDoNotDependOnRootAPI(t *testing.T) {
	t.Parallel()
	for _, root := range []string{".", filepath.Join("..", "codebuddy")} {
		root := root
		t.Run(filepath.Base(root), func(t *testing.T) {
			t.Parallel()
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
				if err != nil {
					return err
				}
				normalized := filepath.ToSlash(path)
				engineAlias := ""
				for _, imported := range parsed.Imports {
					value, err := strconv.Unquote(imported.Path.Value)
					if err != nil {
						return err
					}
					if value == engineImport {
						engineAlias = "engine"
						if imported.Name != nil {
							engineAlias = imported.Name.Name
						}
					}
					if value != moduleRootImport {
						continue
					}
					t.Errorf("provider file %s imports the consumer-facing module root; use driver, public leaf packages, package Config, or root contracts", normalized)
				}
				guardPrivateConfigReferences(t, normalized, parsed, engineAlias)
				return nil
			})
			if err != nil {
				t.Fatalf("scan %s: %v", root, err)
			}
		})
	}
}

func guardPrivateConfigReferences(t *testing.T, path string, parsed *ast.File, engineAlias string) {
	t.Helper()
	if engineAlias == "" {
		return
	}
	if engineAlias == "." {
		t.Errorf("provider file %s dot-imports internal/engine, preventing private-config dependency auditing", path)
		return
	}
	privateConfig := "CodexConfig"
	if strings.Contains("/"+path, "/codebuddy/") || strings.HasPrefix(path, "../codebuddy/") {
		privateConfig = "CodeBuddyConfig"
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != privateConfig {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if ok && ident.Name == engineAlias {
			t.Errorf("provider file %s references private engine.%s; use the package-owned Config", path, privateConfig)
		}
		return true
	})
}
