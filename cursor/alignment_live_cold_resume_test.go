//go:build cursor_live

package cursor

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"maps"
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

// alignmentCursorColdDriver only records resolved inputs to the real driver.
// Embedding preserves its optional interfaces and configuration fingerprint;
// neither responses nor provider events/checkpoints are synthesized or replayed.
type alignmentCursorColdDriver struct {
	configuredDriver
	before  func(driver.Request) error
	profile string
	server  driver.MCPServerSpec
	token   string // Compared only in memory; never included in diagnostics.
	calls   int
}

func (d *alignmentCursorColdDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.calls++
	if req.Profile == nil || req.Profile.Mode != driver.ProfileModeDedicated || req.Profile.Dir == "" {
		return driver.Response{}, errors.New("cold-resume probe requires resolved dedicated profile")
	}
	d.profile = req.Profile.Dir
	for _, server := range req.MCP.Servers {
		if server.Key == "agent-adaptor-tools" {
			d.server = server
			for _, binding := range req.Runtime.SecretEnv {
				if binding.Name == server.BearerTokenEnvVar {
					d.token = binding.Value
				}
			}
		}
	}
	if d.server.URL == "" || d.server.BearerTokenEnvVar == "" || d.token == "" {
		return driver.Response{}, errors.New("cold-resume probe requires real hosted-tool endpoint and credential")
	}
	if d.before != nil {
		if err := d.before(req); err != nil {
			return driver.Response{}, err
		}
	}
	return d.configuredDriver.Run(ctx, req, sink)
}

func alignmentCursorClose(t *testing.T, a *adaptor.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.Close(ctx); err != nil {
		t.Fatalf("bounded Agent.Close failed (%T)", err)
	}
}

// Hash only the test-owned tree, without following links or exposing file
// contents. Limits make an unexpected provider layout a bounded failing probe.
func alignmentCursorFileHashes(root string) (map[string][32]byte, error) {
	hashes := make(map[string][32]byte)
	entries, total := 0, int64(0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect isolated provider files")
		}
		entries++
		if entries > 4096 || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("isolated provider tree exceeds bounds or contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("isolated provider file is not regular")
		}
		total += info.Size()
		if total > 128<<20 {
			return errors.New("isolated provider files exceed byte limit")
		}
		file, err := os.Open(path)
		if err != nil {
			return errors.New("cannot read isolated provider file")
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return errors.New("isolated provider file changed during inspection")
		}
		if n > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return errors.New("cannot identify isolated provider file")
			}
			hashes[rel] = [32]byte(hash.Sum(nil))
		}
		return nil
	})
	return hashes, err
}

// The session identity comes exclusively from the formal public checkpoint.
// Find its exact directory name below the isolated effective profile, then
// require actual nonempty provider files. Unknown layouts fail the live probe;
// an SDK checkpoint or a test-written marker cannot replace this evidence.
func alignmentCursorSessionFiles(root, sessionID string) (string, map[string][32]byte, error) {
	if sessionID == "" || sessionID == "." || filepath.Base(sessionID) != sessionID {
		return "", nil, errors.New("session identity cannot address an isolated session directory")
	}
	dir, entries := "", 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot locate isolated provider session files")
		}
		entries++
		if entries > 4096 || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("isolated session search exceeds bounds or contains a symlink")
		}
		if entry.IsDir() && entry.Name() == sessionID {
			if dir != "" {
				return errors.New("ambiguous isolated session directory")
			}
			dir = path
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if dir == "" {
		return "", nil, errors.New("provider session file layout is not supported by the live fixture")
	}
	hashes, err := alignmentCursorFileHashes(dir)
	if err != nil || len(hashes) == 0 {
		return "", nil, errors.New("no readable nonempty provider session files")
	}
	return dir, hashes, nil
}

func alignmentCursorGatewayURL(t *testing.T, endpoint string) *url.URL {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil {
		t.Fatal("hosted gateway is not an isolated numeric loopback endpoint")
	}
	return u
}

// No proxy or redirects may carry the test credential outside loopback.
func alignmentCursorGatewayStatus(endpoint, token string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cursor-cold-resume-probe","version":"1"}}}`))
	if err != nil {
		return 0, errors.New("cannot construct gateway probe")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("gateway connection unavailable")
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

// TestAlignmentLiveCursorDedicatedToolsColdResume exercises the public
// lifecycle with two Agents. Cursor still spawns once per turn; the retained
// provider session files, not a resident process, must carry the conversation.
func TestAlignmentLiveCursorDedicatedToolsColdResume(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t) // Gate before any profile, nonce or CLI work.
	source := t.TempDir()
	store := memory.NewStore()
	identity := adaptor.Identity{ID: "cursor-cold-resume", Tenant: "alignment", Profile: "isolated"}
	const key = "cursor/live/dedicated-tools/cold-resume"
	const followupValue = "cold-resume-check"
	nonce := alignmentCursorNonce(t)
	var mu sync.Mutex
	var values []string
	probe := tool.Define("alignment_remember", "Acknowledge the supplied value without storing files or returning history.", func(_ context.Context, input struct {
		Value string `json:"value"`
	}) (map[string]string, error) {
		mu.Lock()
		values = append(values, input.Value)
		mu.Unlock()
		return map[string]string{"status": "acknowledged"}, nil
	}, tool.ReadOnly(), tool.Idempotent(), tool.Revision("cursor-cold-resume/v1"))
	newAgent := func(d *alignmentCursorColdDriver) *adaptor.Agent {
		a := adaptor.New(d, adaptor.WithWorkspace(cfg.CWD), adaptor.WithIdentity(identity), adaptor.WithThreadStore(store), adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithTools(probe), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
		t.Cleanup(func() { alignmentCursorClose(t, a) })
		return a
	}
	checkpoint := func(th *adaptor.Thread) *adaptor.Checkpoint {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cp, err := th.Checkpoint(ctx)
		if err != nil || cp == nil || !cp.Valid || cp.State == nil || cp.State.ResumeID == "" {
			t.Fatalf("public healthy checkpoint missing (%T)", err)
		}
		return cp
	}
	run := func(th *adaptor.Thread, prompt string) *adaptor.Result {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		r, err := th.Run(ctx, prompt)
		if err != nil || r == nil {
			t.Fatalf("public cold-resume run failed (%T)", err)
		}
		if r.Raw().Stdout == "" || r.Raw().Terminal == nil || len(r.Transcript()) == 0 || !strings.Contains(r.Text, nonce) {
			t.Fatal("public result lacks formal output or historical nonce")
		}
		return r
	}
	d1 := &alignmentCursorColdDriver{configuredDriver: Driver(cfg).(configuredDriver)}
	a := newAgent(d1)
	th1 := a.Thread(key)
	run(th1, "Remember this exact token in our conversation: "+nonce+". Call the actual alignment_remember MCP tool with that token as value, then reply with the token. Do not read or write files or use another tool.")
	cp1 := checkpoint(th1)
	mu.Lock()
	firstCalled := len(values) > 0 && values[0] == nonce
	values = nil // The tool can never return this history to the next Agent.
	mu.Unlock()
	if !firstCalled || d1.calls != 1 || d1.profile == source {
		t.Fatal("first public turn did not call the actual hosted tool in its owned profile")
	}
	record1, err := store.Resolve(context.Background(), threadstore.Query{Key: key})
	if err != nil || record1 == nil || record1.State == nil || record1.State.ResumeID != cp1.State.ResumeID {
		t.Fatal("first checkpoint was not saved under the public Thread key")
	}
	sessionDir, files, err := alignmentCursorSessionFiles(d1.profile, cp1.State.ResumeID)
	if err != nil {
		t.Fatal(err)
	}
	oldURL := alignmentCursorGatewayURL(t, d1.server.URL)
	alignmentCursorClose(t, a)
	retained, err := alignmentCursorFileHashes(sessionDir)
	if err != nil || !maps.Equal(files, retained) {
		t.Fatal("Agent.Close removed or changed actual provider session files")
	}
	if status, err := alignmentCursorGatewayStatus(d1.server.URL, d1.token); err == nil && status >= 200 && status < 300 {
		t.Fatal("closed Agent gateway still accepts its revoked credential")
	}
	// Reserve the released port so random port reuse cannot hide endpoint rotation.
	portGuard, err := net.Listen("tcp4", oldURL.Host)
	if err != nil {
		t.Fatal("closed Agent did not release its gateway listener")
	}
	defer portGuard.Close()
	d2 := &alignmentCursorColdDriver{configuredDriver: Driver(cfg).(configuredDriver)}
	d2.before = func(req driver.Request) error {
		if req.Session == nil || req.Session.Mode != driver.SessionContinueOnly || req.Session.State == nil || req.Session.State.ResumeID != cp1.State.ResumeID || strings.Contains(req.Prompt, nonce) || req.Profile.Dir != d1.profile {
			return errors.New("second public dispatch did not preserve ResumeOnly identity/profile or reinjected nonce")
		}
		retained, err := alignmentCursorFileHashes(sessionDir)
		if err != nil || !maps.Equal(files, retained) {
			return errors.New("provider session files were not retained until the cold resume dispatch")
		}
		return nil
	}
	b := newAgent(d2)
	th2 := b.Thread(key, adaptor.ResumeOnly())
	run(th2, "Recall the exact token from our prior conversation and include it in your final reply. Call the actual alignment_remember MCP tool with value '"+followupValue+"'. Do not put the historical token in the tool arguments. Do not read or write files or use another tool.")
	cp2 := checkpoint(th2)
	mu.Lock()
	secondCalled := len(values) > 0
	for _, value := range values {
		secondCalled = secondCalled && value == followupValue
	}
	mu.Unlock()
	record2, err := store.Resolve(context.Background(), threadstore.Query{Key: key})
	if !secondCalled || d2.calls != 1 || cp2.State.ResumeID != cp1.State.ResumeID || err != nil || record2 == nil || record2.ID != record1.ID || record2.CompatibilityFingerprint != record1.CompatibilityFingerprint {
		t.Fatal("cold resume did not retain the original conversation/store identity and actual tool access")
	}
	alignmentCursorGatewayURL(t, d2.server.URL)
	if d2.server.URL == d1.server.URL || d2.server.BearerTokenEnvVar == d1.server.BearerTokenEnvVar || d2.token == d1.token {
		t.Fatal("cold Agent did not rotate hosted endpoint and credentials")
	}
	if status, err := alignmentCursorGatewayStatus(d2.server.URL, d1.token); err != nil || status != http.StatusUnauthorized {
		t.Fatal("new gateway did not explicitly reject the old bearer credential")
	}
	_, secondFiles, err := alignmentCursorSessionFiles(d2.profile, cp2.State.ResumeID)
	if err != nil {
		t.Fatal(err)
	}
	alignmentCursorClose(t, b)
	retained, err = alignmentCursorFileHashes(sessionDir)
	if err != nil || !maps.Equal(secondFiles, retained) {
		t.Fatal("second Agent.Close removed or changed actual provider session files")
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatal("provider or hosted resources modified the original empty Dedicated source")
	}
	t.Log("real public Dedicated+WithTools cold resume retained session identity/files and historical nonce; hosted gateway and credentials rotated")
}

// This hermetic test validates only the file assertion helper, not live resume.
func TestAlignmentCursorColdResumeFileProbe(t *testing.T) {
	root := t.TempDir()
	const sessionID = "provider-session"
	if _, _, err := alignmentCursorSessionFiles(root, sessionID); err == nil {
		t.Fatal("missing provider directory accepted")
	}
	dir := filepath.Join(root, "chats", sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alignmentCursorSessionFiles(root, sessionID); err == nil {
		t.Fatal("empty provider directory accepted")
	}
	file := filepath.Join(dir, "store.db")
	if err := os.WriteFile(file, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	actualDir, before, err := alignmentCursorSessionFiles(root, sessionID)
	if err != nil || actualDir != dir || len(before) != 1 {
		t.Fatal("nonempty exact directory was not identified")
	}
	if err := os.WriteFile(file, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := alignmentCursorFileHashes(dir)
	if err != nil || maps.Equal(before, after) {
		t.Fatal("same-length file mutation was not detected")
	}
	if err := os.MkdirAll(filepath.Join(root, "other", sessionID), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := alignmentCursorSessionFiles(root, sessionID); err == nil {
		t.Fatal("ambiguous session directory accepted")
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := alignmentCursorFileHashes(dir); err == nil {
		t.Fatal("symlink accepted by isolated file probe")
	}
}
