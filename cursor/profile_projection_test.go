package cursor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestCursorProjectionConcurrentFirstUse(t *testing.T) {
	cursorPathTestEnvironment(t)
	for iteration := 0; iteration < 4; iteration++ {
		selected := t.TempDir()
		start := make(chan struct{})
		errs := make(chan error, 32)
		var wg sync.WaitGroup
		for i := 0; i < cap(errs); i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, cleanup, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, nil)
				if err == nil {
					err = cleanup()
				}
				if err != nil {
					errs <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		entries, err := os.ReadDir(selected)
		if err != nil || len(entries) != 1 || entries[0].Name() != cursorPrivateHomeName {
			t.Fatal("concurrent publication leaked staging directories", err)
		}
	}
}

func TestCursorProjectionCleanupRejectsReplacementAndLinks(t *testing.T) {
	cursorPathTestEnvironment(t)
	for _, change := range []string{"marker", "directory", "link"} {
		t.Run(change, func(t *testing.T) {
			selected := t.TempDir()
			bindings, _, cleanup, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			home := skillruntime.ResolveHome(bindings)
			marker := filepath.Join(home, cursorPrivateHomeMarker)
			switch change {
			case "marker":
				if err := os.Rename(marker, marker+".original"); err != nil {
					t.Fatal(err)
				}
				cursorWrite(t, marker, cursorProjectionOwner)
			case "directory":
				if err := os.Rename(home, home+".original"); err != nil {
					t.Fatal(err)
				}
				cursorWrite(t, marker, cursorProjectionOwner)
			case "link":
				if err := os.Rename(marker, marker+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(marker+".original", marker); err != nil {
					_ = cleanup()
					t.Skip("symlink unavailable")
				}
			}
			if err := cleanup(); err == nil {
				t.Fatal("cleanup accepted replacement ownership")
			}
			if _, err := os.Lstat(marker); err != nil {
				t.Fatal("failed ownership check deleted successor", err)
			}
		})
	}
}

func TestCursorProjectionSizeLimitAndSourceSymlinks(t *testing.T) {
	cursorPathTestEnvironment(t)
	for _, resource := range []string{"mcp.json", "agents/planner.md", "skills/one/SKILL.md"} {
		t.Run(resource, func(t *testing.T) {
			selected := t.TempDir()
			cursorWrite(t, filepath.Join(selected, resource), strings.Repeat("x", (8<<20)+1))
			_, _, _, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, nil)
			if err == nil {
				t.Fatal("oversized resource accepted")
			}
			entries, _ := os.ReadDir(filepath.Join(selected, cursorPrivateHomeName))
			if len(entries) != 1 {
				t.Fatal("failed copy leaked projection")
			}
		})
	}
	t.Run("selected profile symlink", func(t *testing.T) {
		target := t.TempDir()
		selected := filepath.Join(t.TempDir(), "selected")
		if err := os.Symlink(target, selected); err != nil {
			t.Skip("symlink unavailable")
		}
		if err := prepareCursorPrivateHome(selected); err == nil {
			t.Fatal("accepted linked source root")
		}
		entries, _ := os.ReadDir(target)
		if len(entries) != 0 {
			t.Fatal("modified rejected source")
		}
	})
}

func TestCursorRuntimeRootsStaySelectedAndCleanupFailureRetainsResponse(t *testing.T) {
	home := cursorPathTestEnvironment(t)
	for _, damage := range []bool{false, true} {
		t.Run(map[bool]string{false: "Native runtime override", true: "Dedicated cleanup failure"}[damage], func(t *testing.T) {
			selected, redirected := t.TempDir(), t.TempDir()
			observation := filepath.Join(t.TempDir(), "home")
			shell, cmd := "", ""
			if damage {
				shell = "mv \"$HOME/.agent-adaptor-owner\" \"$HOME/.owner-original\"\nprintf '%s\\n' 'agent-adaptor/cursor-run-projection/v1' > \"$HOME/.agent-adaptor-owner\"\n"
				cmd = "move /y \"%HOME%\\.agent-adaptor-owner\" \"%HOME%\\.owner-original\" >nul\r\necho agent-adaptor/cursor-run-projection/v1>\"%HOME%\\.agent-adaptor-owner\"\r\n"
			}
			terminal := `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"root-test"}`
			command := testutil.WriteCommand(t, t.TempDir(), "fake-cursor-projection",
				"#!/bin/sh\ncat >/dev/null\nprintf '%s' \"$HOME\" > \"$CURSOR_TEST_OBSERVATION\"\n"+shell+"printf '%s\\n' '"+terminal+"'\n",
				"@echo off\r\nset /p X=\r\n<nul set /p =%HOME%>\"%CURSOR_TEST_OBSERVATION%\"\r\n"+cmd+"echo "+terminal+"\r\n")
			cfg := Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: "CURSOR_TEST_OBSERVATION", Value: observation}}}}
			req := driver.Request{Prompt: "go", Config: cfg, Workspace: driver.WorkspaceLease{CWD: home}, Runtime: driver.RuntimePayload{SecretEnv: []driver.EnvBinding{{Name: "HOME", Value: redirected}, {Name: "USERPROFILE", Value: redirected}, {Name: "CURSOR_CONFIG_DIR", Value: redirected}, {Name: "CURSOR_DATA_DIR", Value: redirected}}}}
			if damage {
				req.Profile = &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: selected}
			}
			response, err := (adapter{}).Run(context.Background(), req, &testutil.EventRecorder{})
			if damage {
				if err == nil || !strings.Contains(err.Error(), "projection cleanup") || response.Checkpoint != nil {
					t.Fatal("cleanup failure forged a healthy checkpoint", err)
				}
			} else {
				if err != nil || response.Checkpoint == nil {
					t.Fatal("Native execution failed", err)
				}
				actual, err := os.ReadFile(observation)
				if err != nil || string(actual) != home {
					t.Fatal("runtime redirected Native HOME", string(actual), err)
				}
				if response.Checkpoint.State.Data[cursorSessionConfigDir] != filepath.Join(home, ".cursor") {
					t.Fatal("runtime redirected config root")
				}
			}
			if response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, terminal) || response.RawStreams.Terminal == nil || response.Output != "done" || len(response.Transcript) == 0 {
				t.Fatal("cleanup outcome lost response layers")
			}
		})
	}
}

// Runs unchanged on Windows Actions: the helper itself, bypassing any outer
// pre-check, must never replace even an empty conflicting directory.
func TestCursorPrivateHomePublicationNeverReplaces(t *testing.T) {
	for _, kind := range []string{"directory", "file", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cursorWrite(t, filepath.Join(dir, "pending", "marker"), "new")
			target := filepath.Join(dir, "published")
			switch kind {
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "file":
				cursorWrite(t, target, "old")
			case "symlink":
				if err := os.Symlink(t.TempDir(), target); err != nil {
					t.Skip("symlink unavailable")
				}
			}
			before, _ := os.Lstat(target)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = publishCursorPrivateHome(root, "pending", "published")
			if kind == "missing" {
				if err != nil {
					t.Fatal(err)
				}
				raw, e := os.ReadFile(filepath.Join(target, "marker"))
				if e != nil || string(raw) != "new" {
					t.Fatal("publication lost marker", e)
				}
			} else {
				if err == nil {
					t.Fatal("publication replaced existing target")
				}
				after, e := os.Lstat(target)
				if e != nil || !os.SameFile(before, after) {
					t.Fatal("target identity changed", e)
				}
				raw, e := os.ReadFile(filepath.Join(dir, "pending", "marker"))
				if e != nil || string(raw) != "new" {
					t.Fatal("rejected source changed", e)
				}
			}
		})
	}
}

func TestCursorProjectionSnapshotTracksDeliveredBytes(t *testing.T) {
	cursorPathTestEnvironment(t)
	selected := t.TempDir()
	cursorWrite(t, filepath.Join(selected, "agents", "planner.md"), "initial agent")
	cursorWrite(t, filepath.Join(selected, "hooks.json"), `{"version":1,"hooks":{}}`)
	before, err := cursorResourceState(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	var delivered string
	bindings, plugin, cleanup, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, nil, &delivered)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if delivered != before {
		t.Fatal("projection and preflight use different stable source keys")
	}
	cursorWrite(t, filepath.Join(selected, "agents", "planner.md"), "externally changed agent")
	cursorWrite(t, filepath.Join(selected, "hooks.json"), `{"version":2,"hooks":{}}`)
	changed, err := cursorResourceState(context.Background(), selected)
	if err != nil || changed == delivered {
		t.Fatal("source edit did not change current guard", err)
	}
	agent, err := os.ReadFile(filepath.Join(plugin, "agents", "planner.md"))
	if err != nil || string(agent) != "initial agent" {
		t.Fatal("projection did not retain delivered agent bytes", err)
	}
	hooks, err := os.ReadFile(filepath.Join(skillruntime.ResolveHome(bindings), ".cursor", "hooks.json"))
	if err != nil || string(hooks) != `{"version":1,"hooks":{}}` {
		t.Fatal("projection did not retain delivered hook bytes", err)
	}
	cursorWrite(t, filepath.Join(selected, "agents", "planner.md"), "initial agent")
	cursorWrite(t, filepath.Join(selected, "hooks.json"), `{"version":1,"hooks":{}}`)
	restored, err := cursorResourceState(context.Background(), selected)
	if err != nil || restored != delivered {
		t.Fatal("restored source did not agree with delivered snapshot", err)
	}
}
