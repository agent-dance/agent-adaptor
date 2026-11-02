package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
		{"old", "1.0.0", "1.0.0", false},
		{"new", "CodeBuddy 2.0.0", "2.0.0", false},
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
		})
	}
}

func TestVersionCacheInvalidates(t *testing.T) {
	command := versionCommand(t, "1.0.0", "exit 99")
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
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(string(raw), "1.0.0", "CodeBuddy 2.0.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, command); err != nil {
		t.Fatal(err)
	}
	second, err := d.CheckEnvironment(context.Background(), nil)
	if err != nil || first.Version == second.Version || second.Version != "2.0.0" {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}
