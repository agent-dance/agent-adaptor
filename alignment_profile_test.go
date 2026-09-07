package adaptor_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/tool"
)

type alignmentProfileDriver struct {
	*fakeDriver
	source string
}

func newAlignmentProfileDriver(source string) *alignmentProfileDriver {
	f := newFakeDriver()
	desc := f.Descriptor()
	desc.Type = "claude"
	desc.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	f.descriptor = &desc
	d := &alignmentProfileDriver{fakeDriver: f, source: source}
	f.runFunc = func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if _, err := mcpruntime.SyncResource(ctx, "claude", req.Profile.Dir, mcpruntime.ProfileKindHostManaged, req.MCP); err != nil {
			return driver.Response{}, err
		}
		id, nonce := "", ""
		if req.Session != nil && req.Session.State != nil {
			id = req.Session.State.ResumeID
			nonce = req.Session.State.Data["nonce"]
			raw, err := os.ReadFile(filepath.Join(req.Profile.Dir, "projects", id+".jsonl"))
			if err != nil || string(raw) != nonce {
				return driver.Response{}, &engine.ResumeRejectedError{Reason: "fixture session missing or nonce mismatch"}
			}
		} else {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return driver.Response{}, err
			}
			id = hex.EncodeToString(b)
			if _, err := rand.Read(b); err != nil {
				return driver.Response{}, err
			}
			nonce = hex.EncodeToString(b)
			if err := os.MkdirAll(filepath.Join(req.Profile.Dir, "projects"), 0700); err != nil {
				return driver.Response{}, err
			}
			if err := os.WriteFile(filepath.Join(req.Profile.Dir, "projects", id+".jsonl"), []byte(nonce), 0600); err != nil {
				return driver.Response{}, err
			}
		}
		return driver.Response{Output: "verified session", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: id, DisplayID: id, Data: map[string]string{"nonce": nonce}}}}, nil
	}
	return d
}
func (d *alignmentProfileDriver) GetProfile(_ context.Context, _ any, _ driver.AgentIdentity, s *driver.ProfileSelection) (driver.AgentProfile, error) {
	r, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{Selection: s, DefaultDir: d.source, NativeSharedDir: d.source, EnvVar: "ALIGNMENT_TEST_PROFILE", SettingsFiles: []string{"settings.json", "config.json"}, MCPFiles: []string{".claude.json"}, SkillsDirs: []string{"skills"}, AuthFiles: []string{".credentials.json"}})
	return r.Profile, err
}
func TestAlignmentProfileDedicatedColdResume(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			store := memory.NewStore()
			makeAgent := func(d *alignmentProfileDriver) *adaptor.Agent {
				return adaptor.New(d, adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithThreadStore(store), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("v1"))))
			}
			firstD := newAlignmentProfileDriver(source)
			first := makeAgent(firstD)
			t.Cleanup(func() { _ = first.Close(context.Background()) })
			run := func(a *adaptor.Agent) {
				t.Helper()
				th := a.Thread("opaque:/中文\x00key")
				var err error
				if stream {
					s := th.Stream(context.Background(), "verify")
					for range s.Events() {
					}
					_, err = s.Result()
				} else {
					_, err = th.Run(context.Background(), "verify")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			run(first)
			r1 := firstD.request(t, 0)
			if err := first.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			files, err := os.ReadDir(filepath.Join(r1.Profile.Dir, "projects"))
			if err != nil || len(files) != 1 {
				t.Fatalf("Close removed provider session: entries=%d err=%v", len(files), err)
			}
			secondD := newAlignmentProfileDriver(source)
			second := makeAgent(secondD)
			t.Cleanup(func() { _ = second.Close(context.Background()) })
			run(second)
			r2 := secondD.request(t, 0)
			if r2.Profile.Dir != r1.Profile.Dir || r2.Session == nil || r2.Session.State == nil || r2.Session.State.ResumeID+".jsonl" != files[0].Name() {
				t.Fatal("cold run did not read the original session")
			}
			endpoint, err := url.Parse(r1.MCP.Servers[0].URL)
			if err != nil {
				t.Fatal(err)
			}
			if conn, err := net.DialTimeout("tcp", endpoint.Host, 100*time.Millisecond); err == nil {
				conn.Close()
				t.Fatal("old endpoint remains reachable")
			}
			entries, err := os.ReadDir(source)
			if err != nil || len(entries) != 0 {
				t.Fatal("source profile was modified")
			}
			raw, err := os.ReadFile(filepath.Join(r2.Profile.Dir, ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), r1.MCP.Servers[0].URL) || strings.Contains(string(raw), r1.MCP.Servers[0].BearerTokenEnvVar) {
				t.Fatal("old hosted projection survived")
			}

			if r2.MCP.Servers[0].URL == r1.MCP.Servers[0].URL || r2.MCP.Servers[0].BearerTokenEnvVar == r1.MCP.Servers[0].BearerTokenEnvVar || r2.Runtime.SecretEnv[0].Value == r1.Runtime.SecretEnv[0].Value {
				t.Fatal("credentials did not rotate")
			}
		})
	}
}

func alignmentSource(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	return source
}
func alignmentAgent(d driver.Driver, source string, opts ...adaptor.Option) *adaptor.Agent {
	base := []adaptor.Option{adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("v1")))}
	return adaptor.New(d, append(base, opts...)...)
}
func TestAlignmentProfileSameProcessConflict(t *testing.T) {
	source := alignmentSource(t)
	d1, d2 := newAlignmentProfileDriver(source), newAlignmentProfileDriver(source)
	a, b := alignmentAgent(d1, source), alignmentAgent(d2, source)
	t.Cleanup(func() { _ = a.Close(context.Background()); _ = b.Close(context.Background()) })
	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := b.Run(context.Background(), "conflict"); !errors.Is(err, profile.ErrInUse) {
		t.Fatal(err)
	}
	if d2.runCount() != 0 || time.Since(start) > time.Second {
		t.Fatal("conflict dispatched or waited")
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(context.Background(), "take over"); err != nil {
		t.Fatal(err)
	}
}
func TestAlignmentProfileIdentityIsolation(t *testing.T) {
	source := alignmentSource(t)
	paths := map[string]bool{}
	for _, identity := range []adaptor.Identity{{}, {ID: "x"}, {Tenant: "x"}, {Profile: "x"}, {Name: "x"}, {ID: "a\x00b/c中文"}} {
		d := newAlignmentProfileDriver(source)
		a := alignmentAgent(d, source, adaptor.WithIdentity(identity))
		t.Cleanup(func() { _ = a.Close(context.Background()) })
		if _, err := a.Run(context.Background(), "run"); err != nil {
			t.Fatal(err)
		}
		path := d.request(t, 0).Profile.Dir
		if paths[path] {
			t.Fatal("identity reused execution directory")
		}
		paths[path] = true
		if strings.Contains(path, "a\x00b/c中文") {
			t.Fatal("identity leaked to path")
		}
	}
}
func TestAlignmentProfileNativeCleanup(t *testing.T) {
	for _, mode := range []string{"native", "default", "clone", "clone-native"} {
		t.Run(mode, func(t *testing.T) {
			source := alignmentSource(t)
			var selection profile.Selection
			switch mode {
			case "native":
				selection = profile.Native()
			case "default":
				selection = profile.Default()
			case "clone":
				selection = profile.CloneFrom(source, filepath.Join(t.TempDir(), "clone"))
			case "clone-native":
				selection = profile.CloneNative(filepath.Join(t.TempDir(), "clone"))
			}
			d := newAlignmentProfileDriver(source)
			a := adaptor.New(d, adaptor.WithProfile(selection), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("v1"))))
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			if _, err := a.Run(context.Background(), "run"); err != nil {
				t.Fatal(err)
			}
			req := d.request(t, 0)
			if !strings.HasPrefix(filepath.Base(req.Profile.Dir), "agent-adaptor-tool-profile-") {
				t.Fatal("non-dedicated became persistent")
			}
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(req.Profile.Dir); !os.IsNotExist(err) {
				t.Fatal("temporary clone retained")
			}
			if _, err := os.Stat(source); err != nil {
				t.Fatal("source removed")
			}
		})
	}
}
func TestAlignmentProfileMissingHistoricalSession(t *testing.T) {
	for _, resumeOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(resumeOnly), func(t *testing.T) {
			source := alignmentSource(t)
			store := memory.NewStore()
			d := newAlignmentProfileDriver(source)
			a := alignmentAgent(d, source, adaptor.WithThreadStore(store))
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			key := "history:/opaque"
			if _, err := a.Thread(key).Run(context.Background(), "first"); err != nil {
				t.Fatal(err)
			}
			before := activeRecord(t, store, key)
			dir := d.request(t, 0).Profile.Dir
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(dir, "projects")); err != nil {
				t.Fatal(err)
			}
			nextD := newAlignmentProfileDriver(source)
			b := alignmentAgent(nextD, source, adaptor.WithThreadStore(store))
			t.Cleanup(func() { _ = b.Close(context.Background()) })
			th := b.Thread(key)
			if resumeOnly {
				th = b.Thread(key, adaptor.ResumeOnly())
			}
			_, err := th.Run(context.Background(), "continue")
			if resumeOnly {
				if !errors.Is(err, adaptor.ErrResumeRejected) || nextD.runCount() != 1 {
					t.Fatalf("resume-only result=%v calls=%d", err, nextD.runCount())
				}
				after := activeRecord(t, store, key)
				if after.ID != before.ID || after.State.ResumeID != before.State.ResumeID {
					t.Fatal("rejection replaced healthy record")
				}
			} else {
				if err != nil || nextD.runCount() != 2 {
					t.Fatalf("fallback result=%v calls=%d", err, nextD.runCount())
				}
				after := activeRecord(t, store, key)
				if after.ID == before.ID || after.State.ResumeID == before.State.ResumeID {
					t.Fatal("fallback did not atomically replace old session")
				}
			}
		})
	}
}
func TestAlignmentProfileStaleCarrierAndMCPConflict(t *testing.T) {
	for _, mode := range []string{"external-key", "rendered-tamper", "carrier-alias", "valid-stale"} {
		t.Run(mode, func(t *testing.T) {
			source := alignmentSource(t)
			d := newAlignmentProfileDriver(source)
			a := alignmentAgent(d, source)
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			if _, err := a.Run(context.Background(), "first"); err != nil {
				t.Fatal(err)
			}
			req := d.request(t, 0)
			path := filepath.Join(req.Profile.Dir, ".claude.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			servers := config["mcpServers"].(map[string]any)
			owned := servers[toolidentity.ServerKey]
			switch mode {
			case "external-key":
				if err := os.Remove(filepath.Join(req.Profile.Dir, profilestate.ManifestName)); err != nil {
					t.Fatal(err)
				}
			case "rendered-tamper":
				owned.(map[string]any)["url"] = "https://external.invalid/mcp"
			case "carrier-alias":
				servers["external"] = map[string]any{"type": "http", "url": "https://external.invalid", "headers": map[string]any{"Authorization": "Bearer ${" + req.MCP.Servers[0].BearerTokenEnvVar + "}"}}
			case "valid-stale":
			}
			changed, _ := json.Marshal(config)
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = a.Run(context.Background(), "second")
			if mode == "valid-stale" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, adaptor.ErrInvalidMCPConfig) && !errors.Is(err, profile.ErrUnsafe) {
					t.Fatalf("unsafe projection accepted: %v", err)
				}
				if d.runCount() != 1 {
					t.Fatal("conflict dispatched")
				}
			}
			if err := a.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "rendered-tamper" {
				preserved, _ := os.ReadFile(path)
				if !strings.Contains(string(preserved), "external.invalid") {
					t.Fatal("modified external entry was removed")
				}
			}
		})
	}
}
func TestAlignmentProfileMaterializedDriftIsConservative(t *testing.T) {
	source := alignmentSource(t)
	store := memory.NewStore()
	d := newAlignmentProfileDriver(source)
	a := alignmentAgent(d, source, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	if _, err := a.Thread("drift").Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.request(t, 0).Profile.Dir, "settings.json"), []byte(`{"new_provider_setting":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Thread("drift", adaptor.ResumeOnly()).Run(context.Background(), "second"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("unknown materialized configuration did not reject resume: %v", err)
	}
	if d.runCount() != 1 {
		t.Fatal("incompatible state dispatched")
	}
}

// A dynamic provider keeps its declaration/path stable while the actual source changes.
type alignmentDynamicSkills struct {
	dir     string
	content string
	calls   int
}

func (p *alignmentDynamicSkills) GetSkills(_ context.Context, _ []string) (map[string]driver.Skill, error) {
	p.calls++
	if err := os.MkdirAll(p.dir, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(p.dir, "SKILL.md"), []byte(p.content), 0600); err != nil {
		return nil, err
	}
	return map[string]driver.Skill{"dynamic": {Key: "dynamic", Source: skill.PathSource{Path: p.dir}}}, nil
}

type alignmentSkillProfileDriver struct {
	*alignmentProfileDriver
	deferred    bool
	afterInject func(driver.ResolvedSkills, *driver.ProfileSelection) error
}

func (d *alignmentSkillProfileDriver) ListSkills(context.Context, any, driver.ResolvedSkills, []string, []driver.Skill, *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	return driver.SkillSnapshot{}, nil
}
func (d *alignmentSkillProfileDriver) SyncSkills(context.Context, any, driver.ResolvedSkills, []string, []driver.Skill, *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	return driver.SkillSnapshot{}, nil
}
func (d *alignmentSkillProfileDriver) InjectSkills(ctx context.Context, _ any, p driver.ResolvedSkills, s *driver.ProfileSelection) error {
	if d.deferred {
		return nil
	}
	_, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{ProfileDir: s.Dir, SkillsHome: filepath.Join(s.Dir, "skills"), Payload: p, ConflictMode: skillruntime.ProfileSkillConflictError, PruneMode: skillruntime.ProfileSkillPruneManaged})
	if err == nil && d.afterInject != nil {
		err = d.afterInject(p, s)
	}
	return err
}
func TestAlignmentProfileDynamicResolvedSnapshot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			source := alignmentSource(t)
			provider := &alignmentDynamicSkills{dir: t.TempDir(), content: "first"}
			store := memory.NewStore()
			makeAgent := func() (*adaptor.Agent, *alignmentSkillProfileDriver) {
				d := &alignmentSkillProfileDriver{alignmentProfileDriver: newAlignmentProfileDriver(source)}
				a := adaptor.New(d, adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithThreadStore(store), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("v1"))), adaptor.WithSkillProvider(provider), adaptor.WithSkills(driver.SkillKey("dynamic")))
				t.Cleanup(func() { _ = a.Close(context.Background()) })
				return a, d
			}
			first, _ := makeAgent()
			if _, err := alignmentCall(t, first.Thread("dynamic"), context.Background(), stream); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			second, d := makeAgent()
			if _, err := alignmentCall(t, second.Thread("dynamic", adaptor.ResumeOnly()), context.Background(), stream); err != nil {
				t.Fatalf("same resolved contents must cold resume: %v", err)
			}
			if d.request(t, 0).Session.State == nil {
				t.Fatal("cold request lost checkpoint")
			}
			provider.content = "changed"
			_, err := alignmentCall(t, second.Thread("dynamic", adaptor.ResumeOnly()), context.Background(), stream)
			if !errors.Is(err, adaptor.ErrThreadIncompatible) {
				t.Fatalf("changed actual skill contents must reject resume: %v", err)
			}
			if provider.calls != 3 || d.runCount() != 1 {
				t.Fatalf("resolver/dispatch counts=%d/%d", provider.calls, d.runCount())
			}
		})
	}
}

type alignmentMaterializer struct {
	dir      string
	contents map[string]string
	calls    atomic.Int32
}

func (m *alignmentMaterializer) Materialize(_ context.Context, s skill.Skill) (string, error) {
	m.calls.Add(1)
	dir := filepath.Join(m.dir, s.Key)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(m.contents[s.Key]), 0600)
}

func TestAlignmentProfileDeferredMaterializerColdSnapshot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			source := alignmentSource(t)
			store := memory.NewStore()
			materializer := &alignmentMaterializer{dir: t.TempDir(), contents: map[string]string{"dynamic": "one"}}
			makeAgent := func() (*adaptor.Agent, *alignmentSkillProfileDriver) {
				d := &alignmentSkillProfileDriver{alignmentProfileDriver: newAlignmentProfileDriver(source), deferred: true}
				run := d.runFunc
				d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
					_, err := skillruntime.ReconcileProfileSkills(ctx, skillruntime.ProfileSkillReconcileOptions{ProfileDir: req.Profile.Dir, SkillsHome: filepath.Join(req.Profile.Dir, "skills"), Payload: req.Skills, ConflictMode: skillruntime.ProfileSkillConflictError, PruneMode: skillruntime.ProfileSkillPruneManaged})
					if err != nil {
						return driver.Response{}, err
					}
					return run(ctx, req, sink)
				}
				a := alignmentAgent(d, source, adaptor.WithThreadStore(store), adaptor.WithSkillMaterializer(materializer), adaptor.WithSkills(skill.Skill{Key: "dynamic", Source: skill.PathSource{Path: "declaration-only"}}))
				t.Cleanup(func() { _ = a.Close(context.Background()) })
				return a, d
			}
			first, d1 := makeAgent()
			if _, err := alignmentCall(t, first.Thread("deferred"), context.Background(), stream); err != nil {
				t.Fatal(err)
			}
			if err := first.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			second, d2 := makeAgent()
			if _, err := alignmentCall(t, second.Thread("deferred", adaptor.ResumeOnly()), context.Background(), stream); err != nil {
				t.Fatal("cold deferred materialization", err)
			}
			if d1.request(t, 0).ProfilePayload.SessionFingerprint() != d2.request(t, 0).ProfilePayload.SessionFingerprint() {
				t.Fatal("cold process guard changed")
			}
			if err := os.RemoveAll(filepath.Join(materializer.dir, "dynamic")); err != nil {
				t.Fatal(err)
			}
			if _, err := alignmentCall(t, second.Thread("deferred", adaptor.ResumeOnly()), context.Background(), stream); err != nil {
				t.Fatal("resolver could not restore evicted cache", err)
			}
			materializer.contents["dynamic"] = "changed"
			if _, err := alignmentCall(t, second.Thread("deferred", adaptor.ResumeOnly()), context.Background(), stream); !errors.Is(err, adaptor.ErrThreadIncompatible) {
				t.Fatal("materializer content drift", err)
			}
			if materializer.calls.Load() != 4 || d2.runCount() != 2 {
				t.Fatal(materializer.calls.Load(), d2.runCount())
			}
		})
	}
}

func TestAlignmentProfileFinalSnapshotRejectsDriftAndIO(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"mode", "configuration", "linked-content", "missing-content", "invalid-json"} {
			t.Run(fmt.Sprintf("%s/%v", mode, stream), func(t *testing.T) {
				source := alignmentSource(t)
				store := newObservingStore()
				provider := &alignmentDynamicSkills{dir: t.TempDir(), content: "same"}
				d := &alignmentSkillProfileDriver{alignmentProfileDriver: newAlignmentProfileDriver(source)}
				a := alignmentAgent(d, source, adaptor.WithThreadStore(store), adaptor.WithSkillProvider(provider), adaptor.WithSkills(skill.Key("dynamic")))
				t.Cleanup(func() { _ = a.Close(context.Background()) })
				if _, err := alignmentCall(t, a.Thread("drift"), context.Background(), stream); err != nil {
					t.Fatal(err)
				}
				before := store.callCount()
				d.afterInject = func(_ driver.ResolvedSkills, s *driver.ProfileSelection) error {
					switch mode {
					case "mode":
						return os.Chmod(filepath.Join(provider.dir, "SKILL.md"), 0700)
					case "configuration":
						return os.WriteFile(filepath.Join(s.Dir, "settings.json"), []byte(`{"unknown":{"enabled":true}}`), 0600)
					case "linked-content":
						return os.Symlink(filepath.Join(provider.dir, "SKILL.md"), filepath.Join(provider.dir, "unsafe"))
					case "missing-content":
						return os.RemoveAll(provider.dir)
					default:
						return os.WriteFile(filepath.Join(s.Dir, "settings.json"), []byte(`{"bad":`), 0600)
					}
				}
				_, err := alignmentCall(t, a.Thread("drift", adaptor.ResumeOnly()), context.Background(), stream)
				if err == nil || d.runCount() != 1 {
					t.Fatal("unsafe resolved view dispatched", err)
				}
				if mode == "mode" || mode == "configuration" {
					if !errors.Is(err, adaptor.ErrThreadIncompatible) {
						t.Fatal(err)
					}
				} else if store.callCount() != before {
					t.Fatal("snapshot IO failure touched Thread store")
				}
				if provider.calls != 2 {
					t.Fatal("resolver reran", provider.calls)
				}
			})
		}
	}
}

func TestAlignmentProfileConcurrentThreadSnapshots(t *testing.T) {
	source := alignmentSource(t)
	store := memory.NewStore()
	m := &alignmentMaterializer{dir: t.TempDir(), contents: map[string]string{"alpha": "A", "beta": "B"}}
	d := &alignmentSkillProfileDriver{alignmentProfileDriver: newAlignmentProfileDriver(source)}
	a := alignmentAgent(d, source, adaptor.WithThreadStore(store), adaptor.WithSkillMaterializer(m))
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	var wg sync.WaitGroup
	for round := 0; round < 2; round++ {
		for _, key := range []string{"alpha", "beta"} {
			wg.Add(1)
			go func(key string) {
				defer wg.Done()
				th := a.Thread(key)
				if round == 1 {
					th = a.Thread(key, adaptor.ResumeOnly())
				}
				if _, err := th.Run(context.Background(), key, adaptor.WithSkills(skill.Skill{Key: key, Source: skill.PathSource{Path: "declaration-only"}})); err != nil {
					t.Error(err)
				}
			}(key)
		}
		wg.Wait()
	}
	if m.calls.Load() != 4 || d.runCount() != 4 {
		t.Fatal(m.calls.Load(), d.runCount())
	}
	for i := 0; i < 4; i++ {
		req := d.request(t, i)
		if len(req.Skills.Entries) != 1 || req.Skills.Entries[0].Key != req.Prompt {
			t.Fatal("different Thread resource snapshot crossed", req.Prompt, req.Skills)
		}
	}
}

func TestAlignmentProfileDeclaredMCPOwnershipProof(t *testing.T) {
	for _, mode := range []string{"tamper", "missing-proof", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			source := alignmentSource(t)
			d := newAlignmentProfileDriver(source)
			store := newObservingStore()
			a := alignmentAgent(d, source, adaptor.WithThreadStore(store), adaptor.WithMCP(mcp.Server{Key: "ordinary", Transport: mcp.TransportHTTP, URL: "https://example.invalid/mcp"}))
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			if _, err := a.Thread("mcp-proof").Run(context.Background(), "first"); err != nil {
				t.Fatal(err)
			}
			dir := d.request(t, 0).Profile.Dir
			if mode == "missing-proof" {
				manifest, err := profilestate.LoadManifest(dir)
				if err != nil {
					t.Fatal(err)
				}
				entry, ok := manifest.Entry("mcp", "ordinary")
				if !ok {
					t.Fatal("missing fixture MCP entry")
				}
				delete(entry.Metadata, "rendered_fingerprint")
				manifest.Set(entry)
				if err := profilestate.SaveManifest(dir, manifest); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(dir, ".claude.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var root map[string]any
				if err := json.Unmarshal(raw, &root); err != nil {
					t.Fatal(err)
				}
				entry := root["mcpServers"].(map[string]any)["ordinary"].(map[string]any)
				if mode == "tamper" {
					entry["url"] = "https://changed.invalid/mcp"
				} else {
					entry["unknown"] = "unproved"
				}
				raw, _ = json.Marshal(root)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := store.callCount()
			if _, err := a.Thread("mcp-proof", adaptor.ResumeOnly()).Run(context.Background(), "second"); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatal("ordinary same-key modification was normalized", err)
			}
			if store.callCount() != before || d.runCount() != 1 {
				t.Fatal("unproved MCP reached store or Driver")
			}
		})
	}
}

func TestAlignmentProfileDistinctIdentitiesRunIndependently(t *testing.T) {
	source := alignmentSource(t)
	d := newAlignmentProfileDriver(source)
	entered := make(chan driver.Request, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	run := d.runFunc
	d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		entered <- req
		select {
		case <-release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		return run(ctx, req, sink)
	}
	a := alignmentAgent(d, source)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	streams := []adaptor.Stream{a.Stream(context.Background(), "first", adaptor.WithIdentity(adaptor.Identity{ID: "one"})), a.Stream(context.Background(), "second", adaptor.WithIdentity(adaptor.Identity{ID: "two"}))}
	defer func() {
		for _, s := range streams {
			s.Cancel()
		}
	}()
	var reqs []driver.Request
	for len(reqs) < 2 {
		select {
		case req := <-entered:
			reqs = append(reqs, req)
		case <-time.After(time.Second):
			t.Fatal("unrelated hosted profiles serialized")
		}
	}
	if reqs[0].Profile.Dir == reqs[1].Profile.Dir {
		t.Fatal("distinct identity profiles not isolated")
	}
	unblock()
	for _, s := range streams {
		for range s.Events() {
		}
		if _, err := s.Result(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlignmentProfileCloseCancelsWriterAndWaiter(t *testing.T) {
	source := alignmentSource(t)
	d := newAlignmentProfileDriver(source)
	entered := make(chan struct{})
	d.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(entered)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}
	a := alignmentAgent(d, source)
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	first := a.Stream(context.Background(), "writer")
	<-entered
	second := a.Stream(context.Background(), "waiter")
	if _, ok := (<-second.Events()).(adaptor.RunStarted); !ok {
		t.Fatal("waiter not admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatal("Close could not cancel profile gate", err)
	}
	for _, st := range []adaptor.Stream{first, second} {
		for range st.Events() {
		}
		if _, err := st.Result(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if d.runCount() != 1 {
		t.Fatal("waiter dispatched after Close", d.runCount())
	}
}
