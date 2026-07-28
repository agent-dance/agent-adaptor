package apifreeze_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/testutil/apifreeze"
)

func TestPublicContractMutationsChangeSnapshot(t *testing.T) {
	baseline := `package sample

type Alias = string
type Defined string
type Embedded struct{}
type Record struct {
	Embedded
	Name string ` + "`json:\"name\"`" + `
	hidden int
}
type Contract interface {
	Read(value string) (int, error)
}
const Mode Defined = "safe"
func Execute(value string) (int, error) { return 0, nil }
func (Record) Validate(value string) error { return nil }
`
	tests := []struct {
		name        string
		old         string
		replacement string
		wantRemoved string
		wantAdded   string
	}{
		{
			name:        "function signature",
			old:         "func Execute(value string) (int, error)",
			replacement: "func Execute(value []byte) (int, error)",
			wantRemoved: "func Execute(value string) (int, error)",
			wantAdded:   "func Execute(value []byte) (int, error)",
		},
		{
			name:        "method signature",
			old:         "func (Record) Validate(value string) error",
			replacement: "func (Record) Validate(value []byte) error",
			wantRemoved: "func (Record).Validate(value string) error",
			wantAdded:   "func (Record).Validate(value []byte) error",
		},
		{
			name:        "interface method",
			old:         "Read(value string) (int, error)",
			replacement: "Read(value []byte) (int, error)",
			wantRemoved: "Read(value string) (int, error)",
			wantAdded:   "Read(value []byte) (int, error)",
		},
		{
			name:        "exported struct embedding",
			old:         "\tEmbedded\n\tName",
			replacement: "\t*Embedded\n\tName",
			wantRemoved: "Embedded",
			wantAdded:   "*Embedded",
		},
		{
			name:        "exported struct field type",
			old:         "Name string",
			replacement: "Name []byte",
			wantRemoved: "Name string `json:\"name\"`",
			wantAdded:   "Name []byte `json:\"name\"`",
		},
		{
			name:        "struct field tag",
			old:         "`json:\"name\"`",
			replacement: "`json:\"display_name\"`",
			wantRemoved: "Name string `json:\"name\"`",
			wantAdded:   "Name string `json:\"display_name\"`",
		},
		{
			name:        "alias becomes defined type",
			old:         "type Alias = string",
			replacement: "type Alias string",
			wantRemoved: "type Alias = string",
			wantAdded:   "type Alias string",
		},
		{
			name:        "alias target",
			old:         "type Alias = string",
			replacement: "type Alias = []byte",
			wantRemoved: "type Alias = string",
			wantAdded:   "type Alias = []byte",
		},
		{
			name:        "constant value",
			old:         `const Mode Defined = "safe"`,
			replacement: `const Mode Defined = "unsafe"`,
			wantRemoved: `const Mode Defined = "safe"`,
			wantAdded:   `const Mode Defined = "unsafe"`,
		},
	}

	want := snapshotForSource(t, baseline)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(baseline, tt.old) {
				t.Fatalf("test bug: baseline does not contain %q", tt.old)
			}
			got := snapshotForSource(t, strings.Replace(baseline, tt.old, tt.replacement, 1))
			if got == want {
				t.Fatal("public declaration mutation did not change the API snapshot")
			}
			diff := apifreeze.Diff(want, got)
			if !containsChangedLine(diff, '-', tt.wantRemoved) || !containsChangedLine(diff, '+', tt.wantAdded) {
				t.Fatalf("diff does not identify the contract mutation:\n%s", diff)
			}
		})
	}
}

func containsChangedLine(diff string, prefix byte, declaration string) bool {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) >= 2 && line[0] == prefix && strings.TrimSpace(line[2:]) == declaration {
			return true
		}
	}
	return false
}

func TestPrivateStructFieldMutationDoesNotChangeSnapshot(t *testing.T) {
	baseline := "package sample\ntype Record struct { Exported string; private int }\n"
	mutated := "package sample\ntype Record struct { Exported string; private []byte }\n"
	if got, want := snapshotForSource(t, mutated), snapshotForSource(t, baseline); got != want {
		t.Fatalf("private implementation leaked into API snapshot:\n%s", apifreeze.Diff(want, got))
	}
}

func TestConstInheritancePreservesStaticTypeAndExactValue(t *testing.T) {
	snapshot := snapshotForSource(t, `package sample
type Kind string
const (
	First Kind = "first"
	Second
	Third = "third"
	Fourth
	Index = iota
)
`)
	for _, declaration := range []string{
		`const First Kind = "first"`,
		`const Second Kind = "first"`,
		`const Third untyped string = "third"`,
		`const Fourth untyped string = "third"`,
		`const Index untyped int = 4`,
	} {
		if !strings.Contains(snapshot, declaration) {
			t.Errorf("snapshot does not preserve %q:\n%s", declaration, snapshot)
		}
	}
}

func TestPrivateInterfaceSealingMethodMutationChangesSnapshot(t *testing.T) {
	baseline := `package sample
type Contract interface {
	seal(value string) error
	Read() string
}
`
	mutated := strings.Replace(baseline, "seal(value string)", "seal(value []byte)", 1)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("private sealing method mutation did not change the API snapshot")
	}
	diff := apifreeze.Diff(want, got)
	if !containsChangedLine(diff, '-', "seal(value string) error") ||
		!containsChangedLine(diff, '+', "seal(value []byte) error") {
		t.Fatalf("diff does not identify the sealing contract mutation:\n%s", diff)
	}
}

func TestPrivateConcreteMethodMutationDoesNotChangeSnapshot(t *testing.T) {
	baseline := `package sample
type Contract interface { seal() }
type Record struct{}
func (Record) seal() {}
`
	mutated := strings.Replace(baseline, "func (Record) seal()", "func (Record) seal(value int)", 1)
	if got, want := snapshotForSource(t, mutated), snapshotForSource(t, baseline); got != want {
		t.Fatalf("private concrete method leaked into API snapshot:\n%s", apifreeze.Diff(want, got))
	}
}

func TestEmbeddedPrivateInterfaceSealingMutationChangesSnapshot(t *testing.T) {
	baseline := `package sample
type sealed interface { seal(value string) error }
type Contract interface { sealed }
`
	mutated := strings.Replace(baseline, "seal(value string)", "seal(value []byte)", 1)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("embedded private sealing method mutation did not change the API snapshot")
	}
	if !strings.Contains(want, "value private seal(value string) error") ||
		!strings.Contains(got, "value private seal(value []byte) error") {
		t.Fatalf("complete exported-interface method set is not explicit:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPrivateEmbeddingPromotedMethodMutationChangesSnapshot(t *testing.T) {
	baseline := `package sample
type carrier struct{}
func (carrier) Public(value string) error { return nil }
type Record struct { carrier }
`
	mutated := strings.Replace(baseline, "Public(value string)", "Public(value []byte)", 1)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("promoted public method mutation did not change the API snapshot")
	}
	if !strings.Contains(want, "value Public(value string) error") ||
		!strings.Contains(got, "value Public(value []byte) error") {
		t.Fatalf("promoted method set is not explicit:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPointerOnlyPromotedMethodStaysPointerOnly(t *testing.T) {
	snapshot := snapshotForSource(t, `package sample
type carrier struct{}
func (*carrier) Public(value string) error { return nil }
type Record struct { carrier }
`)
	if !strings.Contains(snapshot, "pointer Public(value string) error") {
		t.Fatalf("pointer method is missing from promoted method set:\n%s", snapshot)
	}
	if strings.Contains(snapshot, "value Public(value string) error") {
		t.Fatalf("pointer-only method leaked into the value method set:\n%s", snapshot)
	}
}

func TestPrivateConstDependencyMutationChangesExactValue(t *testing.T) {
	baseline := `package sample
const privateMode = 1
const PublicMode = privateMode
`
	mutated := strings.Replace(baseline, "privateMode = 1", "privateMode = 2", 1)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("private constant dependency mutation did not change the API snapshot")
	}
	if !strings.Contains(want, "const PublicMode untyped int = 1") ||
		!strings.Contains(got, "const PublicMode untyped int = 2") {
		t.Fatalf("constant exact values are not explicit:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPrivateConstDependencyMutationChangesStaticType(t *testing.T) {
	baseline := `package sample
type first int
type second int
const privateMode first = 1
const PublicMode = privateMode
`
	mutated := strings.Replace(baseline, "privateMode first", "privateMode second", 1)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("private constant type dependency mutation did not change the API snapshot")
	}
	if !strings.Contains(want, "const PublicMode first = 1") ||
		!strings.Contains(got, "const PublicMode second = 1") {
		t.Fatalf("constant static types are not explicit:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPrivateHelperMutationChangesExportedVariableStaticType(t *testing.T) {
	baseline := `package sample
type first struct{}
type second struct{}
func makeDefault() first { return first{} }
var Default = makeDefault()
`
	mutated := strings.Replace(
		baseline,
		"func makeDefault() first { return first{} }",
		"func makeDefault() second { return second{} }",
		1,
	)
	want := snapshotForSource(t, baseline)
	got := snapshotForSource(t, mutated)
	if want == got {
		t.Fatal("private helper return type mutation did not change the API snapshot")
	}
	if !strings.Contains(want, "var Default first") || !strings.Contains(got, "var Default second") {
		t.Fatalf("variable static types are not explicit:\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestPrivateValueDependencyRenameDoesNotChangeSnapshot(t *testing.T) {
	constBaseline := `package sample
const privateMode = 1
const PublicMode = privateMode
`
	constRenamed := strings.ReplaceAll(constBaseline, "privateMode", "privateSetting")
	if got, want := snapshotForSource(t, constRenamed), snapshotForSource(t, constBaseline); got != want {
		t.Fatalf("private constant name leaked into API snapshot:\n%s", apifreeze.Diff(want, got))
	}

	varBaseline := `package sample
type value struct{}
func makeDefault() value { return value{} }
var Default = makeDefault()
`
	varRenamed := strings.ReplaceAll(varBaseline, "makeDefault", "buildDefault")
	if got, want := snapshotForSource(t, varRenamed), snapshotForSource(t, varBaseline); got != want {
		t.Fatalf("private helper name leaked into API snapshot:\n%s", apifreeze.Diff(want, got))
	}
}

func TestSemanticSnapshotIsStableAcrossModuleDirectories(t *testing.T) {
	firstDir := writeFixtureModule(t)
	secondDir := writeFixtureModule(t)
	first := snapshotForDir(t, filepath.Join(firstDir, "api"))
	second := snapshotForDir(t, filepath.Join(secondDir, "api"))
	if first != second {
		t.Fatalf("snapshot depends on the checkout path:\n%s", apifreeze.Diff(first, second))
	}
	if !strings.Contains(first, `var Default example.com/freeze/dep.Base`) ||
		!strings.Contains(first, `const Mode example.com/freeze/dep.Kind = "safe"`) {
		t.Fatalf("semantic types are not import-path qualified:\n%s", first)
	}
	for _, dir := range []string{firstDir, secondDir} {
		if strings.Contains(first, filepath.Clean(dir)) {
			t.Fatalf("snapshot leaked checkout path %q:\n%s", dir, first)
		}
	}
}

func TestModuleSnapshotUsesGoListCompiledFiles(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=freeze_special")
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/compiled\n\ngo 1.26.0\n",
		"api/default.go": `//go:build !freeze_special
package api
const Selection = "default"
`,
		"api/special.go": `//go:build freeze_special
package api
const Selection = "special"
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := snapshotForDir(t, filepath.Join(dir, "api"))
	if !strings.Contains(snapshot, `const Selection untyped string = "special"`) {
		t.Fatalf("snapshot ignored go list build selection:\n%s", snapshot)
	}
	if strings.Contains(snapshot, `"default"`) {
		t.Fatalf("snapshot included a file excluded by go list:\n%s", snapshot)
	}
}

func writeFixtureModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/freeze\n\ngo 1.26.0\n",
		"dep/dep.go": `package dep
type Base struct{}
func (Base) Ready() bool { return true }
type Kind string
const Mode Kind = "safe"
func Make() Base { return Base{} }
`,
		"api/api.go": `package api
import "example.com/freeze/dep"
type Record struct { dep.Base }
const Mode = dep.Mode
var Default = dep.Make()
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func snapshotForSource(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return snapshotForDir(t, dir)
}

func snapshotForDir(t *testing.T, dir string) string {
	t.Helper()
	surface, err := apifreeze.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return surface.String()
}
