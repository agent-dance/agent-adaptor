package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		{"old", "codex-cli 0.99.0", "0.99.0", true},
		{"minimum", "codex-cli 0.100.0", "0.100.0", false},
		{"new", "codex-cli 0.166.0", "0.166.0", false},
		{"prerelease", "codex-cli 0.100.0-alpha.1", "0.100.0-alpha.1", true},
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
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("runtime=%+v want=%+v", got, want)
			}
		})
	}
}

func TestVersionCacheInvalidates(t *testing.T) {
	command := versionCommand(t, "codex-cli 0.99.0", "exit 99")
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
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(string(raw), "codex-cli 0.99.0", "codex-cli 0.166.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, command); err != nil {
		t.Fatal(err)
	}
	second, err := d.CheckEnvironment(context.Background(), nil)
	if err != nil || first.Version == second.Version || second.Version != "0.166.0" {
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
	a, req, _, _ := newPersistentCodexTest(t, "")
	defer closeCodexTestAdapter(t, a)
	cfg := req.Config.(Config)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	cfg.Command = versionCommand(t, "codex-cli 0.99.0", "exec "+quote(cfg.Command)+" \"$@\"", "\""+cfg.Command+"\" %*")
	req.Config, req.Messages, req.Spawn = cfg, &versionUnusedPort{}, true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &testutil.EventRecorder{}
	for range 2 {
		res, err := a.Run(ctx, req, sink)
		if err != nil || res.Failure != nil || res.Checkpoint == nil || !res.Checkpoint.Valid {
			t.Fatalf("old CLI base execution failed: response=%+v err=%v", res, err)
		}
		if strings.Contains(res.RawStreams.Stdout, "codex-cli 0.99.0") {
			t.Fatal("version probe polluted execution raw output")
		}
		runID := req.RunID
		req = resumedCodexRequest(req, res)
		req.RunID = runID
	}
	count := 0
	for _, event := range sink.Snapshot() {
		if event.Metadata["code"] == "codex_version_unsupported" {
			count++
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
	a, req, _, _ := newPersistentCodexTest(t, "")
	cfg := req.Config.(Config)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	cfg.Command = versionCommand(t, "codex-cli 0.99.0", "exec "+quote(cfg.Command)+" \"$@\"", "\""+cfg.Command+"\" %*")
	configured := configuredDriver{adapter: a.(adapter), cfg: cfg}
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
