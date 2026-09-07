//go:build codebuddy_live

package codebuddy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/tool"
)

// This read-only probe retains the real configured Driver and all its optional
// interfaces. It never changes a request, supplies a catalog or makes a result.
type alignmentLiveDriverProbe struct {
	configuredDriver
	mu   sync.Mutex
	runs []alignmentLiveRunResources
}

type alignmentLiveRunResources struct {
	profileDir string
	resumeID   string
	mode       driver.SessionMode
	server     driver.MCPServerSpec
	token      string
}

func (d *alignmentLiveDriverProbe) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	var observed alignmentLiveRunResources
	if req.Profile != nil {
		observed.profileDir = req.Profile.Dir
	}
	if req.Session != nil {
		observed.mode = req.Session.Mode
		if req.Session.State != nil {
			observed.resumeID = req.Session.State.ResumeID
		}
	}
	if len(req.MCP.Servers) == 1 {
		observed.server = req.MCP.Servers[0]
		for _, binding := range req.Runtime.SecretEnv {
			if binding.Name == observed.server.BearerTokenEnvVar {
				observed.token = binding.Value
			}
		}
	}
	d.mu.Lock()
	d.runs = append(d.runs, observed)
	d.mu.Unlock()
	return d.configuredDriver.Run(ctx, req, sink)
}

func (d *alignmentLiveDriverProbe) onlyRun(t *testing.T) alignmentLiveRunResources {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.runs) != 1 {
		t.Fatalf("driver dispatches=%d, want one", len(d.runs))
	}
	return d.runs[0]
}

func alignmentLiveClose(t *testing.T, agent *adaptor.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := agent.Close(ctx); err != nil {
		t.Fatalf("bounded Agent.Close: %v", err)
	}
}

func alignmentLiveLoopback(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" {
		t.Fatal("hosted fixture endpoint is not private HTTP")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		t.Fatal("hosted fixture endpoint is not loopback")
	}
	return u
}

// CodeBuddy 2.137.1 SessionStore uses projects/<compressed workspace>/<id>.jsonl.
// Match the formal ID without duplicating the provider's path compression rule.
func alignmentLiveSessionFile(t *testing.T, dir, id, nonce string) string {
	t.Helper()
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "*?[]") {
		t.Fatal("provider session ID cannot identify one local session file")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(dir, "projects", "*", id+".jsonl"))
		if err == nil && len(matches) == 1 {
			raw, err := os.ReadFile(matches[0])
			if err == nil && strings.Contains(string(raw), nonce) {
				return matches[0]
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("formal provider session file with original tool nonce is missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func alignmentLiveMCPProjection(t *testing.T, resources alignmentLiveRunResources, old *alignmentLiveRunResources) {
	t.Helper()
	if resources.token == "" || resources.server.BearerTokenEnvVar == "" {
		t.Fatal("hosted MCP did not supply an owned credential")
	}
	found := false
	for _, name := range []string{".mcp.json", "mcp.json"} {
		raw, err := os.ReadFile(filepath.Join(resources.profileDir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal("cannot read isolated MCP projection")
		}
		text := string(raw)
		found = found || strings.Contains(text, resources.server.URL) && strings.Contains(text, resources.server.BearerTokenEnvVar)
		if strings.Contains(text, resources.token) {
			t.Fatal("MCP projection wrote the bearer value instead of its environment reference")
		}
		if old != nil && (strings.Contains(text, old.server.URL) || strings.Contains(text, old.server.BearerTokenEnvVar) || strings.Contains(text, old.token)) {
			t.Fatal("old hosted MCP projection survived resource rotation")
		}
	}
	if !found {
		t.Fatal("actual hosted MCP URL/environment reference missing from native profile")
	}
}

func alignmentLiveSessionID(t *testing.T, result *adaptor.Result, want string) {
	t.Helper()
	raw, err := json.Marshal(result.Raw().Terminal)
	var terminal struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		IsError   *bool  `json:"is_error"`
	}
	if err != nil || json.Unmarshal(raw, &terminal) != nil || terminal.SessionID != want || terminal.Type != "result" || terminal.Subtype != "success" || terminal.IsError == nil || *terminal.IsError {
		t.Fatal("formal healthy terminal does not identify the original provider session")
	}
}

func TestAlignmentLiveCodeBuddyDedicatedToolsResumeAfterClose(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	source, workspace, home := isolatedConfigDir(t), t.TempDir(), t.TempDir()
	store := memory.NewStore()
	identity := adaptor.Identity{ID: "alignment-codebuddy", Tenant: "r017", Profile: "cold-resume", Name: "acceptance"}
	key := "alignment/live/dedicated-after-close"
	var mu sync.Mutex
	generation, firstCalls, secondCalls, wrongCalls := 1, 0, 0, 0
	nonce := ""
	definition := tool.Define("alignment_history_probe", "In record phase return a new acceptance nonce; in verify phase return only READY.", func(_ context.Context, input struct {
		Phase string `json:"phase"`
	}) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if generation == 1 && input.Phase == "record" {
			firstCalls++
			if nonce == "" {
				var raw [24]byte
				if _, err := rand.Read(raw[:]); err != nil {
					return "", err
				}
				nonce = "R017_" + hex.EncodeToString(raw[:])
			}
			return nonce, nil
		}
		if generation == 2 && input.Phase == "verify" {
			secondCalls++
			return "READY", nil // The second Agent cannot fetch the nonce here.
		}
		wrongCalls++
		return "UNEXPECTED_PHASE", nil
	}, tool.ReadOnly(), tool.Revision("alignment-cold-resume/v1"))
	cfg := Config{CommonConfig: CommonConfig{Command: codebuddyCLIName(), CWD: workspace, Env: []driver.EnvBinding{
		{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "XDG_CONFIG_HOME", Value: filepath.Join(home, "xdg")},
	}}, Model: liveModel()}
	makeAgent := func() (*adaptor.Agent, *alignmentLiveDriverProbe) {
		probe := &alignmentLiveDriverProbe{configuredDriver: Driver(cfg).(configuredDriver)}
		a := adaptor.New(probe, adaptor.WithWorkspace(workspace), adaptor.WithThreadStore(store), adaptor.WithIdentity(identity), adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithTools(definition), adaptor.WithPolicy(livePolicyHeadless), adaptor.WithBlockingEvents())
		t.Cleanup(func() { alignmentLiveClose(t, a) })
		return a, probe
	}
	first, firstProbe := makeAgent()
	firstResult, _, err := collectLiveStream(ctx, first.Thread(key), "Call alignment_history_probe with phase record. Remember the returned random acceptance nonce only in this conversation. Do not write it to any file. Reply STORED.")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	remembered, calls, wrong := nonce, firstCalls, wrongCalls
	mu.Unlock()
	if calls == 0 || wrong != 0 || remembered == "" || firstResult.Raw().Terminal == nil {
		t.Fatal("first turn did not actually call the hosted tool and complete")
	}
	before, err := store.Resolve(ctx, threadstore.Query{Key: key})
	if err != nil || before == nil || before.State == nil || before.State.ResumeID == "" || before.Status != threadstore.StatusActive {
		t.Fatal("first turn did not save a healthy checkpoint")
	}
	alignmentLiveSessionID(t, firstResult, before.State.ResumeID)
	encoded, err := json.Marshal(before)
	if err != nil || strings.Contains(string(encoded), remembered) {
		t.Fatal("nonce entered host checkpoint instead of provider conversation")
	}
	firstResources := firstProbe.onlyRun(t)
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil || firstResources.profileDir == canonicalSource || firstResources.profileDir == "" {
		t.Fatal("Dedicated + WithTools did not use an owned execution profile")
	}
	file := alignmentLiveSessionFile(t, firstResources.profileDir, before.State.ResumeID, remembered)
	alignmentLiveMCPProjection(t, firstResources, nil)
	oldURL := alignmentLiveLoopback(t, firstResources.server.URL)
	alignmentLiveClose(t, first)
	if alignmentLiveSessionFile(t, firstResources.profileDir, before.State.ResumeID, remembered) != file {
		t.Fatal("Close did not retain the original provider session file")
	}
	if conn, err := net.DialTimeout("tcp", oldURL.Host, 300*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("closed Agent still accepts connections on its old tool gateway")
	}
	mu.Lock()
	generation = 2
	mu.Unlock()
	second, secondProbe := makeAgent()
	prompt := "Call alignment_history_probe with phase verify to check the new tool connection. Then recall the random acceptance nonce returned in the first turn of this conversation. Reply with exactly that nonce. Do not read files or ask any tool to reproduce it."
	if strings.Contains(prompt, remembered) {
		t.Fatal("recall prompt accidentally reinjected the nonce")
	}
	secondResult, _, err := collectLiveStream(ctx, second.Thread(key, adaptor.ResumeOnly()), prompt)
	if err != nil {
		t.Fatalf("new Agent ResumeOnly: %v", err)
	}
	mu.Lock()
	calls, wrong = secondCalls, wrongCalls
	mu.Unlock()
	if strings.TrimSpace(secondResult.Text) != remembered || calls == 0 || wrong != 0 || secondResult.Raw().Terminal == nil {
		t.Fatal("new Agent did not recall history and actually use its new hosted gateway")
	}
	after, err := store.Resolve(ctx, threadstore.Query{Key: key})
	if err != nil || after == nil || after.State == nil || after.ID != before.ID || after.State.ResumeID != before.State.ResumeID || after.CompatibilityFingerprint != before.CompatibilityFingerprint {
		t.Fatal("cold resume changed the original Thread/provider identity")
	}
	alignmentLiveSessionID(t, secondResult, before.State.ResumeID)
	secondResources := secondProbe.onlyRun(t)
	if secondResources.mode != driver.SessionContinueOnly || secondResources.resumeID != before.State.ResumeID || secondResources.profileDir != firstResources.profileDir {
		t.Fatal("new public Agent did not resume the original owned provider session")
	}
	if secondResources.server.URL == firstResources.server.URL || secondResources.server.BearerTokenEnvVar == firstResources.server.BearerTokenEnvVar || secondResources.token == firstResources.token {
		t.Fatal("new Agent reused old gateway credentials")
	}
	alignmentLiveMCPProjection(t, secondResources, &firstResources)
	alignmentLiveLoopback(t, secondResources.server.URL)
	requestCtx, requestCancel := context.WithTimeout(ctx, 2*time.Second)
	defer requestCancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, secondResources.server.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal("cannot construct old-credential rejection check")
	}
	req.Header.Set("Authorization", "Bearer "+firstResources.token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal("new private gateway unavailable for old-credential rejection check")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		t.Fatalf("new gateway did not reject old bearer: status=%d", response.StatusCode)
	}
	alignmentLiveClose(t, second)
	if alignmentLiveSessionFile(t, secondResources.profileDir, after.State.ResumeID, remembered) != file {
		t.Fatal("second Close did not preserve the provider history file")
	}
}
