// Package aguiversion contains tests that guard AG-UI contract alignment between
// the Go backend (module pin in go.mod) and the CopilotKit example frontend
// (@ag-ui/core in package-lock.json). When either side is upgraded, update
// the expected constants and re-run go test ./internal/aguiversion/...
package aguiversion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// expectedAGUICoreNPM is the @ag-ui/core version used by
// examples/showcases/web-copilotkit-hitl (see package-lock.json "node_modules/@ag-ui/core").
// Bump this when you upgrade frontend AG-UI packages.
const expectedAGUICoreNPM = "0.0.52"

// expectedGoAGUIModuleSubstr is a stable substring of the go.mod require line for
// github.com/ag-ui-protocol/ag-ui/sdks/community/go — update when the pin changes.
const expectedGoAGUIModuleSubstr = "github.com/ag-ui-protocol/ag-ui/sdks/community/go v0.0.0-20260420210844-ad3c22477b34"

func TestGoModPinsAGUIGoSDK(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	goMod := filepath.Join(repoRoot, "go.mod")
	b, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(b), expectedGoAGUIModuleSubstr) {
		t.Fatalf("go.mod: expected a require line containing %q (update expectedGoAGUIModuleSubstr when bumping)", expectedGoAGUIModuleSubstr)
	}
}

func TestPackageLockAGUICoreVersion(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../.."))
	lockPath := filepath.Join(repoRoot, "examples/showcases/web-copilotkit-hitl/web/package-lock.json")
	b, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read package-lock: %v", err)
	}
	var root struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("json: %v", err)
	}
	pkg, ok := root.Packages["node_modules/@ag-ui/core"]
	if !ok {
		t.Fatalf("package-lock missing node_modules/@ag-ui/core (path format changed?)")
	}
	if pkg.Version != expectedAGUICoreNPM {
		t.Fatalf("@ag-ui/core: got %q, expected %q — update expectedAGUICoreNPM after intentional bump, then review Go ag-ui module pin in go.mod", pkg.Version, expectedAGUICoreNPM)
	}
}
