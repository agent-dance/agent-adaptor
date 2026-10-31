package pi

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

//go:embed testdata/happy.jsonl
var happy string

func TestProtocolProcess(t *testing.T) { testutil.ProtocolProcess(t) }

func TestRunProtocolContract(t *testing.T) {
	for _, tc := range []struct {
		name, output  string
		exit          int
		valid, failed bool
	}{
		{"success", happy, 0, true, false},
		{"nonzero", happy, 7, false, true},
		{"malformed", happy + "{broken\n", 0, false, true},
		{"missing-terminal", strings.Split(happy, `{"type":"agent_end"`)[0], 0, false, true},
		{"missing-checkpoint", strings.Join(strings.Split(happy, "\n")[1:], "\n"), 0, false, false},
		{"nested-checkpoint", strings.Replace(happy, `"id":"pi-session"`, `"metadata":{"id":"nested"}`, 1), 0, false, true},
		{"business-failure", strings.ReplaceAll(happy, `"stopReason":"stop"`, `"stopReason":"error","errorMessage":"provider failed"`), 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, _ := testutil.ProtocolCommand(t, tc.output, "stderr retained", tc.exit)
			d := Driver(Config{NoExtensions: true, Command: command, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: t.TempDir()}}})
			sink := &testutil.EventRecorder{}
			resp, err := d.Run(context.Background(), driver.Request{Prompt: "hello", Streaming: true, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Checkpoint != nil && resp.Checkpoint.Valid; got != tc.valid {
				t.Fatalf("checkpoint=%+v, failure=%+v", resp.Checkpoint, resp.Failure)
			}
			if (resp.Failure != nil) != tc.failed {
				t.Fatalf("failure=%+v", resp.Failure)
			}
			if resp.RawStreams == nil || resp.RawStreams.Stdout != tc.output || resp.RawStreams.Stderr != "stderr retained" {
				t.Fatalf("raw=%+v", resp.RawStreams)
			}
			if resp.Output != "OK" {
				t.Fatalf("text=%q", resp.Output)
			}
			var items []driver.TranscriptItem
			for _, event := range sink.Snapshot() {
				if event.Item != nil {
					items = append(items, *event.Item)
				}
			}
			if !reflect.DeepEqual(items, resp.Transcript) {
				t.Fatal("transcript differs from emitted items")
			}
			streams := sink.StreamSnapshot()
			for _, violation := range adaptertest.VerifyStreamSequence(streams) {
				t.Error(violation)
			}
			for _, violation := range adaptertest.VerifyResponse(&resp) {
				t.Error(violation)
			}
			for _, violation := range adaptertest.VerifyCheckpointCodec(d, &resp) {
				t.Error(violation)
			}
			if streams[0].Kind != driver.StreamRunStarted {
				t.Fatal("missing start")
			}
			last := streams[len(streams)-1]
			if tc.failed && last.Kind != driver.StreamRunError {
				t.Fatal("missing terminal failure")
			}
			if tc.valid && (resp.Usage == nil || resp.Usage.InputTokens != 5 || resp.RawStreams.Terminal.Event != "agent_end") {
				t.Fatalf("result=%+v", resp)
			}
		})
	}
}

func TestConfigAndInvocation(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, happy, "", 0)
	profile := t.TempDir()
	cwd := t.TempDir()
	cfg := Config{NoExtensions: true, Command: command, CWD: cwd, Provider: "test", Model: "default", Thinking: "low", Tools: []string{"read"}, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: profile}}}
	d := Driver(cfg)
	cfg.Tools[0] = "write"
	cfg.Env[0].Value = "changed"
	resp, err := d.Run(context.Background(), driver.Request{Prompt: "prompt\n--session untrusted", ModelOverride: "override", Instructions: &driver.InstructionsBundleRef{Content: "system instruction"}, Session: &driver.SessionContext{Mode: driver.SessionFork, State: &driver.SessionState{ResumeID: "parent"}}}, &testutil.EventRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Checkpoint == nil {
		t.Fatal("checkpoint absent")
	}
	raw, _ := os.ReadFile(capture)
	var call struct {
		Args        []string
		Prompt, CWD string
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(call.Args, "|")
	for _, want := range []string{"--model|override", "--tools|read", "--fork|parent", "--append-system-prompt|system instruction"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args=%v missing %s", call.Args, want)
		}
	}
	if call.Prompt != "prompt\n--session untrusted" || call.CWD != cwd {
		t.Fatalf("capture=%+v", call)
	}
	if err := d.ValidateConfig((*Config)(nil)); err == nil {
		t.Fatal("typed nil accepted")
	}
	if err := d.ValidateConfig(Config{Thinking: "invalid"}); err == nil {
		t.Fatal("invalid thinking accepted")
	}
}

func TestResumeRejectedBeforeLaunch(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	d := Driver(Config{NoExtensions: true, Command: filepath.Join(t.TempDir(), "missing")})
	_, err := d.Run(context.Background(), driver.Request{Session: &driver.SessionContext{State: &driver.SessionState{ResumeID: "parent", Data: map[string]string{driver.SessionParamCWD: "different"}}}}, &testutil.EventRecorder{})
	var rejected *engine.ResumeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestCancelHasRawAndNoCheckpoint(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	command, _ := testutil.ProtocolCommand(t, happy, "stderr", 0)
	t.Setenv("AGENT_ADAPTOR_PROTOCOL_HANG", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sink := testutil.NewChunkCancelRecorder(cancel, "stdout", "stderr")
	resp, err := Driver(Config{NoExtensions: true, Command: command}).Run(ctx, driver.Request{Prompt: "cancel", Streaming: true, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}}, sink)
	if resp.Checkpoint != nil || !errors.Is(err, context.Canceled) || resp.RawStreams == nil || resp.RawStreams.Stderr != "stderr" {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
}

func TestPiDriverConformance(t *testing.T) {
	profile := t.TempDir()
	cfg := Config{Model: "test-model", Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: profile}}}
	opts := []adaptertest.Option{adaptertest.WithConfig(cfg), adaptertest.WithExpectedDetectedModel("test-model"), adaptertest.WithRequiredConfigFields("command", "cwd", "model"), adaptertest.ExpectRejectForeignConfig(), adaptertest.WithSyncSkillsProbe(), adaptertest.SkipLiveRun("hermetic suite; live tests require two explicit environment gates")}
	opts = append(opts, adaptertest.WithGuardKeys(driver.SessionParamCWD, driver.SessionParamWorkspaceID, driver.SessionParamProfileFingerprint, "profile_dir", "native_config", "session_dir"))
	adaptertest.TestDriver(t, func() driver.Driver { return Driver(cfg) }, opts...)
}

func TestMalformedMessageLifecycleCannotCheckpoint(t *testing.T) {
	var messageEnd string
	for _, line := range strings.Split(happy, "\n") {
		if strings.Contains(line, `"type":"message_end"`) {
			messageEnd = line
		}
	}
	for name, raw := range map[string]string{
		"duplicate-end":         strings.Replace(happy, messageEnd, messageEnd+"\n"+messageEnd, 1),
		"missing-start":         strings.Replace(happy, `{"type":"message_start","message":{"role":"assistant","content":[]}}`, "", 1),
		"missing-agent-start":   strings.Replace(happy, `{"type":"agent_start"}`, "", 1),
		"duplicate-agent-start": strings.Replace(happy, `{"type":"agent_start"}`, "{\"type\":\"agent_start\"}\n{\"type\":\"agent_start\"}", 1),
		"invalid-content":       strings.Replace(happy, `"content":[{"type":"text","text":"OK"}]`, `"content":"invalid"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			p := newParser(&testutil.EventRecorder{}, true)
			p.lines.Write([]byte(raw), p.line)
			p.lines.Flush(p.line)
			resp := p.finish(clihelper.CommandResult{}, nil)
			if resp.Failure == nil || resp.Checkpoint != nil {
				t.Fatalf("malformed lifecycle accepted: %+v", resp)
			}
			if resp.Usage != nil && resp.Usage.InputTokens > 5 {
				t.Fatal("duplicate message usage counted twice")
			}
		})
	}
}

func FuzzParser(f *testing.F) {
	f.Add([]byte(happy))
	f.Add([]byte(toolPrefix + strings.Join(strings.Split(happy, "\n")[2:], "\n")))
	f.Add([]byte("{malformed\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p := newParser(&testutil.EventRecorder{}, true)
		for len(raw) > 0 {
			n := min(7, len(raw))
			p.lines.Write(raw[:n], p.line)
			raw = raw[n:]
		}
		p.lines.Flush(p.line)
		resp := p.finish(clihelper.CommandResult{}, nil)
		if p.malformed && resp.Checkpoint != nil {
			t.Fatal("malformed checkpoint")
		}
	})
}
