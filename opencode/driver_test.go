package opencode

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
		{"nonzero", happy, 5, false, true},
		{"malformed", happy + "{broken\n", 0, false, true},
		{"missing-terminal", strings.Split(happy, `{"type":"step_finish"`)[0], 0, false, true},
		{"missing-checkpoint", strings.ReplaceAll(happy, `"sessionID":"ses_child",`, ""), 0, false, true},
		{"nested-checkpoint", strings.ReplaceAll(happy, `"sessionID":"ses_child",`, `"metadata":{"sessionID":"ses_child"},`), 0, false, true},
		{"business-failure", happy + `{"type":"error","sessionID":"ses_child","error":{"name":"APIError","data":{"message":"provider failed"}}}` + "\n", 0, false, true},
		{"incomplete-tools", strings.Replace(happy, `"reason":"stop"`, `"reason":"tool-calls"`, 1), 0, false, true},
		{"mixed-sessions", happy + strings.ReplaceAll(happy, "ses_child", "ses_other"), 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, _ := testutil.ProtocolCommand(t, tc.output, "stderr retained", tc.exit)
			d := Driver(Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			sink := &testutil.EventRecorder{}
			resp, err := d.Run(context.Background(), driver.Request{Prompt: "hello", Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}}, sink)
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
			var items []driver.TranscriptItem
			for _, event := range sink.Snapshot() {
				if event.Item != nil {
					items = append(items, *event.Item)
				}
			}
			if !reflect.DeepEqual(items, resp.Transcript) {
				t.Fatal("transcript differs from emitted items")
			}
			for _, violation := range adaptertest.VerifyResponse(&resp) {
				t.Error(violation)
			}
			for _, violation := range adaptertest.VerifyCheckpointCodec(d, &resp) {
				t.Error(violation)
			}
			if tc.valid && (resp.Output != "OK" || resp.Usage == nil || resp.Usage.InputTokens != 5 || resp.RawStreams.Terminal.Event != "step_finish") {
				t.Fatalf("result=%+v", resp)
			}
		})
	}
}

func TestResourcesAndInvocation(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, happy, "", 0)
	profile, cwd, skill := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("skill"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Command: command, CWD: cwd, Model: "test/default", Agent: "build", Variant: "low", Pure: true, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: profile}, {Name: "OPENCODE_CONFIG_CONTENT", Value: `{"provider":{"custom":{}},"instructions":["existing.md"],"mcp":{"external":{"type":"remote","url":"http://external"}}}`}}}
	d := Driver(cfg)
	cfg.Env[0].Value = "changed"
	req := driver.Request{Prompt: "prompt\n--session untrusted", ModelOverride: "test/override", Instructions: &driver.InstructionsBundleRef{Content: "system instruction"}, Skills: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "skill", SourcePath: skill}}}, Session: &driver.SessionContext{Mode: driver.SessionFork, State: &driver.SessionState{ResumeID: "parent"}}}
	resp, err := d.Run(context.Background(), req, &testutil.EventRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Checkpoint == nil {
		t.Fatal("checkpoint absent")
	}
	raw, _ := os.ReadFile(capture)
	var call struct {
		Args                []string
		Prompt, CWD, Config string
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(call.Args, "|")
	for _, want := range []string{"--model|test/override", "--session|parent", "--fork", "--thinking", "--pure"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args=%v missing %s", call.Args, want)
		}
	}
	if call.Prompt != req.Prompt || call.CWD != cwd {
		t.Fatalf("capture=%+v", call)
	}
	var overlay map[string]any
	if err := json.Unmarshal([]byte(call.Config), &overlay); err != nil {
		t.Fatal(err)
	}
	if len(overlay["mcp"].(map[string]any)) != 1 || overlay["provider"] == nil {
		t.Fatalf("overlay=%v", overlay)
	}
	instructions := overlay["instructions"].([]any)
	if len(instructions) != 2 {
		t.Fatal("instructions lost")
	}
	if _, err := os.Stat(instructions[1].(string)); !os.IsNotExist(err) {
		t.Fatal("temporary instructions leaked")
	}
	if entries, _ := os.ReadDir(profile); len(entries) != 0 {
		t.Fatal("shared profile was modified")
	}
	if err := d.ValidateConfig((*Config)(nil)); err == nil {
		t.Fatal("typed nil accepted")
	}
	if err := d.ValidateConfig(Config{Model: "no-provider"}); err == nil {
		t.Fatal("invalid model accepted")
	}
}

func TestResumeRejectedBeforeLaunch(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	d := Driver(Config{Command: filepath.Join(t.TempDir(), "missing")})
	_, err := d.Run(context.Background(), driver.Request{Session: &driver.SessionContext{State: &driver.SessionState{ResumeID: "parent", Data: map[string]string{driver.SessionParamCWD: "different"}}}}, &testutil.EventRecorder{})
	var rejected *engine.ResumeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestCancelHasRawAndNoCheckpoint(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	command, _ := testutil.ProtocolCommand(t, happy, "stderr", 0)
	t.Setenv("AGENT_ADAPTOR_PROTOCOL_HANG", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sink := testutil.NewChunkCancelRecorder(cancel, "stdout", "stderr")
	resp, err := Driver(Config{Command: command}).Run(ctx, driver.Request{Prompt: "cancel", Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}}, sink)
	if resp.Checkpoint != nil || resp.Failure == nil || resp.RawStreams == nil || resp.RawStreams.Stderr != "stderr" {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
}

func TestOpenCodeDriverConformance(t *testing.T) {
	cfg := Config{Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}}
	opts := []adaptertest.Option{adaptertest.WithConfig(cfg), adaptertest.WithExpectedDetectedModel("test/model"), adaptertest.WithRequiredConfigFields("command", "cwd", "model"), adaptertest.ExpectRejectForeignConfig(), adaptertest.WithSyncSkillsProbe(), adaptertest.SkipLiveRun("hermetic suite; live tests require two explicit environment gates")}
	opts = append(opts, adaptertest.WithGuardKeys(driver.SessionParamCWD, driver.SessionParamWorkspaceID, driver.SessionParamProfileFingerprint, "profile_dir", "native_config", "native_overlay", "data_home"))
	adaptertest.TestDriver(t, func() driver.Driver { return Driver(cfg) }, opts...)
}

func TestEmptyFinalMessageDoesNotRepeatEarlierCommentary(t *testing.T) {
	first := strings.Replace(happy, `"reason":"stop"`, `"reason":"tool-calls"`, 1)
	second := strings.ReplaceAll(happy, `"messageID":"message"`, `"messageID":"final"`)
	second = strings.ReplaceAll(second, `"id":"`, `"id":"final-`)
	second = strings.Replace(second, `"text":"OK"`, `"text":""`, 1)
	p := newParser(&testutil.EventRecorder{}, true)
	p.lines.Write([]byte(first+second), p.line)
	p.lines.Flush(p.line)
	resp := p.finish(clihelper.CommandResult{}, nil)
	if resp.Failure != nil || resp.Output != "" || resp.Checkpoint == nil {
		t.Fatalf("response=%+v", resp)
	}
}
func FuzzParser(f *testing.F) {
	f.Add([]byte(happy))
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
