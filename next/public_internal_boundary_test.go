package adaptor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestPublicDeclarationsDoNotExposeInternalSelectors is a cutover guard. The
// root implementation may call internal/engine freely in private code, but no
// exported type, field, function signature, alias, variable, or constant may
// make an internal package selector observable to consumers.
func TestPublicDeclarationsDoNotExposeInternalSelectors(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate next package")
	}
	dir := filepath.Dir(current)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		internalAliases := map[string]struct{}{}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || !strings.Contains(importPath, "/internal/") {
				continue
			}
			name := filepath.Base(importPath)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			internalAliases[name] = struct{}{}
		}
		if len(internalAliases) == 0 {
			continue
		}

		inspect := func(label string, node ast.Node) {
			ast.Inspect(node, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if _, internal := internalAliases[ident.Name]; internal {
					t.Errorf("%s exposes internal selector %s.%s at %s", label, ident.Name, sel.Sel.Name, fset.Position(sel.Pos()))
				}
				return true
			})
		}

		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Name.IsExported() && exportedReceiver(decl.Recv) {
					inspect("exported function "+decl.Name.Name, decl.Type)
				}
			case *ast.GenDecl:
				for _, rawSpec := range decl.Specs {
					switch spec := rawSpec.(type) {
					case *ast.TypeSpec:
						if spec.Name.IsExported() {
							inspect("exported type "+spec.Name.Name, spec)
						}
					case *ast.ValueSpec:
						exported := false
						for _, name := range spec.Names {
							exported = exported || name.IsExported()
						}
						if exported {
							inspect("exported value declaration", spec)
						}
					}
				}
			}
		}
	}
}

func exportedReceiver(recv *ast.FieldList) bool {
	if recv == nil {
		return true
	}
	if len(recv.List) != 1 {
		return false
	}
	typ := recv.List[0].Type
	if pointer, ok := typ.(*ast.StarExpr); ok {
		typ = pointer.X
	}
	ident, ok := typ.(*ast.Ident)
	return ok && ident.IsExported()
}
