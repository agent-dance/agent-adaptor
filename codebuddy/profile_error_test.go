package codebuddy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
)

func alignmentProfileErrorConfig(t *testing.T) Config {
	t.Helper()
	home, source := t.TempDir(), t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "cli-start")
	t.Cleanup(func() {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("profile operation started CLI canary")
		}
	})
	return Config{CommonConfig: CommonConfig{Command: executable, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: source}, {Name: "ALIGNMENT_CODEBUDDY_PROFILE_CANARY", Value: marker}}}, Model: "glm-5.2-ioa"}
}

func TestAlignmentCodeBuddyProfileErrorIdentity(t *testing.T) {
	cfg := alignmentProfileErrorConfig(t)
	source := t.TempDir()
	blocked := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocked, []byte("retain me"), 0600); err != nil {
		t.Fatal(err)
	}
	// Obtain the platform's actual underlying filesystem cause rather than
	// assuming POSIX errno constants on Windows.
	expected := os.MkdirAll(blocked, 0700)
	var original *os.PathError
	if !errors.As(expected, &original) {
		t.Fatal("regular file did not reject directory creation with PathError")
	}
	selection := profile.CloneFrom(source, blocked, profile.CopySkills())
	d := Driver(cfg)
	assertCause := func(t *testing.T, err error) {
		t.Helper()
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || !errors.Is(err, original.Err) {
			t.Fatalf("profile error lost filesystem cause: error type=%T", err)
		}
		if filepath.Clean(pathErr.Path) != filepath.Clean(blocked) {
			t.Fatal("profile error lost failing path")
		}
	}
	t.Run("configured_reporter", func(t *testing.T) {
		report, err := d.(driver.ProfileReporter).GetProfile(context.Background(), nil, driver.AgentIdentity{}, &selection)
		assertCause(t, err)
		if report.Dir != "" {
			t.Fatal("failed profile reported usable directory")
		}
	})
	a := adaptor.New(d, adaptor.WithProfile(selection), adaptor.WithTools(tool.Define("profile_probe", "unused", func(context.Context, struct{}) (string, error) { return "unused", nil }, tool.ReadOnly())))
	defer a.Close(context.Background())
	t.Run("public_run", func(t *testing.T) { _, err := a.Run(context.Background(), "must fail before CLI"); assertCause(t, err) })
	t.Run("profile_state", func(t *testing.T) { _, err := a.ProfileState(context.Background()); assertCause(t, err) })
	t.Run("sync_profile", func(t *testing.T) { _, err := a.SyncProfile(context.Background()); assertCause(t, err) })
	t.Run("inspect_skills", func(t *testing.T) { _, err := a.Inspect().Skills(context.Background()); assertCause(t, err) })
	// The legacy internal report view remains an explicit error view; it is
	// not the error identity channel used by ProfileReporter or execution.
	t.Run("legacy_error_view", func(t *testing.T) {
		report := resolveProfile(cfg.CommonConfig, &selection)
		if !report.Supported || report.DriverType != DriverType || report.Dir != "" || report.Error == "" {
			t.Fatal("legacy AgentProfile.Error view changed")
		}
	})
	if data, err := os.ReadFile(blocked); err != nil || string(data) != "retain me" {
		t.Fatal("failed profile resolution changed original file")
	}
}

func TestAlignmentCodeBuddyProfileReporterSuccess(t *testing.T) {
	cfg := alignmentProfileErrorConfig(t)
	d := Driver(cfg).(driver.ProfileReporter)
	for _, tc := range []struct {
		name      string
		selection *driver.ProfileSelection
		want      string
	}{
		{name: "captured_config", want: cfg.Env[2].Value},
		{name: "dedicated", selection: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: t.TempDir()}},
		{name: "clone", selection: &driver.ProfileSelection{Mode: driver.ProfileModeClone, Dir: t.TempDir(), From: t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := d.GetProfile(context.Background(), nil, driver.AgentIdentity{}, tc.selection)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if tc.selection != nil {
				want = tc.selection.Dir
			}
			if !report.Supported || report.DriverType != DriverType || report.Error != "" || filepath.Clean(report.Dir) != filepath.Clean(want) {
				t.Fatal("supported profile report/configuration changed")
			}
		})
	}
	t.Run("invalid_selection", func(t *testing.T) {
		selection := driver.ProfileSelection{Mode: "invalid"}
		report, err := d.GetProfile(context.Background(), nil, driver.AgentIdentity{}, &selection)
		if err == nil || report.Dir != "" {
			t.Fatal("invalid profile selection did not return Go error")
		}
	})
}
