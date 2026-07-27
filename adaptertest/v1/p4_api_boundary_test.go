package adaptertest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// These tests freeze the rejected P4.9 API proposals. They intentionally use
// syntax inspection: Go has no negative compile-time assertion, while the
// contract here is precisely that an exported surface must not reappear.

func TestP4NoParallelSubagentBusAPI(t *testing.T) {
	root := moduleRoot(t)
	var handler, optionsName string
	var file *ast.File
	for _, candidate := range []struct{ file, options string }{
		{"handler_v1.go", "OptionsV1"},
		{"handler_v1.go", "Options"},
		{"handler.go", "Options"},
	} {
		path := filepath.Join(root, "bridges", "sse", candidate.file)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		parsed := parseGoFile(t, path)
		if hasType(parsed, candidate.options) {
			handler, optionsName, file = path, candidate.options, parsed
			break
		}
	}
	if file == nil {
		t.Fatal("P4.9-07: cannot locate the v1 SSE Options declaration")
	}
	fields := exportedStructFields(t, file, optionsName)
	if hasAnonymousStructField(t, file, optionsName) {
		t.Fatalf("P4.9-07: %s.%s has an anonymous field; a promoted delegation overlay can bypass the explicit SubagentBus guard", filepath.Base(handler), optionsName)
	}
	if contains(fields, "SubagentBus") {
		t.Fatalf("P4.9-07: %s.%s reintroduced SubagentBus; team.Option must inject SubagentUpdate into the Runner's single Event stream", filepath.Base(handler), optionsName)
	}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(path, "a2adelegation") || strings.Contains(path, "subagentstream") {
			t.Fatalf("P4.9-07: v1 SSE handler imports %q; it must consume the Runner Event stream without a delegation overlay", path)
		}
	}

	for _, name := range []string{"merge.go", "mux.go"} {
		path := filepath.Join(root, "bridges", "subagentstream", name)
		if _, err := os.Stat(path); err != nil {
			continue // the compatibility bridge is deleted during P5.
		}
		for _, decl := range parseGoFile(t, path).Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && (fn.Name.Name == "MergeV1" || fn.Name.Name == "WrapAGUIV1") {
				t.Fatalf("P4.9-07: rejected parallel entry %s found in %s", fn.Name.Name, path)
			}
		}
	}
}

func TestP4PresentationPolicyStaysOutsideCore(t *testing.T) {
	apiDir := stagingAPIDir(t)
	files := parsePackageFiles(t, apiDir)

	if methods := exportedReceiverMethods(files, "SubagentEventKind"); len(methods) != 0 {
		t.Fatalf("P4.9-10: SubagentEventKind gained core presentation methods %v; hosts render string labels", methods)
	}
	if methods := exportedReceiverMethods(files, "ToolCall"); len(methods) != 0 {
		t.Fatalf("P4.9-10: ToolCall gained core presentation methods %v; argument preview requires host-specific truncation and redaction", methods)
	}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			if fn.Recv != nil && receiverName(fn.Recv.List[0].Type) == "Result" {
				if returnsNamedType(fn.Type.Results, "string") {
					t.Fatalf("P4.9-10: Result method %s returns a presentation string; callers already have independent Text and Summary fields", fn.Name.Name)
				}
				if fieldListContainsNamedType(fn.Type.Results, "RunAttachment") {
					t.Fatalf("P4.9-13: Result method %s exposes RunAttachment; Services is the only runtime observation accessor", fn.Name.Name)
				}
				continue
			}
			if fn.Recv != nil {
				continue
			}
			if acceptsNamedType(fn.Type.Params, "ToolCall") && returnsNamedType(fn.Type.Results, "string") {
				t.Fatalf("P4.9-10: exported helper %s accepts ToolCall and returns string; Args presentation belongs to the host", fn.Name.Name)
			}
			if acceptsNamedType(fn.Type.Params, "Result") && returnsNamedType(fn.Type.Results, "string") {
				t.Fatalf("P4.9-10: exported helper %s accepts Result and returns string; Summary/Text fallback belongs to the host", fn.Name.Name)
			}
			if acceptsNamedType(fn.Type.Params, "Result") && fieldListContainsNamedType(fn.Type.Results, "RunAttachment") {
				t.Fatalf("P4.9-13: exported helper %s exposes RunAttachment from Result; Services is the only runtime observation accessor", fn.Name.Name)
			}
		}
	}

	result := parseGoFile(t, filepath.Join(apiDir, "result.go"))
	assertDirectResultMapping(t, files)
	for _, field := range exportedStructFields(t, result, "Result") {
		if strings.Contains(strings.ToLower(field), "attachment") {
			t.Fatalf("P4.9-13: Result reintroduced attachment field %q; use Services for observed runtime reports", field)
		}
	}
}

func TestP4NoProviderStreamingCapabilityQuery(t *testing.T) {
	files := parsePackageFiles(t, stagingAPIDir(t))
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.IsExported() && strings.Contains(strings.ToLower(fn.Name.Name), "stream") {
				t.Fatalf("P4.9-14: exported function %s creates a parallel provider-streaming surface", fn.Name.Name)
			}
		}
	}
	for _, receiver := range []string{"Agent", "Thread", "Inspector"} {
		for _, method := range exportedReceiverMethods(files, receiver) {
			lower := strings.ToLower(method)
			if method != "Stream" && strings.Contains(lower, "stream") {
				t.Fatalf("P4.9-14: %s.%s is a provider-streaming query; Runner.Stream availability and provider transport fidelity are separate contracts", receiver, method)
			}
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}

func stagingAPIDir(t *testing.T) string {
	t.Helper()
	root := moduleRoot(t)
	next := filepath.Join(root, "next")
	if info, err := os.Stat(next); err == nil && info.IsDir() {
		return next
	}
	return root
}

func parseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func parsePackageFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package directory %s: %v", dir, err)
	}
	var out []*ast.File
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		out = append(out, parseGoFile(t, filepath.Join(dir, entry.Name())))
	}
	return out
}

func assertDirectResultMapping(t *testing.T, files []*ast.File) {
	t.Helper()
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "resultFromResponse" || fn.Body == nil {
				continue
			}
			mapped := map[string]string{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok || receiverName(literal.Type) != "Result" {
					return true
				}
				for _, element := range literal.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok || (key.Name != "Text" && key.Name != "Summary") {
						continue
					}
					selector, ok := pair.Value.(*ast.SelectorExpr)
					base, baseOK := selector.X.(*ast.Ident)
					if !ok || !baseOK || base.Name != "resp" {
						mapped[key.Name] = ""
						continue
					}
					mapped[key.Name] = selector.Sel.Name
				}
				return true
			})
			if mapped["Text"] != "Output" || mapped["Summary"] != "Summary" {
				t.Fatalf("P4.9-10: resultFromResponse mappings are Text<-resp.%s Summary<-resp.%s; both layers must remain direct and independent", mapped["Text"], mapped["Summary"])
			}
			return
		}
	}
	t.Fatal("P4.9-10: resultFromResponse not found")
}

func hasType(file *ast.File, typeName string) bool {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if ok && typeSpec.Name.Name == typeName {
				return true
			}
		}
	}
	return false
}

func exportedStructFields(t *testing.T, file *ast.File, typeName string) []string {
	t.Helper()
	strct := findStructType(t, file, typeName)
	var out []string
	for _, field := range strct.Fields.List {
		for _, name := range field.Names {
			if name.IsExported() {
				out = append(out, name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func hasAnonymousStructField(t *testing.T, file *ast.File, typeName string) bool {
	t.Helper()
	for _, field := range findStructType(t, file, typeName).Fields.List {
		if len(field.Names) == 0 {
			return true
		}
	}
	return false
}

func findStructType(t *testing.T, file *ast.File, typeName string) *ast.StructType {
	t.Helper()
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != typeName {
				continue
			}
			strct, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a struct", typeName)
			}
			return strct
		}
	}
	t.Fatalf("type %s not found in %s", typeName, file.Name.Name)
	return nil
}

func acceptsNamedType(fields *ast.FieldList, typeName string) bool {
	return fieldListContainsNamedType(fields, typeName)
}

func returnsNamedType(fields *ast.FieldList, typeName string) bool {
	return fieldListContainsNamedType(fields, typeName)
}

func fieldListContainsNamedType(fields *ast.FieldList, typeName string) bool {
	if fields == nil {
		return false
	}
	for _, field := range fields.List {
		if expressionContainsNamedType(field.Type, typeName) {
			return true
		}
	}
	return false
}

func expressionContainsNamedType(expr ast.Expr, typeName string) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name == typeName
	case *ast.StarExpr:
		return expressionContainsNamedType(value.X, typeName)
	case *ast.ArrayType:
		return expressionContainsNamedType(value.Elt, typeName)
	case *ast.MapType:
		return expressionContainsNamedType(value.Key, typeName) || expressionContainsNamedType(value.Value, typeName)
	case *ast.ChanType:
		return expressionContainsNamedType(value.Value, typeName)
	case *ast.IndexExpr:
		return expressionContainsNamedType(value.X, typeName) || expressionContainsNamedType(value.Index, typeName)
	case *ast.IndexListExpr:
		if expressionContainsNamedType(value.X, typeName) {
			return true
		}
		for _, index := range value.Indices {
			if expressionContainsNamedType(index, typeName) {
				return true
			}
		}
	}
	return false
}

func exportedReceiverMethods(files []*ast.File, receiver string) []string {
	var out []string
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || len(fn.Recv.List) != 1 {
				continue
			}
			if receiverName(fn.Recv.List[0].Type) == receiver {
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func receiverName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverName(value.X)
	case *ast.IndexExpr:
		return receiverName(value.X)
	case *ast.IndexListExpr:
		return receiverName(value.X)
	default:
		return ""
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
