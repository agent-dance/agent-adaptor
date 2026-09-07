package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/pelletier/go-toml/v2"
)

func TestAlignmentAppendCapabilityAndConflictPreflight(t *testing.T) {
	if !Driver(Config{}).Descriptor().SystemPrompt.Append {
		t.Fatal("native append is not advertised")
	}
	cfg := Config{CommonConfig: CommonConfig{ExtraArgs: []string{"-c", `"developer_instructions" = "secret"`}}}
	d := Driver(cfg)
	if err := d.(interface{ ValidateConfig(any) error }).ValidateConfig(nil); !errors.Is(err, driver.ErrSystemPromptUnsupported) {
		t.Fatalf("config conflict: %v", err)
	}
	if _, err := d.Run(context.Background(), driver.Request{}, nil); !errors.Is(err, driver.ErrSystemPromptUnsupported) {
		t.Fatalf("direct SPI conflict: %v", err)
	}
}

func alignmentCodexFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.Command("go", "build", "-o", path, "testdata/alignment-provider/main.go")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	return path
}
func alignmentCodexConfig(t *testing.T, command, scenario string) (Config, string) {
	t.Helper()
	home := t.TempDir()
	capture := filepath.Join(home, "capture.jsonl")
	return Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: "CODEX_HOME", Value: filepath.Join(home, "profile")}, {Name: "ALIGNMENT_CAPTURE", Value: capture}, {Name: "ALIGNMENT_SCENARIO", Value: scenario}}}}, capture
}
func alignmentCapture(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var v map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		result = append(result, v)
	}
	return result
}

func TestAlignmentExecAppendWireAndGuard(t *testing.T) {
	command := alignmentCodexFixture(t)
	for _, text := range []string{"", "甲\n\"乙\"\\\t\r\n🙂 ", strings.Repeat("x", 32768)} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("bytes-%d/resume-%v", len(text), resume), func(t *testing.T) {
				cfg, capture := alignmentCodexConfig(t, command, "")
				cfg.Model = "model"
				cfg.ExtraArgs = []string{"-c", `custom_key="unchanged"`}
				req := driver.Request{Config: cfg, Prompt: "original stdin", AppendSystemPrompt: text, Workspace: driver.WorkspaceLease{CWD: cfg.CWD}}
				if resume {
					req.Session = &driver.SessionContext{Mode: driver.SessionContinueOnly, State: &driver.SessionState{ResumeID: "thread-exec", Data: map[string]string{appendSystemPromptFingerprintKey: systemprompt.Fingerprint(text)}}}
				}
				sink := &testutil.EventRecorder{}
				result, err := (adapter{}).Run(context.Background(), req, sink)
				if err != nil || result.Failure != nil || result.Checkpoint == nil {
					t.Fatalf("run: %v %#v", err, result)
				}
				if result.Checkpoint.State.Data[appendSystemPromptFingerprintKey] != systemprompt.Fingerprint(text) {
					t.Fatal("missing fingerprint")
				}
				frames := alignmentCapture(t, capture)
				var args []string
				_ = json.Unmarshal(frames[0]["args"], &args)
				var stdin string
				_ = json.Unmarshal(frames[1]["stdin"], &stdin)
				if stdin != req.Prompt {
					t.Fatal("prompt modified")
				}
				found := false
				for i, a := range args {
					if a == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], "developer_instructions=") {
						var values map[string]string
						if err := toml.Unmarshal([]byte(args[i+1]), &values); err != nil || values["developer_instructions"] != text {
							t.Fatal("TOML roundtrip")
						}
						found = true
					}
				}
				if found != (text != "") {
					t.Fatal("empty override")
				}
				if resume && (len(args) < 3 || !reflect.DeepEqual(args[len(args)-3:], []string{"resume", "thread-exec", "-"})) {
					t.Fatalf("selector: %#v", args)
				}
				for _, e := range sink.Snapshot() {
					if e.Type == driver.RunEventInvocation {
						b, _ := json.Marshal(e)
						if text != "" && strings.Contains(string(b), strings.Repeat("x", 100)) {
							t.Fatal("diagnostic leaked text")
						}
						if strings.Contains(string(b), "甲") {
							t.Fatal("diagnostic leaked text")
						}
					}
				}
			})
		}
	}
	cfg, capture := alignmentCodexConfig(t, command, "")
	_, err := (adapter{}).Run(context.Background(), driver.Request{Config: cfg, AppendSystemPrompt: strings.Repeat("x", 32769)}, &testutil.EventRecorder{})
	var unsupported *driver.SystemPromptUnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Reason != "inline_limit" {
		t.Fatalf("limit: %v", err)
	}
	if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("limit spawned")
	}
	for _, mode := range []driver.SessionMode{driver.SessionContinueOnly, driver.SessionFork} {
		for _, old := range []string{"", systemprompt.Fingerprint("same"), systemprompt.Fingerprint("diff")} {
			req := driver.Request{AppendSystemPrompt: "same", Session: &driver.SessionContext{Mode: mode, State: &driver.SessionState{ResumeID: "id", Data: map[string]string{appendSystemPromptFingerprintKey: old}}}}
			err := validateCodexSessionGuard(req, "", "")
			if (err == nil) != (old == systemprompt.Fingerprint("same")) {
				t.Fatal("direct guard bypass")
			}
		}
	}
}

func TestAlignmentConfigConflictForms(t *testing.T) {
	for _, key := range []string{"developer_instructions", "instructions", "base_instructions", "model_instructions_file", "experimental_instructions_file"} {
		for _, args := range [][]string{{"-c", key + `="hidden"`}, {"-c" + key + `="hidden"`}, {"-c=" + key + `="hidden"`}, {"--config=" + key + `="hidden"`}, {"--config", ` "` + key + `" = "hidden"`}, {"-c", `'` + key + `' = "hidden"`}} {
			t.Run(strings.Join(args, "/"), func(t *testing.T) {
				if err := validateCodexPromptArgs(args); !errors.Is(err, driver.ErrSystemPromptUnsupported) || strings.Contains(err.Error(), "hidden") {
					t.Fatalf("unsafe conflict: %v", err)
				}
			})
		}
	}
	for _, args := range [][]string{{"-c"}, {"--config="}, {"-c", "not valid"}, {"-c", `x="unterminated`}} {
		var invalid *driver.InvalidDriverConfigError
		if err := validateCodexPromptArgs(args); !errors.As(err, &invalid) {
			t.Fatalf("invalid: %v", err)
		}
	}
	if err := validateCodexPromptArgs([]string{"--skip-git-repo-check", "-c", `'unrelated.key' = "value"`}); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentPersistentAppendAndCodec(t *testing.T) {
	first := persistentSpec{appendSystemPrompt: "甲"}
	same := first
	different := first
	different.appendSystemPrompt = "乙"
	empty := first
	empty.appendSystemPrompt = ""
	if first.sig() != same.sig() || first.sig() == different.sig() || first.sig() == empty.sig() || first.openOptions().AppendSystemPrompt != "甲" {
		t.Fatal("signature/handshake")
	}
	codec := sessionCodec{}
	if codec.FromParams(driver.SessionParams{}) != nil || len(codec.ToParams(nil).Values) != 0 {
		t.Fatal("nil mapping")
	}
	state := &driver.SessionState{ResumeID: "id", Data: map[string]string{appendSystemPromptFingerprintKey: systemprompt.Fingerprint("甲")}}
	params := codec.ToParams(state)
	copyState := codec.FromParams(params)
	params.Values[appendSystemPromptFingerprintKey] = "changed"
	if copyState.Data[appendSystemPromptFingerprintKey] != state.Data[appendSystemPromptFingerprintKey] {
		t.Fatal("codec aliases")
	}
	command := alignmentCodexFixture(t)
	cfg, capture := alignmentCodexConfig(t, command, "")
	agent := adaptor.New(Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithAppendSystemPrompt("same"))
	defer agent.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := agent.Thread("thread").Run(ctx, "prompt"); err != nil {
			t.Fatal(err)
		}
	}
	frames := alignmentCapture(t, capture)
	starts := 0
	for _, f := range frames {
		if string(f["event"]) == `"start"` {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("same append spawned %d", starts)
	}
	if _, err := agent.Thread("thread", adaptor.ResumeOnly()).Run(ctx, "prompt", adaptor.WithAppendSystemPrompt("diff")); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("incompatible: %v", err)
	}
	if _, err := agent.Thread("thread").Run(ctx, "prompt", adaptor.WithAppendSystemPrompt("")); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Thread("thread").Run(ctx, "prompt", adaptor.WithAppendSystemPrompt(""), adaptor.WithSpawn()); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Thread("thread").Run(ctx, "prompt", adaptor.WithAppendSystemPrompt("")); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(ctx); err != nil {
		t.Fatal(err)
	}
	frames = alignmentCapture(t, capture)
	active := 0
	prompts := 0
	for _, f := range frames {
		switch string(f["event"]) {
		case `"start"`:
			active++
			if string(f["previous_alive"]) != "0" {
				t.Fatal("overlapping writers")
			}
		case `"exit"`:
			active--
		}
		if string(f["Method"]) == `"turn/start"` || string(f["method"]) == `"turn/start"` {
			prompts++
		}
	}
	if prompts != 5 {
		t.Fatalf("active=%d prompts=%d", active, prompts)
	}
}

func TestAlignmentSkillInputSelection(t *testing.T) {
	skills := []driver.ResolvedSkill{{Key: "review", RuntimeName: "review", SourcePath: "/skills/review"}}
	if inputs := codexExplicitSkillInputs("catalog only", skills); len(inputs) != 0 {
		t.Fatal("auto activation")
	}
	if inputs := codexExplicitSkillInputs("Use $review, $unknown x$review $review.", skills); len(inputs) != 1 || inputs[0].Path != "/skills/review/SKILL.md" {
		t.Fatalf("inputs: %#v", inputs)
	}
	skills = append(skills, driver.ResolvedSkill{Key: "other", RuntimeName: "review", SourcePath: "/other"})
	if len(codexExplicitSkillInputs("$review", skills)) != 0 {
		t.Fatal("ambiguous name activated")
	}
}

func TestAlignmentPartialResultAndPublicEquivalence(t *testing.T) {
	command := alignmentCodexFixture(t)
	for _, scenario := range []string{"", "nonzero", "malformed", "cancel"} {
		for _, thread := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/thread-%v", scenario, thread), func(t *testing.T) {
				cfg, _ := alignmentCodexConfig(t, command, scenario)
				agent := adaptor.New(Driver(cfg), adaptor.WithThreadStore(memory.NewStore()))
				defer agent.Close(context.Background())
				var runner adaptor.Runner = agent
				if thread {
					runner = agent.Thread("partial")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				stream := runner.Stream(ctx, "original prompt")
				var seen bool
				for e := range stream.Events() {
					if text, ok := e.(adaptor.TextDelta); ok && text.Text != "" {
						seen = true
						if scenario == "cancel" {
							stream.Cancel()
						}
					}
				}
				result, err := stream.Result()
				if scenario != "" {
					var runErr *adaptor.RunError
					if result != nil || !errors.As(err, &runErr) || runErr.Result == nil {
						t.Fatalf("carrier: %v %#v", err, result)
					}
					result = runErr.Result
					if scenario == "cancel" && !errors.Is(err, context.Canceled) {
						t.Fatal("cause lost")
					}
					if thread {
						if _, err := agent.Thread("partial").Checkpoint(ctx); err == nil {
							t.Fatal("failed checkpoint persisted")
						}
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if result.Text != "answer" || !seen || !strings.Contains(result.Raw().Stdout, "answer") || len(result.Transcript()) == 0 {
					t.Fatalf("partial output lost: %#v", result)
				}
				if result.Metadata["transport"] != "app-server" {
					t.Fatal("partial response lost transport metadata")
				}
				if result.Raw().Stderr != "fixture-stderr" || result.Usage == nil || result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 2 {
					t.Fatal("partial response lost observed stderr/usage")
				}
				if scenario == "" {
					again, err := runner.Run(ctx, "original prompt")
					if err != nil {
						t.Fatal(err)
					}
					if again.Text != result.Text || again.Summary != result.Summary || !reflect.DeepEqual(again.Usage, result.Usage) || again.Raw().Terminal == nil || len(again.Transcript()) != len(result.Transcript()) {
						t.Fatal("Run/Stream output mismatch")
					}
				}
			})
		}
	}
}

func TestAlignmentExecHelperErrorPreservesAudit(t *testing.T) {
	command := alignmentCodexFixture(t)
	cfg, _ := alignmentCodexConfig(t, command, "stdin-error")
	result, err := (adapter{}).Run(context.Background(), driver.Request{Config: cfg, Prompt: strings.Repeat("x", 1<<20)}, &testutil.EventRecorder{})
	if err == nil || result.Output != "partial" || result.RawStreams == nil || result.RawStreams.Terminal == nil || result.RawStreams.Stderr != "partial-stderr" || len(result.Transcript) < 3 || result.Checkpoint != nil {
		t.Fatalf("helper error lost partial: err=%v output=%q", err, result.Output)
	}
}
