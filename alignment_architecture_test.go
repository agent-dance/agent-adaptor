package adaptor_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const alignmentModule = "github.com/agent-dance/agent-adaptor"

func alignmentForbiddenImport(dir, imported string) bool {
	local := imported == alignmentModule || strings.HasPrefix(imported, alignmentModule+"/")
	if !local {
		return false
	}
	target := strings.TrimPrefix(strings.TrimPrefix(imported, alignmentModule), "/")
	provider := func(s string) bool {
		for _, name := range []string{"codex", "claude", "codebuddy", "cursor"} {
			if s == name || strings.HasPrefix(s, name+"/") {
				return true
			}
		}
		return false
	}
	if dir == "driver" {
		return target == "" || strings.HasPrefix(target, "internal/") || provider(target) || strings.HasPrefix(target, "bridges/") || strings.HasPrefix(target, "hosttools/")
	}
	if dir == "internal/engine" {
		return target == ""
	}
	switch dir {
	case "tool", "skill", "mcp", "profile", "threadstore":
		return target == ""
	}
	if dir == "capability" || dir == "todo" || dir == "internal/activebudget" {
		return true
	}
	if dir == "internal/capabilityobs" || dir == "internal/todoobs" {
		return target != "capability" && target != "todo"
	}
	if strings.HasPrefix(dir, "bridges/") || strings.HasPrefix(dir, "hosttools/") {
		if dir == "hosttools/a2adelegation" && target == "internal/activebudget" {
			return false
		}
		return strings.HasPrefix(target, "internal/") || provider(target)
	}
	return false
}

// Parse every platform's production files, so the import boundary also guards
// files that the current operating system would exclude from go list.
func TestAlignmentArchitectureDependencies(t *testing.T) {
	count := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "testdata" || entry.Name() == "vendor" || entry.Name() == "docs" || entry.Name() == "examples" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		count++
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, imp := range file.Imports {
			imported, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if alignmentForbiddenImport(dir, imported) {
				t.Errorf("%s imports %s across the frozen architecture boundary", path, imported)
			}
		}
		if strings.HasPrefix(dir, "bridges/") || strings.HasPrefix(dir, "hosttools/") {
			aliases := map[string]bool{}
			for _, imp := range file.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == alignmentModule+"/driver" {
					name := "driver"
					if imp.Name != nil {
						name = imp.Name.Name
					}
					aliases[name] = true
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if name, ok := sel.X.(*ast.Ident); ok && aliases[name.Name] && (sel.Sel.Name == "Driver" || sel.Sel.Name == "Request" || sel.Sel.Name == "SessionContext") {
						t.Errorf("%s uses execution SPI %s.%s instead of public Runner", path, name.Name, sel.Sel.Name)
					}
				}
				return true
			})
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no production files inspected")
	}
}

func TestAlignmentArchitectureBoundaryOracle(t *testing.T) {
	for _, tc := range []struct {
		dir, target string
		forbidden   bool
	}{
		{"hosttools/a2adelegation", "internal/activebudget", false},
		{"hosttools/a2adelegation", "internal/engine", true},
		{"hosttools/a2adelegation/extra", "internal/activebudget", true},
		{"hosttools/capabilityrecorder", "internal/activebudget", true},
		{"bridges/a2a", "driver", false}, {"bridges/sse", "claude", true},
		{"driver", "codex/appserver", true}, {"driver", "", true},
		{"internal/engine", "", true}, {"internal/capabilityobs", "driver", true},
		{"internal/capabilityobs", "capability", false}, {"capability", "driver", true},
		{"profile", "", true}, {".", "internal/engine", false},
	} {
		imported := alignmentModule
		if tc.target != "" {
			imported += "/" + tc.target
		}
		if got := alignmentForbiddenImport(tc.dir, imported); got != tc.forbidden {
			t.Errorf("%s -> %s forbidden=%v want %v", tc.dir, tc.target, got, tc.forbidden)
		}
	}
}

// Reachable public fields and signatures must not hide an internal type behind
// a local private alias. Private implementation fields remain private; notably
// delegation may hold its one allowed neutral timer without exporting it.
func alignmentPublicLeaks(files []*ast.File) []string {
	type declaration struct {
		expression ast.Expr
		imports    map[string]string
	}
	types := map[string]declaration{}
	importsByFile := map[*ast.File]map[string]string{}
	var out []string
	for _, file := range files {
		imports := map[string]string{}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = p
			if name == "." && strings.Contains(p, "/internal/") {
				out = append(out, "dot import of internal package")
			}
		}
		importsByFile[file] = imports
		for _, decl := range file.Decls {
			if g, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range g.Specs {
					if v, ok := spec.(*ast.TypeSpec); ok {
						types[v.Name.Name] = declaration{v.Type, imports}
					}
				}
			}
		}
	}
	var walk func(ast.Expr, map[string]string, map[string]bool) bool
	walk = func(expr ast.Expr, imports map[string]string, seen map[string]bool) bool {
		if expr == nil {
			return false
		}
		fields := func(list *ast.FieldList) bool {
			if list != nil {
				for _, f := range list.List {
					if walk(f.Type, imports, seen) {
						return true
					}
				}
			}
			return false
		}
		switch v := expr.(type) {
		case *ast.Ident:
			if decl, ok := types[v.Name]; ok && !seen[v.Name] {
				seen[v.Name] = true
				return walk(decl.expression, decl.imports, seen)
			}
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok {
				return strings.Contains(imports[id.Name], "/internal/")
			}
		case *ast.StarExpr:
			return walk(v.X, imports, seen)
		case *ast.ArrayType:
			return walk(v.Elt, imports, seen)
		case *ast.MapType:
			return walk(v.Key, imports, seen) || walk(v.Value, imports, seen)
		case *ast.ChanType:
			return walk(v.Value, imports, seen)
		case *ast.Ellipsis:
			return walk(v.Elt, imports, seen)
		case *ast.ParenExpr:
			return walk(v.X, imports, seen)
		case *ast.IndexExpr:
			return walk(v.X, imports, seen) || walk(v.Index, imports, seen)
		case *ast.IndexListExpr:
			if walk(v.X, imports, seen) {
				return true
			}
			for _, i := range v.Indices {
				if walk(i, imports, seen) {
					return true
				}
			}
		case *ast.FuncType:
			return fields(v.TypeParams) || fields(v.Params) || fields(v.Results)
		case *ast.InterfaceType:
			return fields(v.Methods)
		case *ast.StructType:
			for _, f := range v.Fields.List {
				public := len(f.Names) == 0
				for _, n := range f.Names {
					public = public || n.IsExported()
				}
				if public && walk(f.Type, imports, seen) {
					return true
				}
			}
		case *ast.UnaryExpr:
			return walk(v.X, imports, seen)
		case *ast.BinaryExpr:
			return walk(v.X, imports, seen) || walk(v.Y, imports, seen)
		}
		return false
	}
	for _, file := range files {
		check := func(label string, expr ast.Expr) {
			if walk(expr, importsByFile[file], map[string]bool{}) {
				out = append(out, label)
			}
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() && (d.Recv == nil || ast.IsExported(alignmentReceiver(d.Recv.List[0].Type))) {
					check(d.Name.Name, d.Type)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							check(s.Name.Name, s.Type)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								check(n.Name, s.Type)
								for _, value := range s.Values {
									if selector, ok := value.(*ast.SelectorExpr); ok {
										check(n.Name, selector)
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return out
}
func alignmentReceiver(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return alignmentReceiver(v.X)
	case *ast.IndexExpr:
		return alignmentReceiver(v.X)
	case *ast.IndexListExpr:
		return alignmentReceiver(v.X)
	}
	return ""
}

func TestAlignmentArchitecturePublicTypeBoundary(t *testing.T) {
	packages := map[string][]*ast.File{}
	err := filepath.WalkDir(".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if e.Name() == "internal" || e.Name() == "testdata" || e.Name() == "examples" || e.Name() == "docs" || e.Name() == ".git" || e.Name() == "node_modules" || e.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		packages[filepath.Dir(path)] = append(packages[filepath.Dir(path)], f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for dir, files := range packages {
		for _, leak := range alignmentPublicLeaks(files) {
			t.Errorf("%s exported %s exposes an internal type", dir, leak)
		}
	}
}
func TestAlignmentArchitecturePublicTypeOracle(t *testing.T) {
	for _, tc := range []struct {
		source string
		leaks  bool
	}{
		{`type private = hidden.Controller; type API struct { Value private }`, true},
		{`type private = hidden.Controller; func Exported() *private {return nil}`, true},
		{`type API = hidden.Controller`, true},
		{`type API struct { timer *hidden.Controller }`, false},
		{`type API struct { Value string }; func (a API) PrivateField() string {return ""}`, false},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\nimport hidden \""+alignmentModule+"/internal/activebudget\"\n"+tc.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(alignmentPublicLeaks([]*ast.File{f})) > 0; got != tc.leaks {
			t.Errorf("fixture %s leaks=%v want %v", tc.source, got, tc.leaks)
		}
	}
}

func TestAlignmentArchitectureApprovedWithSurface(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "With") {
				names = append(names, fn.Name.Name)
			}
		}
	}
	sort.Strings(names)
	if len(names) != 27 {
		t.Fatalf("approved With surface is 27 (26 baseline + WithAppendSystemPrompt), got %d: %v", len(names), names)
	}
	for _, required := range []string{"WithAppendSystemPrompt", "WithSpawn", "WithTools"} {
		found := false
		for _, n := range names {
			found = found || n == required
		}
		if !found {
			t.Error(fmt.Sprintf("approved option %s missing", required))
		}
	}
}

// Every provider owns its full package subtree. Similar prefixes are distinct
// packages and must not be mistaken for provider implementations.
func TestAlignmentArchitectureProviderBoundaryOracle(t *testing.T) {
	for _, importer := range []string{"driver", "bridges/a2a", "hosttools/a2adelegation"} {
		for _, provider := range []string{"codex", "claude", "codebuddy", "cursor"} {
			for _, target := range []string{provider, provider + "/child", provider + "/child/nested"} {
				t.Run(importer+"->"+target, func(t *testing.T) {
					if !alignmentForbiddenImport(importer, alignmentModule+"/"+target) {
						t.Fatalf("%s -> %s bypassed the provider package boundary", importer, target)
					}
				})
			}
			for _, target := range []string{provider + "ish", provider + "ish/child"} {
				t.Run(importer+"->"+target, func(t *testing.T) {
					if alignmentForbiddenImport(importer, alignmentModule+"/"+target) {
						t.Fatalf("%s -> %s incorrectly classified a similar prefix as a provider", importer, target)
					}
				})
			}
		}
	}
}
