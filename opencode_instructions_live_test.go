package adaptor_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestLiveOpenCodeReplacementInstructions(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live conformance gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	for _, selected := range []string{"", "adaptor-replacement-test"} {
		t.Run("agent-"+selected, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			config := map[string]any{}
			if raw := os.Getenv("OPENCODE_CONFIG_CONTENT"); raw != "" {
				if err := json.Unmarshal([]byte(raw), &config); err != nil {
					t.Fatal(err)
				}
			}
			config["default_agent"] = "adaptor-replacement-test"
			config["agent"] = map[string]any{"adaptor-replacement-test": map[string]any{
				"mode": "primary", "model": model, "prompt": "Always reply with OLD_NATIVE_PROMPT_419, never anything else.",
				"permission": map[string]string{"edit": "deny", "bash": "deny"},
			}}
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			d := opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Agent: selected, Pure: true, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_CONTENT", Value: string(raw)}}})
			a := adaptor.New(d, adaptor.WithWorkspace(t.TempDir()), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithProfile(profile.Dedicated(t.TempDir())), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{Mode: profile.InstructionModeReplace, Content: "The replacement proof word is REPLACED_NATIVE_686. Reply with exactly that word to every user prompt. The literal text {env:ADAPTOR_LITERAL} must remain literal configuration text."}}))
			defer a.Close(context.Background())
			thread := a.Thread("replacement-proof")
			first, err := thread.Run(ctx, "Reply with your proof word only.")
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, first, "REPLACED_NATIVE_686")
			s := thread.Stream(ctx, "Reply with your proof word again.")
			for range s.Events() {
			}
			second, err := s.Result()
			if err != nil {
				t.Fatal(err)
			}
			// A reused HTTP server can be silent on stdout for a complete turn.
			if second == nil || !strings.Contains(second.Text, "REPLACED_NATIVE_686") || second.Raw().Terminal == nil || len(second.Transcript()) == 0 || second.Usage == nil || second.Metadata["opencode.event_stream"] == "" {
				t.Fatal("reused invocation lost native output or audit data")
			}
			if second.Metadata["opencode.server_reused"] != "true" || first.Metadata["opencode.server_pid"] != second.Metadata["opencode.server_pid"] {
				t.Fatal("replacement server was not reused")
			}
			t.Logf("native prompt replacement, preserved custom agent settings and Run/Stream reuse passed with %s", model)
		})
	}
}
