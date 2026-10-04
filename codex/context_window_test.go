package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestContextWindowInvalidConfigFailsBeforePreparation(t *testing.T) {
	cases := map[string]Config{
		"negative window":  {ContextWindowTokens: -1},
		"negative compact": {AutoCompactTokenLimit: -1},
	}
	for _, arg := range []string{
		"model_context_window=200000", `"model_context_window"=200000`, `'model_context_window'.child="secret"`,
		"model_auto_compact_token_limit=150000", `"model_auto_compact_token_limit"=150000`,
	} {
		for _, args := range [][]string{{"-c", arg}, {"--config", arg}, {"-c" + arg}, {"--config=" + arg}} {
			cases[strings.Join(args, " ")] = Config{ContextWindowTokens: 200000, AutoCompactTokenLimit: 150000, CommonConfig: CommonConfig{ExtraArgs: args}}
		}
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			profile := filepath.Join(t.TempDir(), "must-not-exist")
			cfg.Env = []driver.EnvBinding{{Name: "CODEX_HOME", Value: profile}}
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
			for _, streaming := range []bool{false, true} {
				sink := &testutil.EventRecorder{}
				_, err = d.Run(context.Background(), driver.Request{Streaming: streaming}, sink)
				assertInvalid(err)
				if len(sink.Snapshot()) != 0 {
					t.Fatal("invalid config published execution events")
				}
			}
			if _, err := os.Stat(profile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid config prepared profile: %v", err)
			}
		})
	}
}

func TestContextWindowNativeArgsReachAllTransportsWithoutProfileWrites(t *testing.T) {
	command := alignmentCodexFixture(t)
	for _, transport := range []string{"exec", "app-server", "persistent"} {
		t.Run(transport, func(t *testing.T) {
			cfg, capture := alignmentCodexConfig(t, command, "")
			cfg.ContextWindowTokens, cfg.AutoCompactTokenLimit = 200000, 150000
			cfg.ExtraArgs = []string{"-c", "unrelated=literal with spaces"}
			profile := filepath.Join(cfg.CWD, "profile")
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(profile, "config.toml")
			original := []byte("# user-owned\nmodel_context_window = 400000\nmodel_auto_compact_token_limit = 300000\n")
			if err := os.WriteFile(configPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			d := Driver(cfg).(configuredDriver)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			defer d.CloseProcesses(ctx)
			req := driver.Request{Prompt: "unchanged", Streaming: transport != "exec", Workspace: driver.WorkspaceLease{CWD: cfg.CWD}}
			if transport == "persistent" {
				req.Session = &driver.SessionContext{EngineSessionID: "context-test"}
			}
			r, err := d.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || r.Failure != nil || r.ExitCode != 0 || r.Checkpoint == nil || !r.Checkpoint.Valid {
				t.Fatalf("run: %v %#v", err, r)
			}
			if err := d.CloseProcesses(ctx); err != nil {
				t.Fatal(err)
			}
			frames := alignmentCapture(t, capture)
			starts := 0
			for _, frame := range frames {
				if string(frame["event"]) != `"start"` {
					continue
				}
				starts++
				var args []string
				if err := json.Unmarshal(frame["args"], &args); err != nil {
					t.Fatal(err)
				}
				for _, value := range []string{"model_context_window=200000", "model_auto_compact_token_limit=150000", "unrelated=literal with spaces"} {
					count := 0
					for i, arg := range args {
						if i > 0 && args[i-1] == "-c" && arg == value {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("override %q count=%d in %#v", value, count, args)
					}
				}
			}
			if starts != 1 {
				t.Fatalf("unexpected process starts: %d", starts)
			}
			if got, err := os.ReadFile(configPath); err != nil || string(got) != string(original) {
				t.Fatalf("native profile modified: %v %q", err, got)
			}
		})
	}
}

func TestContextWindowZeroAndIndependentControlsPreserveExtraArgs(t *testing.T) {
	for _, cfg := range []Config{
		{CommonConfig: CommonConfig{ExtraArgs: []string{"-c", "model_context_window=legacy literal"}}},
		{ContextWindowTokens: 200000, CommonConfig: CommonConfig{ExtraArgs: []string{"-c", "model_auto_compact_token_limit=150000"}}},
		{AutoCompactTokenLimit: 150000, CommonConfig: CommonConfig{ExtraArgs: []string{"-c", "model_context_window=200000"}}},
	} {
		if err := Driver(cfg).ValidateConfig(nil); err != nil {
			t.Fatal(err)
		}
		args, err := codexExecArgs(driver.Request{}, cfg, "")
		if err != nil || !strings.Contains(strings.Join(args, "\x00"), strings.Join(cfg.ExtraArgs, "\x00")) {
			t.Fatalf("exec did not preserve escape hatch: %v %#v", err, args)
		}
		opts, err := buildAppServerOptions(driver.Request{}, cfg, "fixture", nil, profileinstructions.Prepared{})
		if err != nil || !strings.Contains(strings.Join(opts.ExtraArgs, "\x00"), strings.Join(cfg.ExtraArgs, "\x00")) {
			t.Fatalf("app-server did not preserve escape hatch: %v %#v", err, opts.ExtraArgs)
		}
	}
	if args, err := codexContextArgs(Config{}); err != nil || len(args) != 0 {
		t.Fatalf("zero config injected overrides: %v %#v", err, args)
	}
}

func TestContextWindowConfigSnapshotSchemaAndProcessSignature(t *testing.T) {
	base := Config{ContextWindowTokens: 200000, AutoCompactTokenLimit: 150000}
	d := Driver(base).(configuredDriver)
	base.ContextWindowTokens = 400000
	if got := d.requestWithConfig(driver.Request{}).Config.(Config); got.ContextWindowTokens != 200000 || got.AutoCompactTokenLimit != 150000 {
		t.Fatalf("construction snapshot changed: %#v", got)
	}
	schema, err := d.ConfigSchema(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"context_window_tokens", "auto_compact_token_limit"} {
		if field := schemaFieldByName(t, schema, name); field.Type != "number" || field.Default != int64(0) {
			t.Fatalf("schema field: %#v", field)
		}
	}
	initialArgs, err := codexContextArgs(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []Config{{ContextWindowTokens: 300000, AutoCompactTokenLimit: 150000}, {ContextWindowTokens: 200000, AutoCompactTokenLimit: 160000}} {
		if codexSessionFingerprint(t, d) == codexSessionFingerprint(t, Driver(changed)) {
			t.Fatal("context change did not change Thread config fingerprint")
		}
		args, err := codexContextArgs(changed)
		if err != nil {
			t.Fatal(err)
		}
		oldSpec, newSpec := persistentSpec{extraArgs: initialArgs}, persistentSpec{extraArgs: args}
		if oldSpec.sig() == newSpec.sig() || !reflect.DeepEqual(newSpec.openOptions().ExtraArgs, args) {
			t.Fatal("context change would reuse stale persistent process")
		}
	}
}
