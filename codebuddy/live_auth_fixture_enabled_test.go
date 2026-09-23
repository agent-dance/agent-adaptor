//go:build codebuddy_live

package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
)

// Exercises the actual live wrapper, with live gates disabled and a private
// executable copy of this test binary occupying the CLI name. No real CLI runs.
func codeBuddyNativeAuthFixtureEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AGENT_ADAPTOR_LIVE_CONFORMANCE", "AGENT_ADAPTOR_E2E", "UPDATE_API_GOLDEN"} {
		t.Setenv(name, "0")
	}
	for _, name := range []string{"CODEBUDDY_API_KEY", "CODEBUDDY_AUTH_TOKEN", "CODEBUDDY_INTERNET_ENVIRONMENT", "CODEBUDDY_INTERNET_ENVIROMENT", "CODEBUDDY_BASE_URL", "CODEBUDDY_CUSTOM_HEADERS", "CODEBUDDY_CREDENTIALS_IN_MEMORY", "ACC_PRODUCT_CONFIG_PATH", "ACC_PRODUCT_CONFIG_V2", "ACC_PRODUCT_CONFIG_V3"} {
		t.Setenv(name, "")
	}
}

func installCodeBuddyNativeAuthFixtureCLI(t *testing.T) string {
	t.Helper()
	t.Setenv("GO_WANT_CODEBUDDY_LIVE_AUTH_FIXTURE", "1")
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("CODEBUDDY_AUTH_FIXTURE_CAPTURE", capturePath)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "codebuddy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := t.TempDir()
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(bin, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy test executable failed")
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return capturePath
}

func TestCodeBuddyNativeAuthFixtureWrapper(t *testing.T) {
	codeBuddyNativeAuthFixtureEnvironment(t)
	seed := codeBuddySyntheticSeed(t)
	t.Setenv("CODEBUDDY_NATIVE_AUTH_FILE_SOURCE", seed)
	t.Setenv("CODEBUDDY_CONFIG_DIR_SOURCE", t.TempDir())
	capturePath := installCodeBuddyNativeAuthFixtureCLI(t)
	for _, kind := range []string{"newLiveAgent", "conformance", "cold_successor"} {
		var capturedHome string
		t.Run(kind, func(t *testing.T) {
			var first, second *adaptor.Agent
			switch kind {
			case "newLiveAgent":
				first = newLiveAgent(t, t.TempDir(), false)
			case "conformance":
				first = adaptor.New(Driver(codebuddyConformanceConfig(t, true, t.TempDir(), t.TempDir())))
			case "cold_successor":
				cfg, source := alignmentLiveCodeBuddyColdConfig(t, t.TempDir())
				first = adaptor.New(Driver(cfg), adaptor.WithProfile(profile.Dedicated(source)))
				second = adaptor.New(Driver(cfg), adaptor.WithProfile(profile.Dedicated(source)))
			}
			t.Cleanup(func() {
				if err := first.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if second != nil {
				t.Cleanup(func() {
					if err := second.Close(context.Background()); err != nil {
						t.Error(err)
					}
				})
			}
			run := func(a *adaptor.Agent) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				result, runErr := a.Run(ctx, "Synthetic native auth fixture", adaptor.WithPolicy(livePolicyHeadless))
				var capture codeBuddyAuthCapture
				raw, err := os.ReadFile(capturePath)
				if err != nil || json.Unmarshal(raw, &capture) != nil {
					t.Fatal("private test executable was not reached")
				}
				if !capture.NativeExact || !capture.HomeMatches || runErr != nil || result == nil || result.Text != "NATIVE_SEED_EXACT" {
					t.Fatalf("native seed wrapper: exact=%t shared_home=%t run_error=%v", capture.NativeExact, capture.HomeMatches, runErr)
				}
				if capturedHome != "" && capturedHome != capture.Home {
					t.Fatal("cold successor changed private authentication HOME")
				}
				capturedHome = capture.Home
			}
			run(first)
			if second != nil {
				if err := first.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				run(second)
			}
		})
		if capturedHome != "" {
			if _, err := os.Stat(capturedHome); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrapper credentials survived test cleanup")
			}
		}
	}
	if data, err := os.ReadFile(seed); err != nil || string(data) != codeBuddySyntheticNativeSession {
		t.Fatal("wrapper changed native source")
	}
}

func TestCodeBuddyNativeAuthFixtureMissingBeforeCLI(t *testing.T) {
	if os.Getenv("CODEBUDDY_AUTH_FIXTURE_REJECTION_CHILD") == "1" {
		t.Setenv("GO_WANT_CODEBUDDY_LIVE_AUTH_FIXTURE", "1")
		t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "1")
		requireCodeBuddyCLI(t)
		newLiveAgent(t, t.TempDir(), false)
		t.Fatal("missing native seed was accepted")
	}
	codeBuddyNativeAuthFixtureEnvironment(t)
	capture := installCodeBuddyNativeAuthFixtureCLI(t)
	t.Setenv("GO_WANT_CODEBUDDY_LIVE_AUTH_FIXTURE", "0")
	t.Setenv("CODEBUDDY_AUTH_FIXTURE_REJECTION_CHILD", "1")
	t.Setenv("CODEBUDDY_NATIVE_AUTH_FILE_SOURCE", filepath.Join(codeBuddyFixtureSeedDir(t), "missing.info"))
	t.Setenv("CODEBUDDY_CONFIG_DIR_SOURCE", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCodeBuddyNativeAuthFixtureMissingBeforeCLI$")
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(string(output), "validate explicit live authentication before CLI: read explicit native authentication seed") || strings.Contains(string(output), "missing native seed was accepted") {
		t.Fatal("missing seed did not fail in fixture preparation")
	}
	if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing seed reached CLI canary")
	}
}

func TestCodeBuddyNativeAuthFixtureDisabledGateIgnoresSeed(t *testing.T) {
	codeBuddyNativeAuthFixtureEnvironment(t)
	capture := installCodeBuddyNativeAuthFixtureCLI(t)
	t.Setenv("CODEBUDDY_NATIVE_AUTH_FILE_SOURCE", "relative-invalid-source")
	t.Run("actual conformance gate", func(t *testing.T) {
		if live, _ := codebuddyLiveGate(t); live {
			t.Fatal("disabled conformance gate enabled execution")
		}
		codebuddyConformanceConfig(t, false, t.TempDir(), t.TempDir())
	})
	t.Run("actual live gate", func(t *testing.T) { requireCodeBuddyCLI(t); t.Fatal("disabled live gate returned") })
	if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled gate accessed CLI")
	}
}
