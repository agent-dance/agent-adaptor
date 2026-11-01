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
// examples/web-chat/copilotkit (see package-lock.json "node_modules/@ag-ui/core").
// Bump this when you upgrade frontend AG-UI packages.
const expectedAGUICoreNPM = "0.0.57"

// expectedGoAGUIModuleSubstr is a stable substring of the go.mod require line for
// github.com/ag-ui-protocol/ag-ui/sdks/community/go — update when the pin changes.
const expectedGoAGUIModuleSubstr = "github.com/ag-ui-protocol/ag-ui/sdks/community/go v0.0.0-20261030214150-b2f4a1c1ea63"

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
	lockPath := filepath.Join(repoRoot, "examples/web-chat/copilotkit/web/package-lock.json")
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

// CopilotKit owns its transitive 0.0.57 client. Direct examples share the
// explicitly tested 1.0.3 contract in both manifests and lockfiles.
func TestDirectClientPins(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	for _, example := range []string{"aguiclient", "assistant-ui"} {
		raw, err := os.ReadFile(filepath.Join(root, "examples/web-chat", example, "web/package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var pkg struct {
			Dependencies map[string]string `json:"dependencies"`
		}
		if err = json.Unmarshal(raw, &pkg); err != nil {
			t.Fatal(err)
		}
		if got := pkg.Dependencies["@ag-ui/client"]; got != "1.0.3" {
			t.Fatalf("%s @ag-ui/client: %q", example, got)
		}
		lock, err := os.ReadFile(filepath.Join(root, "examples/web-chat", example, "web/package-lock.json"))
		if err != nil {
			t.Fatal(err)
		}
		var locked struct {
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
		}
		if err = json.Unmarshal(lock, &locked); err != nil {
			t.Fatal(err)
		}
		if got := locked.Packages["node_modules/@ag-ui/client"].Version; got != "1.0.3" {
			t.Fatalf("%s locked @ag-ui/client: %q", example, got)
		}
		if example == "assistant-ui" {
			for name, want := range map[string]string{"@assistant-ui/react": "0.15.32", "@assistant-ui/react-ag-ui": "0.1.1", "@assistant-ui/react-markdown": "0.14.24", "react": "19.3.1", "react-dom": "19.3.1"} {
				if got := pkg.Dependencies[name]; got != want {
					t.Fatalf("%s: %q, want %q", name, got, want)
				}
			}
		}
	}
}
