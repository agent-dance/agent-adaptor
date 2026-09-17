package adaptor_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/tool"
)

// Profile and skill operations use the real configured CodeBuddy implementation.
// Only execution is replaced: this fixture never starts a CLI or reads auth.
type managedCloneProfileDriver struct {
	driver.Driver
	driver.ProfileReporter
	driver.SkillSupport
	driver.StreamSupport
	driver.SessionCodecProvider
	driver.SessionConfigFingerprinter
	engine.ProfileResourceDriver
	entered  atomic.Int32
	injected atomic.Int32
	mu       sync.Mutex
	requests []driver.Request
}

func (d *managedCloneProfileDriver) InjectSkills(ctx context.Context, cfg any, p driver.ResolvedSkills, s *driver.ProfileSelection) error {
	d.injected.Add(1)
	return d.SkillSupport.InjectSkills(ctx, cfg, p, s)
}
func (d *managedCloneProfileDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.entered.Add(1)
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	if len(req.Runtime.SecretEnv) == 0 {
		return driver.Response{}, errors.New("hosted runtime environment lost")
	}
	for _, entry := range req.Skills.Entries {
		path := filepath.Join(req.Profile.Dir, "skills", entry.RuntimeName)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return driver.Response{}, fmt.Errorf("copied skill not available before Driver: %v", err)
		}
	}
	// CodeBuddy normally reconciles in Run. Retain its real resource method while
	// replacing its process dispatch, so target manifest paths are tested too.
	if _, err := d.SkillSupport.SyncSkills(ctx, req.Config, req.Skills, req.Skills.Keys(), nil, req.Profile); err != nil {
		return driver.Response{}, err
	}
	manifest, err := profilestate.LoadManifest(req.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	for _, entry := range manifest.KindEntries("skills") {
		if filepath.Dir(filepath.Dir(entry.Path)) != req.Profile.Dir {
			return driver.Response{}, errors.New("staging/source manifest path survived")
		}
	}
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID}); err != nil {
		return driver.Response{}, err
	}
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID}); err != nil {
		return driver.Response{}, err
	}
	return driver.Response{Output: "zero CLI reached", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "fixture-session"}}}, nil
}
func (d *managedCloneProfileDriver) request() driver.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[len(d.requests)-1]
}

func newManagedCloneProfileDriver(home, source string) *managedCloneProfileDriver {
	real := codebuddy.Driver(codebuddy.Config{CommonConfig: codebuddy.CommonConfig{Command: filepath.Join(home, "never-executed"), Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: source}}}})
	return &managedCloneProfileDriver{Driver: real, ProfileReporter: real.(driver.ProfileReporter), SkillSupport: real.(driver.SkillSupport), StreamSupport: real.(driver.StreamSupport), SessionCodecProvider: real.(driver.SessionCodecProvider), SessionConfigFingerprinter: real.(driver.SessionConfigFingerprinter), ProfileResourceDriver: real.(engine.ProfileResourceDriver)}
}

type managedCloneMaterializer struct {
	inner skill.Materializer
	calls atomic.Int32
}

func (m *managedCloneMaterializer) Materialize(ctx context.Context, s skill.Skill) (string, error) {
	m.calls.Add(1)
	return m.inner.Materialize(ctx, s)
}

func managedCloneSourceDigest(t *testing.T, dir string) string {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(hash, "%s:%s\n", rel, info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hash.Write([]byte(target))
		} else if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			hash.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
func managedCloneRun(t *testing.T, ctx context.Context, r adaptor.Runner, stream bool) (*adaptor.Result, error) {
	t.Helper()
	if !stream {
		return r.Run(ctx, "zero CLI fixture")
	}
	s := r.Stream(ctx, "zero CLI fixture")
	for range s.Events() {
	}
	return s.Result()
}

func TestAlignmentProfileManagedClone(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, managed := range []bool{false, true} {
				t.Run(fmt.Sprintf("dedicated=%v/stream=%v/managed=%v", dedicated, stream, managed), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					home, cache := t.TempDir(), t.TempDir()
					source := filepath.Join(home, ".codebuddy")
					t.Setenv(skill.SkillCacheRootEnv, cache)
					materializer := &managedCloneMaterializer{inner: skill.NewDefaultSkillMaterializer(skill.WithSkillCacheRoot(cache))}
					store := memory.NewStore()
					opts := []adaptor.Option{adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("clone/v1"))), adaptor.WithWorkspace(t.TempDir()), adaptor.WithSkillMaterializer(materializer), adaptor.WithThreadStore(store)}
					if dedicated {
						opts = append(opts, adaptor.WithProfile(profile.Dedicated(source)))
					}
					if managed {
						opts = append(opts, adaptor.WithSkills(skill.Inline("fixture", "---\nname: fixture\ndescription: fixture\n---\nSynthetic fixture instructions.")))
					}
					d := newManagedCloneProfileDriver(home, source)
					a := adaptor.New(d, opts...)
					t.Cleanup(func() { _ = a.Close(context.Background()) })
					if _, err := a.SyncProfile(ctx); err != nil {
						t.Fatal(err)
					}
					before := managedCloneSourceDigest(t, source)
					calls := materializer.calls.Load()
					result, err := managedCloneRun(t, ctx, a.Thread("managed-clone"), stream)
					if err != nil || result == nil || result.Text != "zero CLI reached" || d.entered.Load() != 1 {
						t.Fatalf("public SyncProfile → WithTools failed: %v; entered=%d", err, d.entered.Load())
					}
					if d.injected.Load() != 1 || (managed && materializer.calls.Load() != calls+1) {
						t.Fatal("resolution/injection was repeated")
					}
					if managed && managedCloneSourceDigest(t, source) != before {
						t.Fatal("execution modified source profile")
					}
					if err := a.Close(ctx); err != nil {
						t.Fatal(err)
					}
					if dedicated {
						next := newManagedCloneProfileDriver(home, source)
						cold := adaptor.New(next, opts...)
						t.Cleanup(func() { _ = cold.Close(context.Background()) })
						if _, err := managedCloneRun(t, ctx, cold.Thread("managed-clone", adaptor.ResumeOnly()), stream); err != nil {
							t.Fatalf("cold ResumeOnly: %v", err)
						}
						req := next.request()
						if req.Session == nil || req.Session.State == nil || req.Session.State.ResumeID != "fixture-session" || next.entered.Load() != 1 {
							t.Fatal("cold call did not resume exactly once")
						}
						if req.Profile.Dir != d.request().Profile.Dir {
							t.Fatal("cold execution profile changed")
						}
					}
				})
			}
		}
	}
}

func TestAlignmentProfileManagedCloneDrift(t *testing.T) {
	for _, kind := range []string{"bytes", "mode", "attachment", "marker"} {
		t.Run(kind, func(t *testing.T) {
			home, cache := t.TempDir(), t.TempDir()
			source := filepath.Join(home, ".codebuddy")
			t.Setenv(skill.SkillCacheRootEnv, cache)
			d := newManagedCloneProfileDriver(home, source)
			a := adaptor.New(d, adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("clone/v1"))), adaptor.WithSkills(skill.Inline("fixture", "---\nname: fixture\n---\nOriginal")), adaptor.WithSkillMaterializer(skill.NewDefaultSkillMaterializer(skill.WithSkillCacheRoot(cache))))
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			if _, err := a.SyncProfile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Thread("drift").Run(context.Background(), "seed"); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(d.request().Profile.Dir, "skills", "fixture")
			file := filepath.Join(dir, "SKILL.md")
			switch kind {
			case "bytes":
				if err := os.WriteFile(file, []byte("user changed bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "attachment":
				if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("user attachment"), 0600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				mode := os.FileMode(0640)
				if runtime.GOOS == "windows" {
					mode = 0444
				}
				before, _ := os.Stat(file)
				if err := os.Chmod(file, mode); err != nil {
					t.Fatal(err)
				}
				after, _ := os.Stat(file)
				if before.Mode() == after.Mode() {
					t.Fatal("fixture failed to change observable mode")
				}
				t.Cleanup(func() { _ = os.Chmod(file, 0600) })
			case "marker":
				if err := os.WriteFile(filepath.Join(dir, ".agent-adaptor-source-path"), []byte(filepath.Join(cache, "different-source")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Thread("drift", adaptor.ResumeOnly()).Run(context.Background(), "must reject"); err == nil {
				t.Fatal("copied drift accepted")
			}
			if d.entered.Load() != 1 {
				t.Fatal("driver entered despite drift")
			}
		})
	}
}

type managedCloneErrorDriver struct {
	*alignmentProfileDriver
	cause    error
	embedded bool
}

func (d *managedCloneErrorDriver) GetProfile(ctx context.Context, cfg any, id driver.AgentIdentity, s *driver.ProfileSelection) (driver.AgentProfile, error) {
	if s != nil && s.Mode == driver.ProfileModeClone {
		if d.embedded {
			return driver.AgentProfile{Supported: true, Dir: s.Dir, Error: d.cause.Error()}, nil
		}
		return driver.AgentProfile{}, d.cause
	}
	return d.alignmentProfileDriver.GetProfile(ctx, cfg, id, s)
}
func TestAlignmentProfileManagedCloneErrors(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		for _, embedded := range []bool{false, true} {
			t.Run(fmt.Sprintf("dedicated=%v/embedded=%v", dedicated, embedded), func(t *testing.T) {
				sentinel := errors.New("fixture original profile cause")
				cause := &os.PathError{Op: "read", Path: "synthetic", Err: sentinel}
				source := t.TempDir()
				d := &managedCloneErrorDriver{alignmentProfileDriver: newAlignmentProfileDriver(source), cause: cause, embedded: embedded}
				opts := []adaptor.Option{adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("clone/v1")))}
				if dedicated {
					opts = append(opts, adaptor.WithProfile(profile.Dedicated(source)))
				}
				a := adaptor.New(d, opts...)
				t.Cleanup(func() { _ = a.Close(context.Background()) })
				_, err := a.Run(context.Background(), "must not execute")
				if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
					t.Fatalf("original cause lost: %v", err)
				}
				if !embedded {
					var pathErr *os.PathError
					if !errors.Is(err, sentinel) || !errors.As(err, &pathErr) {
						t.Fatal("Go error identity lost")
					}
				}
				if len(d.fakeDriver.requests) != 0 {
					t.Fatal("driver executed after profile failure")
				}
			})
		}
	}
}
