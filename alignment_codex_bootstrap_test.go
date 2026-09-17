package adaptor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/tool"
)

// Real configured profile and session contracts; execution is entirely local.
// Unknown .system contents must remain protected even though a complete known
// provider bundle can be normalized by the immutable snapshot.
type codexBootstrapProfileDriver struct {
	driver.Driver
	driver.ProfileReporter
	driver.StreamSupport
	driver.SessionCodecProvider
	driver.SessionConfigFingerprinter
	entered atomic.Int32
	dir     string
}

func (d *codexBootstrapProfileDriver) Run(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
	d.entered.Add(1)
	d.dir = req.Profile.Dir
	if _, err := mcpruntime.SyncResource(ctx, "codex", req.Profile.Dir, mcpruntime.ProfileKindHostManaged, req.MCP); err != nil {
		return driver.Response{}, err
	}
	return driver.Response{Output: "healthy fixture", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "bootstrap-fixture"}}}, nil
}
func TestAlignmentProfileCodexBootstrapDrift(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, change := range []string{"unchanged", "unknown_system_bytes", "system_marker", "new_user_skill", "settings", "file_mode", "remove_skills_parent"} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, change), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				home, source, workspace := t.TempDir(), t.TempDir(), t.TempDir()
				write := func(root, path, body string) {
					t.Helper()
					p := filepath.Join(root, path)
					if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(body), 0644); err != nil {
						t.Fatal(err)
					}
				}
				write(source, "skills/.system/SKILL.md", "unknown bundle: actual input")
				write(source, "skills/.system/.codex-system-skills.marker", "91663ef126b94ab1\n")
				store := memory.NewStore()
				newAgent := func() (*Agent, *codexBootstrapProfileDriver) {
					real := codex.Driver(codex.Config{Model: "gpt-5.6-luna", CommonConfig: codex.CommonConfig{Command: filepath.Join(home, "must-not-execute"), CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEX_HOME", Value: source}}}})
					d := &codexBootstrapProfileDriver{Driver: real, ProfileReporter: real.(driver.ProfileReporter), StreamSupport: real.(driver.StreamSupport), SessionCodecProvider: real.(driver.SessionCodecProvider), SessionConfigFingerprinter: real.(driver.SessionConfigFingerprinter)}
					a := New(d, WithThreadStore(store), WithWorkspace(workspace), WithProfile(profile.Dedicated(source)), WithTools(tool.Define("probe", "offline", func(context.Context, struct{}) (string, error) { return "unused", nil }, tool.Revision("bootstrap/v1"))))
					t.Cleanup(func() { _ = a.Close(context.Background()) })
					return a, d
				}
				run := func(th *Thread) error {
					if stream {
						s := th.Stream(ctx, "fixture")
						for range s.Events() {
						}
						_, err := s.Result()
						return err
					}
					_, err := th.Run(ctx, "fixture")
					return err
				}
				first, d := newAgent()
				if err := run(first.Thread("cold")); err != nil {
					t.Fatal(err)
				}
				old, err := store.Resolve(ctx, threadstore.Query{Key: "cold"})
				if err != nil || old == nil || old.State == nil || old.State.ResumeID != "bootstrap-fixture" {
					t.Fatal("no healthy first record", err)
				}
				if err := first.Close(ctx); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "unknown_system_bytes":
					write(d.dir, "skills/.system/SKILL.md", "changed actual input")
				case "system_marker":
					write(d.dir, "skills/.system/.codex-system-skills.marker", "different-version\n")
				case "new_user_skill":
					write(d.dir, "skills/user/SKILL.md", "ordinary user skill")
				case "settings":
					write(d.dir, "config.toml", "model = \"other\"\n")
				case "file_mode":
					p := filepath.Join(d.dir, "skills/.system/SKILL.md")
					before, err := os.Stat(p)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(p, 0400); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(p, before.Mode().Perm()) })
					after, err := os.Stat(p)
					if err != nil || after.Mode() == before.Mode() {
						t.Fatal("mode fixture made no observable change", err)
					}
				case "remove_skills_parent":
					if err := os.RemoveAll(filepath.Join(d.dir, "skills")); err != nil {
						t.Fatal(err)
					}
				}
				second, next := newAgent()
				err = run(second.Thread("cold", ResumeOnly()))
				if change == "unchanged" {
					if err != nil || next.entered.Load() != 1 {
						t.Fatal("unchanged real profile rejected", err)
					}
				} else {
					if !errors.Is(err, ErrThreadIncompatible) || next.entered.Load() != 0 {
						t.Fatal("real profile drift reached Driver or lost compatibility error", err)
					}
					after, e := store.Resolve(ctx, threadstore.Query{Key: "cold"})
					if e != nil || !reflect.DeepEqual(old, after) {
						t.Fatal("rejection mutated healthy record", e)
					}
				}
			})
		}
	}
}
