package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestContextWindowInvalidConfigFailsBeforePreparation(t *testing.T) {
	for name, cfg := range map[string]Config{
		"negative window":  {ContextWindowTokens: -1},
		"inexact window":   {ContextWindowTokens: 1 << 53},
		"negative compact": {AutoCompactWindowTokens: -1},
		"small compact":    {AutoCompactWindowTokens: 99999},
		"large compact":    {AutoCompactWindowTokens: 1000001},
		"window env conflict": {
			ContextWindowTokens: 200000,
			CommonConfig:        CommonConfig{Env: []driver.EnvBinding{{Name: claudeContextWindowEnv, Value: "secret"}}},
		},
		"compact env conflict": {
			AutoCompactWindowTokens: 150000,
			CommonConfig:            CommonConfig{Env: []driver.EnvBinding{{Name: claudeAutoCompactEnv, Value: "150000"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "must-not-exist")
			cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CLAUDE_CONFIG_DIR", Value: profile})
			cfg.Command = filepath.Join(profile, "must-not-run")
			d := Driver(cfg).(configuredDriver)
			assertInvalid := func(err error) {
				t.Helper()
				var invalid *driver.InvalidDriverConfigError
				if !errors.As(err, &invalid) || invalid.Driver != DriverType || strings.Contains(err.Error(), "secret") {
					t.Fatalf("expected safe invalid-config error: %v", err)
				}
			}
			assertInvalid(d.ValidateConfig(nil))
			_, err := d.ConfigSchema(context.Background(), nil)
			assertInvalid(err)
			_, err = d.CheckEnvironment(context.Background(), nil)
			assertInvalid(err)
			sink := &testutil.EventRecorder{}
			_, err = d.Run(context.Background(), driver.Request{}, sink)
			assertInvalid(err)
			if len(sink.Snapshot()) != 0 {
				t.Fatal("invalid config published execution events")
			}
			if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid config prepared profile: %v", err)
			}
		})
	}
}

func TestContextWindowRuntimeConflictFailsBeforePreparation(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "must-not-exist")
	cfg := Config{ContextWindowTokens: 200000, CommonConfig: CommonConfig{Env: []driver.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: profile}}}}
	for _, name := range []string{claudeContextWindowEnv, strings.ToLower(claudeContextWindowEnv)} {
		err := validateClaudeContextConfig(cfg, []driver.EnvBinding{{Name: name, Value: "secret"}})
		want := name == claudeContextWindowEnv || runtime.GOOS == "windows"
		if (err != nil) != want {
			t.Fatalf("environment key %q: %v", name, err)
		}
	}
	_, err := Driver(cfg).Run(context.Background(), driver.Request{Runtime: driver.RuntimePayload{
		SecretEnv: []driver.EnvBinding{{Name: claudeContextWindowEnv, Value: "secret"}},
	}}, &testutil.EventRecorder{})
	var invalid *driver.InvalidDriverConfigError
	if !errors.As(err, &invalid) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("runtime conflict: %v", err)
	}
	if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime conflict prepared profile")
	}
}

func TestContextWindowNativeEnvironmentAndZeroDefaults(t *testing.T) {
	windowName, compactName := claudeContextWindowEnv, claudeAutoCompactEnv
	if runtime.GOOS == "windows" {
		windowName, compactName = strings.ToLower(windowName), strings.ToLower(compactName)
	}
	t.Setenv(windowName, "300000")
	t.Setenv(compactName, "250000")
	for _, streaming := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "context-env",
				`#!/bin/sh
set -eu
cat >/dev/null
printf '{"type":"assistant","session_id":"context-test","message":{"content":[{"type":"text","text":"%s/%s"}]}}\n' "$CLAUDE_CODE_MAX_CONTEXT_TOKENS" "$CLAUDE_CODE_AUTO_COMPACT_WINDOW"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"context-test","result":"%s/%s"}\n' "$CLAUDE_CODE_MAX_CONTEXT_TOKENS" "$CLAUDE_CODE_AUTO_COMPACT_WINDOW"
`, "@echo off\r\nmore > nul\r\necho {\"type\":\"assistant\",\"session_id\":\"context-test\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"%CLAUDE_CODE_MAX_CONTEXT_TOKENS%/%CLAUDE_CODE_AUTO_COMPACT_WINDOW%\"}]}}\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"context-test\",\"result\":\"%CLAUDE_CODE_MAX_CONTEXT_TOKENS%/%CLAUDE_CODE_AUTO_COMPACT_WINDOW%\"}\r\n")
			cfg := Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{
				{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: home},
			}}}
			want := "300000/250000"
			if explicit {
				cfg.ContextWindowTokens, cfg.AutoCompactWindowTokens = 200000, 150000
				want = "200000/150000"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			r, err := Driver(cfg).Run(ctx, driver.Request{Prompt: "unchanged", Streaming: streaming}, &testutil.EventRecorder{})
			cancel()
			if err != nil || r.Failure != nil || r.ExitCode != 0 || r.Output != want || r.Checkpoint == nil || !r.Checkpoint.Valid {
				t.Fatalf("streaming=%v explicit=%v: %v %#v", streaming, explicit, err, r)
			}
		}
	}
	base := []driver.EnvBinding{{Name: claudeContextWindowEnv, Value: "raw"}}
	if got := appendClaudeContextEnv(base, Config{}); !reflect.DeepEqual(got, base) {
		t.Fatalf("zero config changed explicit environment: %#v", got)
	}
}

func TestContextWindowConfigSnapshotSchemaAndProcessSignature(t *testing.T) {
	base := Config{ContextWindowTokens: 200000, AutoCompactWindowTokens: 150000}
	d := Driver(base).(configuredDriver)
	base.ContextWindowTokens = 400000
	if got := d.requestWithConfig(driver.Request{}).Config.(Config); got.ContextWindowTokens != 200000 || got.AutoCompactWindowTokens != 150000 {
		t.Fatalf("construction snapshot changed: %#v", got)
	}
	schema := d.Descriptor().ConfigSchema
	for _, name := range []string{"context_window_tokens", "auto_compact_window_tokens"} {
		if field := schemaFieldByName(t, schema, name); field.Type != "number" || field.Default != int64(0) {
			t.Fatalf("schema field: %#v", field)
		}
	}
	initial := d.cfg
	for _, changed := range []Config{{ContextWindowTokens: 300000, AutoCompactWindowTokens: 150000}, {ContextWindowTokens: 200000, AutoCompactWindowTokens: 160000}} {
		if configuredSessionFingerprint(t, Driver(initial)) == configuredSessionFingerprint(t, Driver(changed)) {
			t.Fatal("context change did not change Thread config fingerprint")
		}
		oldSpec := persistentSpec{env: appendClaudeContextEnv(nil, initial)}
		newSpec := persistentSpec{env: appendClaudeContextEnv(nil, changed)}
		if oldSpec.sig() == newSpec.sig() {
			t.Fatal("context change would reuse stale persistent process")
		}
	}
	for _, cfg := range []Config{{}, {ContextWindowTokens: claudeMaxExactTokens}, {AutoCompactWindowTokens: 100000}, {AutoCompactWindowTokens: 1000000}} {
		if err := Driver(cfg).ValidateConfig(nil); err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
	}
}

func TestContextWindowWindowsAmbientAliasesCannotOverrideTypedValue(t *testing.T) {
	for _, name := range []string{claudeContextWindowEnv, claudeAutoCompactEnv} {
		lower := strings.ToLower(name)
		mixed := "Claude_" + name[len("CLAUDE_"):]
		ambient := []string{name + "=300000", lower + "=350000", mixed + "=400000", "UNRELATED=preserved"}
		bindings := appendClaudeContextEnvAliases(nil, ambient, name, "200000")
		bindings = append(bindings, driver.EnvBinding{Name: name, Value: "200000"})
		// Model the helpers' exact-key map, then check every possible ordering
		// before os/exec's Windows last-wins case-insensitive deduplication.
		merged := make(map[string]string)
		for _, entry := range ambient {
			key, value, _ := strings.Cut(entry, "=")
			merged[key] = value
		}
		for _, binding := range bindings {
			merged[binding.Name] = binding.Value
		}
		for _, key := range []string{name, lower, mixed} {
			if merged[key] != "200000" {
				t.Fatalf("Windows ordering could restore stale %s=%s", key, merged[key])
			}
		}
		if merged["UNRELATED"] != "preserved" || len(merged) != 4 {
			t.Fatal("projection changed unrelated ambient environment")
		}
	}
}
