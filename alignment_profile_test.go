package adaptor_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
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
