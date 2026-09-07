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
	"sync"
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

// Embed the concrete configured driver so all optional interfaces, including
// persistent lifecycle and session fingerprinting, remain the real ones. Run
// calls the provider once and snapshots that same Response before core maps it.
type alignmentResponseDriver struct {
	configuredDriver
	mu              sync.Mutex
	snapshots       []alignmentPublicAudit
	causes          []error
	cancelAfterText context.CancelFunc
}

type alignmentPublicAudit struct {
	Text, Summary, Provider, Model              string
	Stdout, Stderr, TerminalEvent, TerminalJSON string
	Usage                                       string
	Transcript                                  string
	Services                                    string
}

func alignmentAuditJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}
func alignmentAudit(text, summary, provider, model string, raw driver.RawStreams, usage *driver.Usage, transcript []driver.TranscriptItem, services []driver.RuntimeServiceReport) alignmentPublicAudit {
	// Core collects service reports into an empty list when none were observed.
	// Compare the entire report sequence; nil and empty both contain zero reports.
	audit := alignmentPublicAudit{Text: text, Summary: summary, Provider: provider, Model: model, Stdout: raw.Stdout, Stderr: raw.Stderr, Usage: alignmentAuditJSON(usage), Transcript: alignmentAuditJSON(transcript), Services: alignmentAuditJSON(append([]driver.RuntimeServiceReport{}, services...))}
	if raw.Terminal != nil {
		audit.TerminalEvent, audit.TerminalJSON = raw.Terminal.Event, string(raw.Terminal.JSON)
	}
	return audit
}

type alignmentCancelTextSink struct {
	driver.EventSink
	cancel context.CancelFunc
}

func (s alignmentCancelTextSink) EmitStream(payload driver.StreamPayload) error {
	err := s.EventSink.EmitStream(payload)
	if payload.Kind == driver.StreamTextContent && payload.Delta != "" {
		s.cancel()
	}
	return err
}

func (d *alignmentResponseDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	cancel := d.cancelAfterText
	d.mu.Unlock()
	if cancel != nil {
		sink = alignmentCancelTextSink{EventSink: sink, cancel: cancel}
	}
	response, err := d.configuredDriver.Run(ctx, req, sink)
	var raw driver.RawStreams
	if response.RawStreams != nil {
		raw = *response.RawStreams
	}
	snapshot := alignmentAudit(response.Output, response.Summary, response.Provider, response.Model, raw, response.Usage, response.Transcript, response.RuntimeServices)
	d.mu.Lock()
	d.snapshots = append(d.snapshots, snapshot)
	d.causes = append(d.causes, err)
	d.mu.Unlock()
	return response, err
}
func alignmentResultAudit(result *adaptor.Result) alignmentPublicAudit {
	return alignmentAudit(result.Text, result.Summary, result.Provider, result.Model, result.Raw(), result.Usage, result.Transcript(), result.Services())
}
func (d *alignmentResponseDriver) assertMapped(t *testing.T, index int, result *adaptor.Result, err error) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.snapshots) != index+1 {
		t.Fatalf("expected exactly %d real Driver calls; got %d", index+1, len(d.snapshots))
	}
	if got := alignmentResultAudit(result); got != d.snapshots[index] {
		t.Fatalf("same Response -> Result mismatch:\nDriver: %#v\nPublic: %#v", d.snapshots[index], got)
	}
	if cause := d.causes[index]; cause != nil && !errors.Is(err, cause) {
		t.Fatalf("Driver cause lost: driver=%v public=%v", cause, err)
	}
}

func TestAlignmentPartialResultAndPublicEquivalence(t *testing.T) {
	command := alignmentCodexFixture(t)
	for _, scenario := range []string{"", "nonzero", "malformed", "cancel"} {
		for _, thread := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/thread-%v", scenario, thread), func(t *testing.T) {
				cfg, _ := alignmentCodexConfig(t, command, scenario)
				recordingDriver := &alignmentResponseDriver{configuredDriver: Driver(cfg).(configuredDriver)}
				agent := adaptor.New(recordingDriver, adaptor.WithThreadStore(memory.NewStore()))
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
				recordingDriver.assertMapped(t, 0, result, err)
				// Exact admitted resident stderr is established by the real-child source
				// oracle TestAlignmentCodexStderrAdmission. This layer checks the same
				// Response byte for byte, without presuming receipt across two pipes.
				// One-shot and failed-process EOF/drain paths retain their exact oracle.
				if (!thread || scenario != "") && result.Raw().Stderr != "fixture-stderr" {
					t.Fatalf("drained stderr: got %q (%d bytes); usage=%#v", result.Raw().Stderr, len(result.Raw().Stderr), result.Usage)
				}
				if result.Usage == nil || result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 2 {
					t.Fatalf("formal usage: got %#v; stderr=%q", result.Usage, result.Raw().Stderr)
				}
				if scenario == "" {
					terminal := result.Raw().Terminal
					if terminal == nil || terminal.Event != "turn/completed" || !strings.Contains(string(terminal.JSON), `"status":"completed"`) || !strings.Contains(result.Raw().Stdout, string(terminal.JSON)) {
						t.Fatalf("formal terminal: %+v", terminal)
					}
				}
				published := alignmentResultAudit(result)
				repeated, repeatedErr := stream.Result()
				if scenario != "" {
					var runErr *adaptor.RunError
					if !errors.As(repeatedErr, &runErr) {
						t.Fatalf("repeat cause: %v", repeatedErr)
					}
					repeated = runErr.Result
				}
				if repeated == nil || alignmentResultAudit(repeated) != published || !errors.Is(repeatedErr, err) {
					t.Fatal("repeated Result changed published data/cause")
				}
				if scenario == "" {
					again, err := runner.Run(ctx, "original prompt")
					if err != nil {
						t.Fatal(err)
					}
					recordingDriver.assertMapped(t, 1, again, err)
					if alignmentResultAudit(result) != published {
						t.Fatal("next Run changed prior Stream.Result")
					}
					if !thread && again.Raw().Stderr != "fixture-stderr" {
						t.Fatalf("second one-shot stderr: %q", again.Raw().Stderr)
					}
					if again.Text != result.Text || again.Summary != result.Summary || !reflect.DeepEqual(again.Usage, result.Usage) || again.Raw().Terminal == nil || len(again.Transcript()) != len(result.Transcript()) {
						t.Fatal("Run/Stream output mismatch")
					}
				} else {
					runCtx, runCancel := context.WithCancel(ctx)
					defer runCancel()
					if scenario == "cancel" {
						recordingDriver.mu.Lock()
						recordingDriver.cancelAfterText = runCancel
						recordingDriver.mu.Unlock()
					}
					again, runErr := runner.Run(runCtx, "original prompt")
					var carrier *adaptor.RunError
					if again != nil || !errors.As(runErr, &carrier) || carrier.Result == nil {
						t.Fatalf("Run partial carrier: result=%#v err=%v", again, runErr)
					}
					again = carrier.Result
					recordingDriver.assertMapped(t, 1, again, runErr)
					if again.Text != result.Text || again.Summary != result.Summary || !reflect.DeepEqual(again.Usage, result.Usage) || again.Raw().Stderr != "fixture-stderr" || len(again.Transcript()) == 0 || again.Metadata["transport"] != "app-server" || again.Raw().Terminal != nil {
						t.Fatalf("Run partial audit: raw=%+v usage=%#v text=%q", again.Raw(), again.Usage, again.Text)
					}
					if scenario == "cancel" && !errors.Is(runErr, context.Canceled) {
						t.Fatalf("Run cancellation cause: %v", runErr)
					}
					if thread {
						if _, checkpointErr := agent.Thread("partial").Checkpoint(ctx); checkpointErr == nil {
							t.Fatal("failed Run checkpoint persisted")
						}
					}
					if alignmentResultAudit(result) != published {
						t.Fatal("failed Run changed prior partial Stream.Result")
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
