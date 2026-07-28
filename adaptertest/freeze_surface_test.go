package adaptertest

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/testutil/apifreeze"
)

// TestDriverAPISurfaceIsFrozen protects the complete extension-author SPI,
// including signatures, interfaces, exported fields and tags, alias targets,
// complete exported method sets, exact constant values, and variable static
// types. Private driver implementation remains outside the snapshot.
func TestDriverAPISurfaceIsFrozen(t *testing.T) {
	root := moduleRoot(t)
	driverDir := filepath.Join(root, "driver")
	goldenPath := filepath.Join("testdata", "driver_api.golden")
	surface, err := apifreeze.Read(driverDir)
	if err != nil {
		t.Fatal(err)
	}
	got := surface.String()
	if os.Getenv("AGENT_ADAPTOR_UPDATE_API_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read API golden %s: %v (set AGENT_ADAPTOR_UPDATE_API_GOLDEN=1 only when approving an intentional Driver SPI decision)", goldenPath, err)
	}
	wantText := strings.ReplaceAll(string(want), "\r\n", "\n")
	if wantText != got {
		t.Fatalf("driver public API changed; review the v1 SPI and update the approved snapshot only for an intentional API decision:\n%s", apifreeze.Diff(wantText, got))
	}
}

func TestRejectedDriverVocabularyCannotReappear(t *testing.T) {
	surface, err := apifreeze.Read(filepath.Join(moduleRoot(t), "driver"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"DriverAdapter":      {},
		"DriverCheckpoint":   {},
		"DriverDescriptor":   {},
		"DriverRunRequest":   {},
		"DriverRunResult":    {},
		"DriverSessionState": {},
	}
	for _, declaration := range surface.Declarations {
		names := []string{declaration.Name}
		if declaration.Kind == "interface" || declaration.Kind == "methodset" {
			names = append(names, declaration.Members...)
		}
		for _, name := range names {
			if _, found := forbidden[name]; found {
				t.Fatalf("%s reintroduces rejected Driver SPI vocabulary %q", declaration.Text, name)
			}
			if strings.HasPrefix(name, "DriverRun") || strings.HasPrefix(name, "DriverSession") {
				t.Fatalf("%s reintroduces rejected Driver SPI vocabulary %q", declaration.Text, name)
			}
		}
	}
}

// TestFreezeResponseAndResultAccountingSurfaces prevents the removed parallel
// question/billing result planes from returning and freezes the observed
// usage distinction at both sides of the Driver boundary.
func TestFreezeResponseAndResultAccountingSurfaces(t *testing.T) {
	root := moduleRoot(t)
	driverFile := parseGoFile(t, filepath.Join(root, "driver", "run.go"))
	response := findStructType(t, driverFile, "Response")
	forbiddenFields := map[string]bool{
		"Question": true, "Biller": true, "BillingType": true, "CostUSD": true,
	}
	for _, field := range response.Fields.List {
		for _, name := range field.Names {
			if forbiddenFields[name.Name] {
				t.Errorf("driver.Response reintroduced removed field %s", name.Name)
			}
		}
	}
	for _, file := range parsePackageFiles(t, filepath.Join(root, "driver")) {
		for _, removed := range []string{"RunQuestion", "RunChoice"} {
			if hasType(file, removed) {
				t.Errorf("driver reintroduced removed parallel HITL type %s", removed)
			}
		}
	}

	resultFile := parseGoFile(t, filepath.Join(root, "result.go"))
	result := findStructType(t, resultFile, "Result")
	for _, field := range result.Fields.List {
		if len(field.Names) != 1 || field.Names[0].Name != "Usage" {
			continue
		}
		pointer, ok := field.Type.(*ast.StarExpr)
		if !ok {
			t.Fatalf("Result.Usage must be *Usage so nil means unobserved; got %T", field.Type)
		}
		ident, identOK := pointer.X.(*ast.Ident)
		if !identOK || ident.Name != "Usage" {
			t.Fatalf("Result.Usage must be *Usage so nil means unobserved; got %T", field.Type)
		}
		return
	}
	t.Fatal("Result.Usage field not found")
}

func TestFreezeCodeBuddyPermissionModesMatchCLI(t *testing.T) {
	root := moduleRoot(t)
	for _, file := range parsePackageFiles(t, filepath.Join(root, "codebuddy")) {
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range group.Specs {
				values, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range values.Names {
					if name.Name == "PermissionFullAccess" {
						t.Fatal("CodeBuddy fullAccess is an IDE protocol state, not a valid --permission-mode CLI value")
					}
				}
			}
		}
	}
}
