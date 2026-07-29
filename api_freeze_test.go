package adaptor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/testutil/apifreeze"
)

const updateAPIGoldenEnv = "AGENT_ADAPTOR_UPDATE_API_GOLDEN"

// TestRootAPISurfaceIsFrozen protects the complete application-facing source
// contract: function and method signatures, complete exported method sets,
// interfaces (including private sealing methods), exported struct fields and
// tags, aliases versus defined types, exact constant values/types, and
// variable static types. It intentionally excludes implementation bodies,
// initializers, private concrete methods, and unexported struct fields.
func TestRootAPISurfaceIsFrozen(t *testing.T) {
	assertAPIGolden(t, ".", filepath.Join("testdata", "root_api.golden"))
}

func TestRejectedRootVocabularyCannotReappear(t *testing.T) {
	surface, err := apifreeze.Read(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range surface.Declarations {
		assertApprovedRootName(t, declaration.Name, declaration.Text)
		if declaration.Receiver != "" {
			assertApprovedRootName(t, declaration.Receiver, declaration.Text)
		}
		if declaration.Kind == "interface" || declaration.Kind == "methodset" {
			for _, member := range declaration.Members {
				assertApprovedRootName(t, member, declaration.Text)
			}
		}
	}
}

func assertAPIGolden(t *testing.T, dir, goldenPath string) {
	t.Helper()
	surface, err := apifreeze.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := surface.String()
	if os.Getenv(updateAPIGoldenEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read API golden %s: %v (set %s=1 only when approving an intentional v1 API decision)", goldenPath, err, updateAPIGoldenEnv)
	}
	wantText := strings.ReplaceAll(string(want), "\r\n", "\n")
	if wantText != got {
		t.Fatalf("root public API changed; review the v1 contract and update the approved snapshot only for an intentional API decision:\n%s", apifreeze.Diff(wantText, got))
	}
}

func assertApprovedRootName(t *testing.T, name, location string) {
	t.Helper()
	forbiddenExact := map[string]struct{}{
		"SDK": {}, "NewSDK": {}, "Start": {}, "RunHandle": {},
		"Binding": {}, "AgentBinding": {}, "TypedAgentBinding": {},
		"Registry": {}, "DefaultAgent": {}, "WithDefaultAgent": {},
		"WithDriver": {}, "NewAdapter": {}, "NewThread": {},
		"SchemaStrict": {}, "SchemaFlexible": {}, "SchemaPromptOnly": {},
	}
	if _, forbidden := forbiddenExact[name]; forbidden {
		t.Fatalf("%s reintroduces rejected root API vocabulary %q", location, name)
	}
	if strings.Contains(name, "SDK") || strings.HasPrefix(name, "Start") ||
		strings.Contains(name, "RunHandle") || strings.Contains(name, "Binding") ||
		strings.Contains(name, "Registry") || strings.Contains(name, "DefaultAgent") {
		t.Fatalf("%s reintroduces rejected root API vocabulary %q", location, name)
	}
}
