package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/tool"
)

// Both gates are mandatory even when a CLI and credentials are installed.
// Profiles, API credentials, CLI paths and models come from the operator's
// environment; no endpoint or key is stored in repository fixtures.
func TestLivePiAndOpenCode(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 and a provider-specific live gate")
	}
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			prefix := "AGENT_ADAPTOR_" + strings.ToUpper(name) + "_"
			if os.Getenv(prefix+"LIVE") != "1" {
				t.Skip("set " + prefix + "LIVE=1")
			}
			command, model := os.Getenv(prefix+"COMMAND"), os.Getenv(prefix+"MODEL")
			if model == "" {
				t.Fatal(prefix + "MODEL must select an explicit live model")
			}
			workspace := t.TempDir()
			var makeDriver func() driver.Driver
			if name == "pi" {
				makeDriver = func() driver.Driver {
					return pi.Driver(pi.Config{Command: command, Provider: os.Getenv(prefix + "PROVIDER"), Model: model, Thinking: "low", NoExtensions: true, NoSkills: true, Tools: []string{"read"}})
				}
			} else {
				makeDriver = func() driver.Driver {
					return opencode.Driver(opencode.Config{Command: command, Model: model, Pure: true})
				}
			}
			t.Run("adaptertest", func(t *testing.T) {
				adaptertest.TestDriver(t, makeDriver, adaptertest.WithWorkspace(workspace), adaptertest.WithLiveRun("Reply with exactly OK."))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			a := adaptor.New(makeDriver(), adaptor.WithWorkspace(workspace), adaptor.WithThreadStore(memory.NewStore()))
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			parent := a.Thread("live/parent:opaque")
			res, err := parent.Run(ctx, "Remember the secret test word COBALT71. Reply only with that word.")
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, res, "COBALT71")
			checkpoint, err := parent.Checkpoint(ctx)
			if err != nil || checkpoint == nil || !checkpoint.Valid {
				t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
			}
			branch := parent.Fork("live/branch:opaque")
			res, err = branch.Run(ctx, "Change the remembered test word to AMBER82. Reply only with AMBER82.")
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, res, "AMBER82")
			branchCheckpoint, err := branch.Checkpoint(ctx)
			if err != nil || branchCheckpoint == nil || branchCheckpoint.State.ResumeID == checkpoint.State.ResumeID {
				t.Fatalf("fork checkpoint=%+v err=%v", branchCheckpoint, err)
			}
			s := parent.Stream(ctx, "What test word did I ask you to remember? Reply only with that word.", adaptor.WithSpawn())
			if s.RunID() == "" {
				t.Fatal("RunID unavailable")
			}
			events := 0
			for range s.Events() {
				events++
			}
			res, err = s.Result()
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, res, "COBALT71")
			if events == 0 {
				t.Fatal("no events")
			}
			if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("TOOL_MARKER_93"), 0600); err != nil {
				t.Fatal(err)
			}
			res, err = a.Run(ctx, "Read marker.txt with your file-reading tool. Reply with its exact contents.")
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, res, "TOOL_MARKER_93")
			toolResult := false
			for _, item := range res.Transcript() {
				if item.Kind == driver.TranscriptToolResult {
					toolResult = true
				}
			}
			if !toolResult {
				t.Fatal("native tool result missing from Transcript")
			}
			type answer struct {
				Value string `json:"value"`
			}
			res, err = a.Run(ctx, "Return the word from the instructions in the value property. The value must be only that word, with no braces or quotes inside it.", adaptor.WithInstructions("Set the value property to the literal word INSTRUCTION_64."), adaptor.WithSchema[answer]())
			if err != nil {
				t.Fatal(err)
			}
			var decoded answer
			if err := res.Decode(&decoded); err != nil || decoded.Value != "INSTRUCTION_64" {
				t.Fatalf("decoded=%+v err=%v terminal=%s", decoded, err, res.Raw().Terminal.JSON)
			}
			{
				var calls atomic.Int32
				definition := tool.Define("lookup_marker", "Return the test marker. Always call this tool when asked for the hosted marker.", func(context.Context, struct{}) (answer, error) { calls.Add(1); return answer{Value: "HOSTED_57"}, nil })
				withTool := adaptor.New(makeDriver(), adaptor.WithWorkspace(workspace), adaptor.WithTools(definition), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
				defer withTool.Close(context.Background())
				res, err = withTool.Run(ctx, "Call lookup_marker to obtain the hosted marker and reply with its exact value.")
				if err != nil {
					t.Fatal(err)
				}
				assertLiveResult(t, res, "HOSTED_57")
				if calls.Load() == 0 {
					t.Fatal("hosted Tool was not actually called")
				}
			}
			t.Logf("%s: conformance, resume, fork isolation, Stream, native tool, instructions and schema passed with %s", name, model)
		})
	}
}
func assertLiveResult(t *testing.T, res *adaptor.Result, want string) {
	t.Helper()
	if res == nil || !strings.Contains(res.Text, want) {
		t.Fatalf("expected %q in result: %+v", want, res)
	}
	if res.Raw().Stdout == "" || res.Raw().Terminal == nil || len(res.Transcript()) == 0 || res.Usage == nil {
		t.Fatalf("incomplete audit result: %+v", res)
	}
}

func TestLivePiAndOpenCodeSkills(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("live conformance gate is disabled")
	}
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			prefix := "AGENT_ADAPTOR_" + strings.ToUpper(name) + "_"
			if os.Getenv(prefix+"LIVE") != "1" {
				t.Skip("provider live gate is disabled")
			}
			model := os.Getenv(prefix + "MODEL")
			if model == "" {
				t.Fatal("an explicit live model is required")
			}
			var d driver.Driver
			if name == "pi" {
				d = pi.Driver(pi.Config{Command: os.Getenv(prefix + "COMMAND"), Provider: os.Getenv(prefix + "PROVIDER"), Model: model, NoExtensions: true, NoSkills: true, Tools: []string{"read"}})
			} else {
				d = opencode.Driver(opencode.Config{Command: os.Getenv(prefix + "COMMAND"), Model: model, Pure: true})
			}
			a := adaptor.New(d, adaptor.WithWorkspace(t.TempDir()))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			definition := skill.Inline("sdk-live-proof", "---\nname: sdk-live-proof\ndescription: Return the hidden proof word for the SDK live skill test.\n---\nWhen invoked, respond with exactly SKILL_PROOF_42.\n")
			result, err := a.Run(ctx, "Use the sdk-live-proof skill and reply with its proof word only.", adaptor.WithSkills(definition))
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, "SKILL_PROOF_42")
		})
	}
}
