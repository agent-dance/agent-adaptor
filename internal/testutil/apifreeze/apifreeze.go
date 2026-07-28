// Package apifreeze provides stable public API snapshots for contract tests.
// It combines exported AST declaration shape with go/types semantics for
// method sets, constant values, and variable types. Implementation bodies,
// private struct fields, and private concrete methods stay unfrozen.
package apifreeze

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Declaration is one exported package declaration. Receiver is populated for
// methods; Name is the unqualified exported identifier used by vocabulary
// guards.
type Declaration struct {
	Kind     string
	Name     string
	Receiver string
	Members  []string
	Text     string
}

// Surface is the complete application-facing source surface of one package.
type Surface struct {
	Declarations []Declaration
}

// Read parses the build-selected non-test Go files in dir and returns their
// exported API. Declarations are sorted by stable semantic keys; field order
// within a struct remains source order because it affects layout.
func Read(dir string) (Surface, error) {
	fset := token.NewFileSet()
	var loaded *loadedPackage
	var paths []string
	if isInModule(dir) {
		var err error
		loaded, err = loadPackage(dir, fset)
		if err != nil {
			return Surface{}, fmt.Errorf("load %s: %w", dir, err)
		}
		paths = append(paths, loaded.compiledGoFiles...)
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Surface{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			matched, err := build.Default.MatchFile(dir, entry.Name())
			if err != nil {
				return Surface{}, fmt.Errorf("match build constraints for %s: %w", entry.Name(), err)
			}
			if matched {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
	}
	sort.Strings(paths)

	var files []*ast.File
	var declarations []Declaration
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return Surface{}, fmt.Errorf("parse %s: %w", path, err)
		}
		files = append(files, file)
		fileDeclarations, err := readFile(fset, file)
		if err != nil {
			return Surface{}, fmt.Errorf("read %s: %w", path, err)
		}
		declarations = append(declarations, fileDeclarations...)
	}

	typedPackage, err := checkPackage(dir, fset, files, loaded)
	if err != nil {
		return Surface{}, err
	}
	if err := addValueSemantics(declarations, typedPackage); err != nil {
		return Surface{}, err
	}
	declarations = append(declarations, methodSetDeclarations(typedPackage)...)

	sort.Slice(declarations, func(i, j int) bool {
		return declarationKey(declarations[i]) < declarationKey(declarations[j])
	})
	return Surface{Declarations: declarations}, nil
}

// String renders a stable, readable golden snapshot.
func (s Surface) String() string {
	var out strings.Builder
	for i, declaration := range s.Declarations {
		if i != 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(declaration.Text)
	}
	if len(s.Declarations) != 0 {
		out.WriteByte('\n')
	}
	return out.String()
}

// Diff returns a line-oriented LCS diff suitable for a test failure. The
// expected snapshot is prefixed with '-' and the observed surface with '+'.
func Diff(want, got string) string {
	wantLines := splitLines(want)
	gotLines := splitLines(got)
	lcs := make([][]int, len(wantLines)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(gotLines)+1)
	}
	for i := len(wantLines) - 1; i >= 0; i-- {
		for j := len(gotLines) - 1; j >= 0; j-- {
			if wantLines[i] == gotLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out strings.Builder
	for i, j := 0, 0; i < len(wantLines) || j < len(gotLines); {
		switch {
		case i < len(wantLines) && j < len(gotLines) && wantLines[i] == gotLines[j]:
			i++
			j++
		case j == len(gotLines) || (i < len(wantLines) && lcs[i+1][j] >= lcs[i][j+1]):
			fmt.Fprintf(&out, "- %s\n", wantLines[i])
			i++
		default:
			fmt.Fprintf(&out, "+ %s\n", gotLines[j])
			j++
		}
	}
	return out.String()
}

func readFile(fset *token.FileSet, file *ast.File) ([]Declaration, error) {
	var declarations []Declaration
	for _, raw := range file.Decls {
		switch declaration := raw.(type) {
		case *ast.FuncDecl:
			if !declaration.Name.IsExported() {
				continue
			}
			item, ok, err := functionDeclaration(fset, declaration)
			if err != nil {
				return nil, err
			}
			if ok {
				declarations = append(declarations, item)
			}
		case *ast.GenDecl:
			items, err := generalDeclarations(fset, declaration)
			if err != nil {
				return nil, err
			}
			declarations = append(declarations, items...)
		}
	}
	return declarations, nil
}

func functionDeclaration(fset *token.FileSet, fn *ast.FuncDecl) (Declaration, bool, error) {
	signature, err := renderNode(fset, fn.Type)
	if err != nil {
		return Declaration{}, false, err
	}
	signature = strings.TrimPrefix(signature, "func")
	if fn.Recv == nil {
		return Declaration{Kind: "func", Name: fn.Name.Name, Text: "func " + fn.Name.Name + signature}, true, nil
	}
	if len(fn.Recv.List) != 1 {
		return Declaration{}, false, fmt.Errorf("method %s has %d receivers", fn.Name.Name, len(fn.Recv.List))
	}
	receiverName := baseTypeName(fn.Recv.List[0].Type)
	if !ast.IsExported(receiverName) {
		return Declaration{}, false, nil
	}
	receiver, err := renderNode(fset, fn.Recv.List[0].Type)
	if err != nil {
		return Declaration{}, false, err
	}
	return Declaration{
		Kind:     "method",
		Name:     fn.Name.Name,
		Receiver: receiverName,
		Text:     "func (" + receiver + ")." + fn.Name.Name + signature,
	}, true, nil
}

func generalDeclarations(fset *token.FileSet, declaration *ast.GenDecl) ([]Declaration, error) {
	var out []Declaration
	for _, raw := range declaration.Specs {
		switch spec := raw.(type) {
		case *ast.TypeSpec:
			if !spec.Name.IsExported() {
				continue
			}
			item, err := typeDeclaration(fset, spec)
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		case *ast.ValueSpec:
			out = append(out, valueDeclarations(declaration.Tok, spec)...)
		}
	}
	return out, nil
}

func typeDeclaration(fset *token.FileSet, spec *ast.TypeSpec) (Declaration, error) {
	typeParams := ""
	if spec.TypeParams != nil {
		rendered, err := renderNode(fset, spec.TypeParams)
		if err != nil {
			return Declaration{}, err
		}
		typeParams = rendered
	}
	prefix := "type " + spec.Name.Name + typeParams
	if spec.Assign.IsValid() {
		target, err := renderNode(fset, spec.Type)
		if err != nil {
			return Declaration{}, err
		}
		return Declaration{Kind: "alias", Name: spec.Name.Name, Text: prefix + " = " + target}, nil
	}

	switch value := spec.Type.(type) {
	case *ast.StructType:
		text, members, err := publicStruct(fset, prefix, value)
		return Declaration{Kind: "type", Name: spec.Name.Name, Members: members, Text: text}, err
	case *ast.InterfaceType:
		text, members, err := publicInterface(fset, prefix, value)
		return Declaration{Kind: "interface", Name: spec.Name.Name, Members: members, Text: text}, err
	default:
		underlying, err := renderNode(fset, spec.Type)
		if err != nil {
			return Declaration{}, err
		}
		return Declaration{Kind: "type", Name: spec.Name.Name, Text: prefix + " " + underlying}, nil
	}
}

func publicStruct(fset *token.FileSet, prefix string, value *ast.StructType) (string, []string, error) {
	fields := make([]string, 0, len(value.Fields.List))
	var members []string
	for _, field := range value.Fields.List {
		if len(field.Names) == 0 {
			if !embeddedTypeIsExported(field.Type) {
				continue
			}
			typeText, err := renderNode(fset, field.Type)
			if err != nil {
				return "", nil, err
			}
			fields = append(fields, typeText+renderTag(field.Tag))
			members = append(members, baseTypeName(field.Type))
			continue
		}
		var names []string
		for _, name := range field.Names {
			if name.IsExported() {
				names = append(names, name.Name)
			}
		}
		if len(names) == 0 {
			continue
		}
		typeText, err := renderNode(fset, field.Type)
		if err != nil {
			return "", nil, err
		}
		fields = append(fields, strings.Join(names, ", ")+" "+typeText+renderTag(field.Tag))
		members = append(members, names...)
	}
	return renderBlock(prefix+" struct", fields, "// no exported fields"), members, nil
}

func publicInterface(fset *token.FileSet, prefix string, value *ast.InterfaceType) (string, []string, error) {
	methods := make([]string, 0, len(value.Methods.List))
	var members []string
	for _, field := range value.Methods.List {
		if len(field.Names) == 0 {
			embedded, err := renderNode(fset, field.Type)
			if err != nil {
				return "", nil, err
			}
			methods = append(methods, embedded)
			members = append(members, baseTypeName(field.Type))
			continue
		}
		for _, name := range field.Names {
			signature, err := renderNode(fset, field.Type)
			if err != nil {
				return "", nil, err
			}
			methods = append(methods, name.Name+strings.TrimPrefix(signature, "func"))
			members = append(members, name.Name)
		}
	}
	sort.Strings(methods)
	sort.Strings(members)
	return renderBlock(prefix+" interface", methods, "// no methods"), members, nil
}

func checkPackage(dir string, fset *token.FileSet, files []*ast.File, loaded *loadedPackage) (*types.Package, error) {
	if len(files) == 0 {
		return types.NewPackage("apifreeze.local/empty", "empty"), nil
	}

	packagePath := "apifreeze.local/" + files[0].Name.Name
	packageImporter := types.Importer(importer.Default())
	goVersion := ""
	if loaded == nil && needsExportDataImporter(files) {
		var err error
		loaded, err = loadPackage(dir, fset)
		if err != nil {
			return nil, fmt.Errorf("resolve imports for %s: %w", dir, err)
		}
	}
	if loaded != nil {
		packagePath = loaded.packagePath
		packageImporter = loaded.importer
		goVersion = loaded.goVersion
	}

	config := types.Config{
		IgnoreFuncBodies:         true,
		DisableUnusedImportCheck: true,
		Importer:                 packageImporter,
		GoVersion:                goVersion,
		Sizes:                    types.SizesFor(runtime.Compiler, build.Default.GOARCH),
	}
	typedPackage, err := config.Check(packagePath, fset, files, nil)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %w", dir, err)
	}
	return typedPackage, nil
}

func isInModule(dir string) bool {
	current, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		info, err := os.Stat(filepath.Join(current, "go.mod"))
		if err == nil && !info.IsDir() {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func needsExportDataImporter(files []*ast.File) bool {
	for _, file := range files {
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				continue
			}
			first := path
			if slash := strings.IndexByte(first, '/'); slash >= 0 {
				first = first[:slash]
			}
			if strings.Contains(first, ".") {
				return true
			}
		}
	}
	return false
}

type listedPackage struct {
	ImportPath      string
	Export          string
	Dir             string
	CompiledGoFiles []string
	Module          *struct {
		GoVersion string
	}
}

type loadedPackage struct {
	packagePath     string
	importer        types.Importer
	goVersion       string
	compiledGoFiles []string
}

func loadPackage(dir string, fset *token.FileSet) (*loadedPackage, error) {
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	command := exec.Command("go", "list", "-deps", "-export", "-compiled", "-json", ".")
	command.Dir = absoluteDir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	exportFiles := make(map[string]string)
	packagePath := ""
	goVersion := ""
	var compiledGoFiles []string
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var item listedPackage
		err := decoder.Decode(&item)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		if item.Export != "" {
			exportFiles[item.ImportPath] = item.Export
		}
		if sameDirectory(item.Dir, absoluteDir) {
			packagePath = item.ImportPath
			for _, path := range item.CompiledGoFiles {
				if !filepath.IsAbs(path) {
					path = filepath.Join(item.Dir, path)
				}
				compiledGoFiles = append(compiledGoFiles, filepath.Clean(path))
			}
			if item.Module != nil && item.Module.GoVersion != "" {
				goVersion = "go" + item.Module.GoVersion
			}
		}
	}
	if packagePath == "" {
		return nil, fmt.Errorf("go list did not identify package in %s", absoluteDir)
	}
	if len(compiledGoFiles) == 0 {
		return nil, fmt.Errorf("go list reported no compiled Go files for %s", packagePath)
	}

	lookup := func(path string) (io.ReadCloser, error) {
		exportFile, ok := exportFiles[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(exportFile)
	}
	gcImporter := importer.ForCompiler(fset, runtime.Compiler, lookup)
	return &loadedPackage{
		packagePath: packagePath,
		importer: exportMapImporter{
			exports:  exportFiles,
			primary:  gcImporter,
			fallback: importer.Default(),
		},
		goVersion:       goVersion,
		compiledGoFiles: compiledGoFiles,
	}, nil
}

func sameDirectory(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

type exportMapImporter struct {
	exports  map[string]string
	primary  types.Importer
	fallback types.Importer
}

func (i exportMapImporter) Import(path string) (*types.Package, error) {
	if _, ok := i.exports[path]; ok {
		return i.primary.Import(path)
	}
	return i.fallback.Import(path)
}

func addValueSemantics(declarations []Declaration, typedPackage *types.Package) error {
	for index := range declarations {
		declaration := &declarations[index]
		object := typedPackage.Scope().Lookup(declaration.Name)
		switch declaration.Kind {
		case "const":
			constant, ok := object.(*types.Const)
			if !ok {
				return fmt.Errorf("exported const %s missing from type-checked scope", declaration.Name)
			}
			declaration.Text = "const " + declaration.Name + " " + canonicalType(constant.Type(), typedPackage) + " = " + constant.Val().ExactString()
		case "var":
			variable, ok := object.(*types.Var)
			if !ok {
				return fmt.Errorf("exported var %s missing from type-checked scope", declaration.Name)
			}
			declaration.Text = "var " + declaration.Name + " " + canonicalType(variable.Type(), typedPackage)
		}
	}
	return nil
}

func methodSetDeclarations(typedPackage *types.Package) []Declaration {
	var declarations []Declaration
	for _, name := range typedPackage.Scope().Names() {
		if !ast.IsExported(name) {
			continue
		}
		typeName, ok := typedPackage.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}

		var entries []string
		var members []string
		_, isInterface := typeName.Type().Underlying().(*types.Interface)
		valueEntries, valueMembers := publicMethodSet("value", typeName.Type(), typedPackage, isInterface)
		entries = append(entries, valueEntries...)
		members = append(members, valueMembers...)
		pointerEntries, pointerMembers := publicMethodSet("pointer", types.NewPointer(typeName.Type()), typedPackage, false)
		entries = append(entries, pointerEntries...)
		members = append(members, pointerMembers...)
		if len(entries) == 0 {
			continue
		}
		sort.Strings(members)
		declarations = append(declarations, Declaration{
			Kind:     "methodset",
			Name:     name,
			Receiver: name,
			Members:  members,
			Text:     renderBlock("methodset "+name, entries, "// no exported methods"),
		})
	}
	return declarations
}

func publicMethodSet(form string, target types.Type, typedPackage *types.Package, includePrivate bool) ([]string, []string) {
	set := types.NewMethodSet(target)
	entries := make([]string, 0, set.Len())
	members := make([]string, 0, set.Len())
	for index := 0; index < set.Len(); index++ {
		selection := set.At(index)
		method := selection.Obj()
		if !method.Exported() && !includePrivate {
			continue
		}
		displayName := method.Name()
		if !method.Exported() {
			displayName = "private " + method.Name()
			if method.Pkg() != typedPackage {
				displayName = "private " + types.Id(method.Pkg(), method.Name())
			}
		}
		signature := strings.TrimPrefix(canonicalType(selection.Type(), typedPackage), "func")
		entries = append(entries, form+" "+displayName+signature)
		members = append(members, method.Name())
	}
	return entries, members
}

func canonicalType(value types.Type, owner *types.Package) string {
	return types.TypeString(value, func(imported *types.Package) string {
		if imported == owner {
			return ""
		}
		return imported.Path()
	})
}

func valueDeclarations(kind token.Token, spec *ast.ValueSpec) []Declaration {
	var out []Declaration
	for _, name := range spec.Names {
		if !name.IsExported() {
			continue
		}
		declarationKind := strings.ToLower(kind.String())
		out = append(out, Declaration{Kind: declarationKind, Name: name.Name, Text: declarationKind + " " + name.Name})
	}
	return out
}

func renderBlock(prefix string, members []string, emptyMarker string) string {
	var out strings.Builder
	out.WriteString(prefix)
	out.WriteString(" {\n")
	if len(members) == 0 {
		out.WriteString("\t")
		out.WriteString(emptyMarker)
		out.WriteByte('\n')
	} else {
		for _, member := range members {
			out.WriteByte('\t')
			out.WriteString(member)
			out.WriteByte('\n')
		}
	}
	out.WriteByte('}')
	return out.String()
}

func renderTag(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	return " " + tag.Value
}

func embeddedTypeIsExported(expr ast.Expr) bool {
	return ast.IsExported(baseTypeName(expr))
}

func baseTypeName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return baseTypeName(value.X)
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.IndexExpr:
		return baseTypeName(value.X)
	case *ast.IndexListExpr:
		return baseTypeName(value.X)
	case *ast.ParenExpr:
		return baseTypeName(value.X)
	default:
		return ""
	}
}

func renderNode(fset *token.FileSet, node any) (string, error) {
	var out bytes.Buffer
	if err := format.Node(&out, fset, node); err != nil {
		return "", err
	}
	return out.String(), nil
}

func declarationKey(declaration Declaration) string {
	if declaration.Kind == "method" {
		return "method\x00" + declaration.Receiver + "\x00" + declaration.Name
	}
	if declaration.Kind == "methodset" {
		return "declaration\x00" + declaration.Name + "\xffmethodset"
	}
	return "declaration\x00" + declaration.Name + "\x00" + declaration.Kind
}

func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
