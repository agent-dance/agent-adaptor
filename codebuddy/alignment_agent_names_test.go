package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

// Independent R018 public boundary fixture. Source 53dbc0d; no SPI payload injection.
// Fake process output uses the exact name read from the public SyncProfile artifact.
func TestAlignmentCodeBuddyPublicNativeCatalogNames(t *testing.T) {
	for _, tc := range []struct{ label, key, runtime, sourceExt string }{
		{"ascii_control", "catalog/ascii", "reviewer", ""},
		{"case", "catalog/case", "ReviewAgent", ""},
		{"unicode", "catalog/unicode", "审查Agent", ""},
		{"default_key", "catalog-default", "", ""},
		{"runtime_extension", "catalog/extension", "reviewer.json", ""},
		{"runtime_inner_dots", "catalog/dots", "reviewer..json", ""},
		{"runtime_device_name", "catalog/device", "CON", ""},
		{"runtime_ui_reserved", "catalog/reserved", "default", ""},
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

func TestAlignmentCodeBuddyPublicRejectsUnsafeNativeNamesBeforeCLI(t *testing.T) {
	for _, tc := range []struct{ label, key, runtime string }{
		{"default_key_slash", "catalog/default", ""},
		{"runtime_slash", "catalog/invalid", "review/agent"},
		{"runtime_backslash", "catalog/invalid", `review\agent`},
		{"runtime_colon", "catalog/invalid", "review:agent"},
		{"runtime_dot", "catalog/invalid", "."},
		{"runtime_dotdot", "catalog/invalid", ".."},
		{"runtime_traversal", "catalog/invalid", "../escape"},
		{"runtime_leading_bom", "catalog/invalid", "\ufeffreviewer"},
		{"runtime_trailing_bom", "catalog/invalid", "reviewer\ufeff"},
		{"source_runtime_slash", "catalog/source", "source/agent"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			root := t.TempDir()
			cfg := alignmentProfileErrorConfig(t)
			healthy := adaptor.New(Driver(cfg), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "healthy", Instructions: "Keep this agent"}}}))
			defer healthy.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := healthy.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			snapshot := func() map[string]string {
				t.Helper()
				entries, err := os.ReadDir(filepath.Join(root, "agents"))
				if err != nil {
					t.Fatal(err)
				}
				files := map[string]string{}
				for _, entry := range entries {
					raw, err := os.ReadFile(filepath.Join(root, "agents", entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					files[entry.Name()] = string(raw)
				}
				return files
			}
			before := snapshot()
			spec := profile.SubAgent{Key: tc.key, RuntimeName: tc.runtime, Instructions: "Must fail before CLI"}
			if tc.label == "source_runtime_slash" {
				spec.SourcePath = filepath.Join(t.TempDir(), "source.md")
				spec.Instructions = ""
				if err := os.WriteFile(spec.SourcePath, []byte("---\nname: source-agent\n---\nNative bytes remain caller-owned\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := adaptor.New(Driver(cfg), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{
				{Key: "a-new-agent", Instructions: "Must not replace healthy"},
				spec,
			}}))
			defer a.Close(context.Background())
			for _, operation := range []string{"SyncProfile", "Run"} {
				t.Run(operation, func(t *testing.T) {
					var err error
					if operation == "SyncProfile" {
						_, err = a.SyncProfile(ctx)
					} else {
						var result *adaptor.Result
						result, err = a.Run(ctx, "Must not launch the CLI canary")
						if result != nil {
							t.Error("rejected native name returned a Result")
						}
					}
					var runErr *adaptor.RunError
					if operation == "Run" {
						// Resource materialization is in the entered Driver.Run's
						// prepareRun, so the existing failure contract is RunError.
						if !errors.As(err, &runErr) || runErr.Reason != adaptor.ReasonInfrastructure || runErr.Result == nil || runErr.Cause == nil || !strings.Contains(runErr.Cause.Error(), "invalid runtime name") {
							t.Errorf("expected infrastructure RunError preserving native-name cause, got %v", err)
						} else if raw := runErr.Result.Raw(); raw.Stdout != "" || raw.Stderr != "" || raw.Terminal != nil || len(runErr.Result.Transcript()) != 0 || runErr.Result.Text != "" {
							t.Error("before-CLI rejection fabricated provider output")
						}
					} else if err == nil || errors.As(err, &runErr) || !strings.Contains(err.Error(), "invalid runtime name") {
						t.Errorf("expected explicit SyncProfile invalid runtime name, got %v", err)
					}
					if after := snapshot(); !reflect.DeepEqual(after, before) {
						t.Error("invalid native name partially changed healthy agent files")
					}
				})
			}
		})
	}
}
