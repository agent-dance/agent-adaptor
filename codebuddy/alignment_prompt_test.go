package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

func alignmentRecordArgs() {
	if path := os.Getenv("ALIGNMENT_CODEBUDDY_ARGV"); path != "" {
		raw, _ := json.Marshal(os.Args[1:])
		appendCodeBuddyPersistentHelperLine(path, string(raw))
	}
}
func alignmentReplayObservation(writer *bufio.Writer) bool {
	path := os.Getenv("ALIGNMENT_CODEBUDDY_PROTOCOL")
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		os.Exit(97)
	}
	if marker := os.Getenv("ALIGNMENT_CODEBUDDY_CATALOG_VERIFIED"); marker != "" {
		root := os.Getenv("CODEBUDDY_CONFIG_DIR")
		for path, want := range map[string]string{
			filepath.Join("agents", alignmentCatalogAgentName+".md"):       "SUBAGENT_COMPLETED",
			filepath.Join("skills", alignmentCatalogSkillName, "SKILL.md"): "SKILL_ACTIVATED",
		} {
			content, err := os.ReadFile(filepath.Join(root, path))
			if err != nil || !strings.Contains(string(content), want) {
				os.Exit(96)
			}
		}
		if err := os.WriteFile(marker, []byte("materialized catalog verified"), 0600); err != nil {
			os.Exit(95)
		}
	}
	_, _ = writer.Write(raw)
	_ = writer.Flush()
	return true
}
func alignmentArgs(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}
func alignmentArg(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}
func TestAlignmentAppendValidationAndWindows(t *testing.T) {
	for _, flag := range []string{"--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file"} {
		for _, extra := range [][]string{{flag, "secret"}, {flag + "=secret"}} {
			d := Driver(Config{CommonConfig: CommonConfig{ExtraArgs: extra}})
			if err := d.ValidateConfig(nil); !errors.Is(err, driver.ErrSystemPromptUnsupported) {
				t.Fatalf("config flag %s error=%v", flag, err)
			}
			rec := &testutil.EventRecorder{}
			_, err := d.Run(context.Background(), driver.Request{AppendSystemPrompt: "", Prompt: "prompt"}, rec)
			if !errors.Is(err, driver.ErrSystemPromptUnsupported) || len(rec.Snapshot()) != 0 {
				t.Fatal("conflict must fail before resource/spawn")
			}
		}
	}
	if err := systemprompt.ValidateInline(DriverType, strings.Repeat("界", 10922)+"aa"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ text, reason string }{{strings.Repeat("a", 32769), "inline_limit"}, {"a\x00", "nul_byte"}, {string([]byte{0xff}), "invalid_utf8"}} {
		_, err := Driver(Config{}).Run(context.Background(), driver.Request{AppendSystemPrompt: tc.text}, &testutil.EventRecorder{})
		var unsupported *driver.SystemPromptUnsupportedError
		if !errors.As(err, &unsupported) || unsupported.Reason != tc.reason {
			t.Fatalf("reason=%v want %s", err, tc.reason)
		}
	}
	for _, tc := range []struct {
		command string
		args    []string
		reason  string
	}{
		{`C:\codebuddy.exe`, []string{"--append-system-prompt", "甲\n\"乙\"😀"}, ""},
		{`C:\` + strings.Repeat("x", 1200) + `\codebuddy.exe`, []string{"--append-system-prompt", strings.Repeat("a", 32000)}, "command_line_limit"},
		{`C:\codebuddy.exe`, []string{"--append-system-prompt", strings.Repeat(`\"`, 12000)}, "command_line_limit"},
		{"cmd.exe", []string{"/d", "/s", "/c", "call", `C:\codebuddy.cmd`, "--append-system-prompt", strings.Repeat("a", 8190)}, "command_line_limit"},
		{"cmd.exe", []string{"/d", "/s", "/c", "call", `C:\codebuddy.cmd`, "--append-system-prompt", "a&b"}, "unsafe_shell_argument"},
	} {
		err := systemprompt.ValidateCommandLine(DriverType, tc.command, tc.args, "windows")
		var unsupported *driver.SystemPromptUnsupportedError
		if tc.reason == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.As(err, &unsupported) || unsupported.Reason != tc.reason {
			t.Fatalf("Windows validation=%v want %s", err, tc.reason)
		}
	}
}
func TestAlignmentAppendPersistentPrewarmAndClear(t *testing.T) {
	const text = "APPEND_T15_NONCE_91"
	argv := filepath.Join(t.TempDir(), "argv")
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_ARGV", Value: argv}, {Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}}, adaptor.WithAppendSystemPrompt(text))
	defer fx.close()
	fx.run(t, "one")
	fx.run(t, "two")
	schema := adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`))
	fx.run(t, "schema", schema)
	if fx.waitSpawnCount(t, 3) != 3 {
		t.Fatal("persistent/schema/prewarm must use three processes")
	}
	fx.run(t, "after schema")
	if fx.spawnCount(t) != 3 || fx.overlapCount(t) != 0 {
		t.Fatal("prewarm reuse or single writer lost")
	}
	for _, args := range alignmentArgs(t, argv) {
		if alignmentArg(args, "--append-system-prompt") != text {
			t.Fatal("actual process lost append")
		}
	}
	checkpoint, err := fx.thread.Checkpoint(context.Background())
	if err != nil || checkpoint.State.Data[appendSystemPromptFingerprintKey] != systemprompt.Fingerprint(text) {
		t.Fatalf("checkpoint=%#v err=%v", checkpoint, err)
	}
	stream := fx.thread.Stream(context.Background(), "diagnostic")
	for event := range stream.Events() {
		if e, ok := event.(adaptor.Notice); ok {
			raw, _ := json.Marshal(e)
			if strings.Contains(string(raw), text) {
				t.Fatal("append leaked in SDK diagnostic")
			}
		}
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	fx.run(t, "fresh", adaptor.WithSpawn())
	if fx.spawnCount(t) != 4 {
		t.Fatal("WithSpawn did not launch one process")
	}
	fx.run(t, "again")
	if fx.spawnCount(t) != 5 {
		t.Fatal("WithSpawn was retained as writer")
	}
	fx.run(t, "clear", adaptor.WithAppendSystemPrompt(""))
	if fx.spawnCount(t) != 6 || fx.overlapCount(t) != 0 {
		t.Fatal("clear did not hand off")
	}
	last := alignmentArgs(t, argv)
	if alignmentArg(last[len(last)-1], "--append-system-prompt") != "" {
		t.Fatal("clear argv kept append")
	}
}
func TestAlignmentAppendThreadAndDirectGuards(t *testing.T) {
	store := memory.NewStore()
	argv := filepath.Join(t.TempDir(), "argv")
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_ARGV", Value: argv}, {Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}}, adaptor.WithThreadStore(store), adaptor.WithAppendSystemPrompt("first"))
	defer fx.close()
	fx.run(t, "healthy")
	before, _ := store.Resolve(context.Background(), threadstore.Query{Key: "persistent-test/thread"})
	_, err := fx.agent.Thread("persistent-test/thread", adaptor.ResumeOnly()).Run(context.Background(), "change", adaptor.WithAppendSystemPrompt("second"))
	if !errors.Is(err, adaptor.ErrThreadIncompatible) || fx.spawnCount(t) != 1 {
		t.Fatalf("ResumeOnly drift=%v", err)
	}
	after, _ := store.Resolve(context.Background(), threadstore.Query{Key: "persistent-test/thread"})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("incompatible changed healthy record")
	}
	checkpoint, _ := fx.thread.Checkpoint(context.Background())
	codec := sessionCodec{}
	state := codec.FromParams(codec.ToParams(checkpoint.State))
	if !reflect.DeepEqual(state, checkpoint.State) {
		t.Fatal("codec lost append")
	}
	for _, text := range []string{"", "second"} {
		rec := &testutil.EventRecorder{}
		_, err := Driver(Config{}).Run(context.Background(), driver.Request{AppendSystemPrompt: text, Session: &driver.SessionContext{State: state}}, rec)
		if err == nil || len(rec.Snapshot()) != 0 {
			t.Fatal("direct guard allowed drift")
		}
	}
	base := persistentSpec{appendSystemPrompt: "first", env: []driver.EnvBinding{{Name: "A", Value: "B"}}}
	changed := base
	changed.appendSystemPrompt = "other"
	if base.sig() == changed.sig() {
		t.Fatal("same-length append missing from process signature")
	}
	changed = base
	changed.env = []driver.EnvBinding{{Name: "A", Value: "C"}}
	if base.sig() == changed.sig() {
		t.Fatal("original environment signature lost")
	}
	fx.run(t, "safe replacement", adaptor.WithAppendSystemPrompt("second"))
	if fx.spawnCount(t) != 2 || fx.overlapCount(t) != 0 {
		t.Fatal("default drift replacement failed")
	}
}

func TestAlignmentAppendActualStatelessOverrideAndDiagnostics(t *testing.T) {
	const original = "T15_DEFAULT_APPEND"
	argv := filepath.Join(t.TempDir(), "argv")
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_ARGV", Value: argv}, {Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}}, adaptor.WithAppendSystemPrompt(original))
	defer fx.close()
	texts := []string{original, " 甲\n\"乙\"😀 ", ""}
	for i, text := range texts {
		opts := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove}})}
		if i != 0 {
			opts = append(opts, adaptor.WithAppendSystemPrompt(text))
		}
		stream := fx.agent.Stream(context.Background(), "original user prompt", opts...)
		for event := range stream.Events() {
			if notice, ok := event.(adaptor.Notice); ok && notice.Kind == adaptor.NoticeInvocation {
				args, _ := notice.Data["args"].([]string)
				if text != "" && alignmentArg(args, "--append-system-prompt") != "[redacted]" {
					t.Fatal("diagnostic copy was not redacted")
				}
			}
		}
		if _, err := stream.Result(); err != nil {
			t.Fatal(err)
		}
	}
	rows := alignmentArgs(t, argv)
	if len(rows) != 3 {
		t.Fatalf("actual spawns=%d", len(rows))
	}
	for i, row := range rows {
		if alignmentArg(row, "--append-system-prompt") != texts[i] {
			t.Fatalf("actual argv lost exact text at %d", i)
		}
	}
	// Direct batch transport exercises the process helper's invocation path too.
	command, _ := os.Executable()
	home := t.TempDir()
	rec := &testutil.EventRecorder{}
	req := driver.Request{RunID: "batch-append", Prompt: "original user prompt", AppendSystemPrompt: original, Config: Config{CommonConfig: CommonConfig{Command: command, Env: []driver.EnvBinding{{Name: codeBuddyPersistentHelperEnv, Value: "1"}, {Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: filepath.Join(home, "profile")}}}}, Workspace: driver.WorkspaceLease{CWD: home}, Policy: autoApprovePolicy()}
	response, err := (adapter{}).Run(context.Background(), req, rec)
	if err != nil || response.Checkpoint == nil {
		t.Fatalf("batch response err=%v", err)
	}
	for _, e := range rec.Snapshot() {
		if e.Type == driver.RunEventInvocation {
			args, _ := e.Data["args"].([]string)
			if alignmentArg(args, "--append-system-prompt") != "[redacted]" || args[len(args)-1] != req.Prompt {
				t.Fatal("batch diagnostic or user prompt changed")
			}
		}
	}
}
