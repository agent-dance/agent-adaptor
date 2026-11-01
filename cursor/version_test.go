package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
)

func versionCommand(t *testing.T, output, body string, windowsBodies ...string) string {
	t.Helper()
	windowsBody := body
	if len(windowsBodies) > 0 {
		windowsBody = windowsBodies[0]
	}
	return testutil.WriteCommand(t, t.TempDir(), "version-cli",
		"#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf '%s\\n' '"+output+"'\n  exit 0\nfi\n"+body+"\n",
		"@echo off\r\nif not \"%~1\"==\"--version\" goto run\r\necho "+output+"\r\nexit /b 0\r\n:run\r\n"+windowsBody+"\r\n")
}

func versionConfig(t *testing.T, command string) Config {
	t.Helper()
	root := t.TempDir()
	return Config{CommonConfig: CommonConfig{Command: command, CWD: root, Env: []driver.EnvBinding{
		{Name: "HOME", Value: root}, {Name: "USERPROFILE", Value: root},
		{Name: "CODEX_HOME", Value: root}, {Name: "CLAUDE_CONFIG_DIR", Value: root},
		{Name: "CURSOR_HOME", Value: root}, {Name: "CODEBUDDY_CONFIG_DIR", Value: root},
		{Name: "OPENAI_API_KEY", Value: "fixture"}, {Name: "ANTHROPIC_API_KEY", Value: "fixture"},
		{Name: "CURSOR_API_KEY", Value: "fixture"}, {Name: "CODEBUDDY_API_KEY", Value: "fixture"},
	}}}
}

func TestVersionEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, output, version string
		old                   bool
	}{
		{"old", "2026.9.30-abcd", "2026.9.30-abcd", true},
		{"minimum", "2026.10.14-abcd", "2026.10.14-abcd", false},
		{"new", "2026.11.1-ff00", "2026.11.1-ff00", false},
		{"invalid-date", "2026.02.30-abcd", "", false},
		{"unknown", "unrecognized", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := versionCommand(t, tc.output, "exit 99")
			cfg := versionConfig(t, command)
			d := Driver(cfg)
			agent := adaptor.New(d)
			t.Cleanup(func() {
				if err := agent.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			report, err := agent.Inspect().Environment(context.Background())
			if err != nil || report.Version != tc.version || !report.Healthy {
				t.Fatalf("report=%+v error=%v", report, err)
			}
			declared := d.Descriptor().Messages
			want := declared
			if tc.old {
				want.Boundaries = nil
				want.Interrupt = false
				want.Continue = driver.ContinueResume
				want.ConfirmedDelivery = false
			}
			if !reflect.DeepEqual(report.Messages, want) {
				t.Fatalf("effective=%+v want=%+v", report.Messages, want)
			}
			if !reflect.DeepEqual(d.Descriptor().Messages, declared) {
				t.Fatal("version probe mutated the descriptor")
			}
			versionLine, warning := false, false
			for _, check := range report.Checks {
				if check.Code == DriverType+"_version" {
					versionLine = true
					if (check.Level == "warn") != (tc.version == "") {
						t.Fatalf("version check=%+v", check)
					}
				}
				if check.Code == DriverType+"_version_unsupported" {
					warning = true
					if check.Level != "warn" || check.Hint == "" || !strings.Contains(check.Message, "turn end") {
						t.Fatalf("warning=%+v", check)
					}
				}
			}
			if !versionLine || warning != tc.old {
				t.Fatalf("checks=%+v", report.Checks)
			}
			req := driver.Request{Streaming: true, Messages: &versionUnusedPort{}, Workspace: driver.WorkspaceLease{CWD: cfg.CWD, StrategyType: driver.WorkspaceStrategyGitWorktree}}
			got := d.(driver.MessageCapabilityResolver).ResolveMessageCapability(req)
			runtimeWant := want
			if runtime.GOOS == "windows" {
				// Version support does not enable the unverified Windows relay.
				runtimeWant.Boundaries = nil
			}
			if !reflect.DeepEqual(got, runtimeWant) {
				t.Fatalf("runtime=%+v want=%+v", got, runtimeWant)
			}
		})
	}
}

func TestVersionCacheInvalidates(t *testing.T) {
	command := versionCommand(t, "2026.9.30-abcd", "exit 99")
	cfg := versionConfig(t, command)
	d := Driver(cfg).(driver.EnvironmentProbe)
	first, err := d.CheckEnvironment(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(command)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(command), "replacement")
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(string(raw), "2026.9.30-abcd", "2026.11.1-ff00")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, command); err != nil {
		t.Fatal(err)
	}
	second, err := d.CheckEnvironment(context.Background(), nil)
	if err != nil || first.Version == second.Version || second.Version != "2026.11.1-ff00" {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

// A version-gated CLI must never receive an in-run port operation.
type versionUnusedPort struct{}

func (*versionUnusedPort) Window(bool)                                { panic("old CLI message window") }
func (*versionUnusedPort) Signal() <-chan struct{}                    { panic("old CLI message signal") }
func (*versionUnusedPort) Claim(driver.Boundary) []driver.MessageItem { panic("old CLI message claim") }
func (*versionUnusedPort) Ack(string, driver.MessageAck)              { panic("old CLI message ack") }

func TestVersionRuntimeDegradation(t *testing.T) {
	terminal := `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"version-session"}`
	command := versionCommand(t, "2026.10.13-abcd", "cat >/dev/null\necho '"+terminal+"'", "more >nul\r\necho "+terminal)
	cfg := versionConfig(t, command)
	d := Driver(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := driver.Request{RunID: "version-runtime", Prompt: "initial", Streaming: true, Spawn: true, Messages: &versionUnusedPort{}, Workspace: driver.WorkspaceLease{CWD: cfg.CWD, StrategyType: driver.WorkspaceStrategyGitWorktree}}
	sink := &testutil.EventRecorder{}
	for range 2 {
		res, err := d.Run(ctx, req, sink)
		if err != nil || res.Failure != nil || res.Output != "done" || res.Checkpoint == nil || !res.Checkpoint.Valid {
			t.Fatalf("old CLI base execution failed: response=%+v err=%v", res, err)
		}
		if strings.Contains(res.RawStreams.Stdout, "2026.10.13-abcd") {
			t.Fatal("version probe polluted execution raw output")
		}
		req.Session = &driver.SessionContext{Mode: driver.SessionContinueOrStart, State: res.Checkpoint.State}
	}
	count := 0
	for _, event := range sink.Snapshot() {
		if event.Metadata["code"] == "cursor_version_unsupported" {
			count++
		}
		if event.Metadata["code"] == "cursor_hook_unavailable" {
			t.Fatalf("old CLI attempted hook setup: %+v", event)
		}
	}
	if count != 1 {
		t.Fatalf("version notices=%d, want 1", count)
	}
}

// Park the first dispatch after capability resolution so both timing requests
// are tested deterministically before the provider can complete its turn.
type versionGatedDriver struct {
	configuredDriver
	started, release chan struct{}
	once             sync.Once
}

func (d *versionGatedDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.once.Do(func() {
		close(d.started)
		select {
		case <-d.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return driver.Response{}, err
	}
	return d.configuredDriver.Run(ctx, req, sink)
}

func TestVersionCoreResume(t *testing.T) {
	terminal := `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"version-session"}`
	command := versionCommand(t, "2026.10.13-abcd", "cat >/dev/null\necho '"+terminal+"'", "more >nul\r\necho "+terminal)
	cfg := versionConfig(t, command)
	configured := Driver(cfg).(configuredDriver)
	d := &versionGatedDriver{configuredDriver: configured, started: make(chan struct{}), release: make(chan struct{})}
	agent := adaptor.New(d, adaptor.WithSpawn())
	defer func() {
		if err := agent.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := agent.Send(ctx, "initial")
	select {
	case <-d.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := stream.Send(ctx, "interrupt", adaptor.When(adaptor.Interrupt)); !errors.Is(err, adaptor.ErrMessageUnsupported) {
		t.Fatalf("old CLI interrupt error=%v", err)
	}
	if err := stream.Send(ctx, "supplement", adaptor.MessageID("supplement")); err != nil {
		t.Fatal(err)
	}
	close(d.release)
	result, err := stream.Result()
	if err != nil || result.Turns != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	delivered := false
	for _, outcome := range result.Messages() {
		if outcome.ID == "supplement" {
			delivered = outcome.State == adaptor.MessageDelivered && outcome.Mechanism == "core.resume" && outcome.Boundary == "turn_end"
		}
	}
	if !delivered {
		t.Fatalf("supplement outcomes=%+v", result.Messages())
	}
	notices := 0
	for event := range stream.Events() {
		if notice, ok := event.(adaptor.Notice); ok && notice.Metadata["code"] == DriverType+"_version_unsupported" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("version notices=%d, want 1", notices)
	}
}
