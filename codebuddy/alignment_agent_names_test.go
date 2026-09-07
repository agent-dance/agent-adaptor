package codebuddy

import (
	"context"
	"encoding/json"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Independent R018 public boundary fixture. Source 53dbc0d; no SPI payload injection.
// Fake process output uses the exact name read from the public SyncProfile artifact.
func TestAlignmentCodeBuddyPublicNativeCatalogNames(t *testing.T) {
	for _, tc := range []struct{ label, key, runtime, sourceExt string }{
		{"ascii_control", "catalog/ascii", "reviewer", ""},
		{"case", "catalog/case", "ReviewAgent", ""},
		{"unicode", "catalog/unicode", "审查Agent", ""},
		{"default_key", "catalog/default", "", ""},
		{"runtime_extension", "catalog/extension", "reviewer.json", ""},
		{"source_md_control", "catalog/source-md", "source-agent", ".md"},
		{"source_other_extension", "catalog/source-txt", "source-agent", ".txt"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			private := t.TempDir()
			protocol := filepath.Join(private, "protocol")
			spec := profile.SubAgent{Key: tc.key, RuntimeName: tc.runtime, Instructions: "R018 exact instructions"}
			if tc.sourceExt != "" {
				spec.SourcePath = filepath.Join(private, "native"+tc.sourceExt)
				spec.Instructions = ""
				if err := os.WriteFile(spec.SourcePath, []byte("---\nname: \"source-agent\"\ndescription: \"Native escape\"\n---\nR018 exact source"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}, {Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: protocol}, {Name: "USERPROFILE", Value: private}, {Name: "XDG_CONFIG_HOME", Value: private}}, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}), adaptor.WithBlockingEvents())
			defer fx.close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := fx.agent.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if fx.spawnCount(t) != 0 {
				t.Fatal("SyncProfile launched process")
			}
			entries, err := os.ReadDir(filepath.Join(fx.profileDir, "agents"))
			if err != nil {
				t.Fatal(err)
			}
			actualName := ""
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(fx.profileDir, "agents", e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "name: ") {
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "name: ")), &actualName); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if actualName == "" {
				t.Fatalf("SyncProfile succeeded but official *.md loader has no agent; runtime=%q source=%q entries=%v", tc.runtime, tc.sourceExt, entries)
			}
			expected := tc.runtime
			if expected == "" {
				expected = tc.key
			}
			t.Logf("resolved runtime=%q; native loader name=%q", expected, actualName)
			input, _ := json.Marshal(map[string]any{"subagent_type": actualName, "prompt": "R018 fixture"})
			frames := strings.Join([]string{`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`, alignmentCall("Task", "r018-call", string(input)), alignmentResult("r018-call", `"done"`, false, ""), `{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"done"}`, ""}, "\n")
			if err := os.WriteFile(protocol, []byte(frames), 0600); err != nil {
				t.Fatal(err)
			}
			s := fx.agent.Stream(ctx, "Invoke materialized agent")
			completed := 0
			for e := range s.Events() {
				if v, ok := e.(adaptor.CapabilityInvocation); ok && v.Invocation.Ref == (capability.Ref{Kind: capability.Subagent, Key: tc.key, Operation: "spawn"}) && v.Invocation.Phase == capability.Completed {
					completed++
				}
			}
			r, err := s.Result()
			if err != nil {
				t.Fatal(err)
			}
			if r.Raw().Stdout != frames {
				t.Fatal("raw formal audit changed")
			}
			if fx.spawnCount(t) != 1 {
				t.Fatal("fake process did not execute exactly once")
			}
			if completed != 1 {
				t.Fatalf("actual native name %q cannot map to public catalog runtime %q: completed=%d", actualName, expected, completed)
			}
		})
	}
}
