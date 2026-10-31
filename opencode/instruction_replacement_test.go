package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

const replacementAgents = `[
 {"name":"build","mode":"primary","permission":[{"permission":"read","pattern":"*.env","action":"deny"}]},
 {"name":"custom","mode":"all","prompt":"original custom prompt","model":{"providerID":"test","modelID":"native"},"options":{"temperature":0.5}},
 {"name":"plan","mode":"primary","prompt":"original plan prompt"},
 {"name":"explore","mode":"subagent","prompt":"original explorer prompt"},
 {"name":"title","mode":"primary","hidden":true,"prompt":"original title prompt"}
]`

func TestServerReplacementInstructions(t *testing.T) {
	for _, selected := range []string{"", "custom"} {
		t.Run("agent-"+selected, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, serverHappy, "replacement stderr", 0)
			dir := filepath.Dir(capture)
			for name, content := range map[string]string{"native-agents.json": replacementAgents, "native-config.json": `{"default_agent":"custom"}`} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			profileDir := t.TempDir()
			d := Driver(Config{Command: command, Agent: selected, Pure: true, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: profileDir}, {Name: "AGENT_ADAPTOR_OPENCODE_EXCLUSIVE_WRITER", Value: "1"}}})
			defer d.(configuredDriver).CloseProcesses(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			req := driver.Request{Prompt: "hello", Instructions: &driver.InstructionsBundleRef{Content: "Literal {env:KEY} and {file:path} replacement", Mode: driver.InstructionModeReplace}, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart, EngineSessionID: "replacement"}}
			first, err := d.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || first.Failure != nil || first.Checkpoint == nil {
				t.Fatalf("first: %+v %v raw=%+v", first, err, first.RawStreams)
			}
			if strings.Count(first.RawStreams.Stdout, "opencode server listening") != 2 {
				t.Fatal("discovery and execution raw output not both retained")
			}
			req.Session.State = first.Checkpoint.State
			second, err := d.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || second.Metadata["opencode.server_reused"] != "true" || second.Metadata["opencode.server_pid"] != first.Metadata["opencode.server_pid"] {
				t.Fatalf("reuse: %+v %v", second, err)
			}
			data, _ := os.ReadFile(capture)
			var call struct {
				Input  map[string]any
				Config string
			}
			if err := json.Unmarshal(data, &call); err != nil {
				t.Fatal(err)
			}
			if _, exists := call.Input["system"]; exists {
				t.Fatal("replacement was also appended as per-message system")
			}
			if selected == "" && call.Input["agent"] != nil {
				t.Fatal("native default agent was overridden")
			}
			var overlay struct{ Agent map[string]map[string]any }
			if err := json.Unmarshal([]byte(call.Config), &overlay); err != nil {
				t.Fatal(err)
			}
			if overlay.Agent["custom"]["prompt"] != req.Instructions.Content || overlay.Agent["title"] != nil || overlay.Agent["explore"] != nil {
				t.Fatalf("prompt scope: %#v", overlay.Agent)
			}
			if selected == "custom" && (overlay.Agent["build"] != nil || overlay.Agent["plan"] != nil) {
				t.Fatal("explicit agent replaced unrelated prompts")
			}
			if strings.Contains(call.Config, "{env:") || strings.Contains(call.Config, "{file:") {
				t.Fatal("literal prompt was exposed to native config interpolation")
			}
			req.Instructions.Content = "changed replacement"
			req.Session.State = second.Checkpoint.State
			third, err := d.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || third.Metadata["opencode.server_pid"] == second.Metadata["opencode.server_pid"] {
				t.Fatalf("changed prompt reused old writer: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "overlap")); !os.IsNotExist(err) {
				t.Fatal("overlapping writers")
			}
			spawns, _ := os.ReadFile(filepath.Join(dir, "spawns.log"))
			prompts, _ := os.ReadFile(filepath.Join(dir, "prompts.log"))
			if len(strings.Fields(string(spawns))) != 4 || len(strings.Fields(string(prompts))) != 3 {
				t.Fatalf("spawns/prompts=%s/%s", spawns, prompts)
			}
			entries, _ := os.ReadDir(profileDir)
			if len(entries) != 0 {
				t.Fatal("invocation replacement wrote native profile")
			}
		})
	}
}

func TestServerReplacementMismatchPreventsPrompt(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}, {Name: "AGENT_ADAPTOR_OPENCODE_IGNORE_AGENT_PROMPTS", Value: "1"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := d.Run(ctx, driver.Request{Instructions: &driver.InstructionsBundleRef{Content: "replacement", Mode: driver.InstructionModeReplace}}, &testutil.EventRecorder{})
	if err == nil || response.Checkpoint != nil || !strings.Contains(err.Error(), "not applied exactly") {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("prompt delivered after replacement mismatch")
	}
	if strings.Count(response.RawStreams.Stdout, "opencode server listening") != 2 {
		t.Fatal("failed replacement lost audit output")
	}
}

func TestReplacementPermissionEquivalence(t *testing.T) {
	decode := func(raw string) []any {
		var out []any
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	allowA := `{"permission":"external_directory","pattern":"/a/*","action":"allow"}`
	allowB := `{"permission":"external_directory","pattern":"/b/*","action":"allow"}`
	deny := `{"permission":"external_directory","pattern":"*","action":"deny"}`
	left := decode("[" + deny + "," + allowA + "," + allowB + "]")
	right := decode("[" + deny + "," + allowB + "," + allowA + "]")
	if !reflect.DeepEqual(canonicalPermissions(left), canonicalPermissions(right)) {
		t.Fatal("equivalent adjacent allow rules differ")
	}
	reordered := decode("[" + allowA + "," + deny + "," + allowB + "]")
	if reflect.DeepEqual(canonicalPermissions(left), canonicalPermissions(reordered)) {
		t.Fatal("deny precedence was erased")
	}
	changed := decode("[" + deny + "," + allowA + "," + strings.ReplaceAll(allowB, `"allow"`, `"ask"`) + "]")
	if reflect.DeepEqual(canonicalPermissions(left), canonicalPermissions(changed)) {
		t.Fatal("permission action change was hidden")
	}
	unknown := decode(`[{"permission":"*","pattern":"a","action":"allow","new_semantics":true},{"permission":"*","pattern":"b","action":"allow","new_semantics":true}]`)
	reversed := []any{unknown[1], unknown[0]}
	if reflect.DeepEqual(canonicalPermissions(unknown), canonicalPermissions(reversed)) {
		t.Fatal("unknown permission semantics were reordered")
	}
}

func TestServerReplacementFailureAndEmptyBase(t *testing.T) {
	for _, scenario := range []string{"empty-base", "unknown-agent", "malformed-agents", "after-prompt-failure"} {
		t.Run(scenario, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
			dir := filepath.Dir(capture)
			cfg := Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}}
			switch scenario {
			case "unknown-agent":
				cfg.Agent = "unavailable"
			case "malformed-agents":
				if err := os.WriteFile(filepath.Join(dir, "native-agents.json"), []byte(`[{"name":"broken","mode":"unexpected"}]`), 0600); err != nil {
					t.Fatal(err)
				}
			case "after-prompt-failure":
				cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "AGENT_ADAPTOR_OPENCODE_FAIL_AFTER_PROMPT", Value: "1"})
			}
			d := Driver(cfg)
			defer d.(configuredDriver).CloseProcesses(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			instructions := &driver.InstructionsBundleRef{Mode: driver.InstructionModeReplace, Content: "replacement"}
			if scenario == "empty-base" {
				instructions.Content = ""
				instructions.Path = filepath.Join(t.TempDir(), "empty.md")
				if err := os.WriteFile(instructions.Path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			response, err := d.Run(ctx, driver.Request{Instructions: instructions}, &testutil.EventRecorder{})
			spawns, _ := os.ReadFile(filepath.Join(dir, "spawns.log"))
			prompts, _ := os.ReadFile(filepath.Join(dir, "prompts.log"))
			if scenario == "empty-base" {
				if err != nil || response.Failure != nil {
					t.Fatalf("empty base: %v %+v", err, response.Failure)
				}
				var call struct{ Config string }
				raw, _ := os.ReadFile(capture)
				if err := json.Unmarshal(raw, &call); err != nil {
					t.Fatal(err)
				}
				var config struct {
					Agent map[string]struct{ Prompt string }
				}
				if err := json.Unmarshal([]byte(call.Config), &config); err != nil {
					t.Fatal(err)
				}
				if config.Agent["build"].Prompt != "\n" {
					t.Fatal("empty replacement restored native preamble")
				}
			} else if err == nil || response.Checkpoint != nil {
				t.Fatalf("failure not propagated: %+v %v", response, err)
			}
			wantSpawns, wantPrompts := 1, 0
			if scenario == "empty-base" || scenario == "after-prompt-failure" {
				wantSpawns, wantPrompts = 2, 1
			}
			if len(strings.Fields(string(spawns))) != wantSpawns || len(strings.Fields(string(prompts))) != wantPrompts {
				t.Fatalf("unexpected replay or delivery: spawns=%s prompts=%s", spawns, prompts)
			}
		})
	}
}
