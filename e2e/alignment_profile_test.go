package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/tool"
)

func alignmentTool(revision string) tool.Definition {
	return tool.Define("t20_echo", "Echo", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }, tool.ReadOnly(), tool.Revision(revision))
}
func TestAlignmentLifecycleProfileColdResume(t *testing.T) {
	for _, method := range []string{"Run", "Stream"} {
		t.Run(method, func(t *testing.T) {
			f := newAlignmentFixture(t, "cursor")
			store := memory.NewStore()
			opts := []adaptor.Option{adaptor.WithThreadStore(store), adaptor.WithTools(alignmentTool("one")), adaptor.WithIdentity(adaptor.Identity{ID: "a\x00:b", Tenant: "中文", Profile: "p", Name: "n"})}
			a := f.agent(t, opts...)
			ctx, c := alignmentContext(t)
			defer c()
			key := "host:key/\x00中"
			if _, e := a.Thread(key).Run(ctx, "first"); e != nil {
				t.Fatal(e)
			}
			first := f.wait(t, "prompt", 1)[0]
			before, _ := store.Resolve(ctx, threadstore.Query{Key: key})
			if first.Profile == f.profile || first.Carrier == "" || first.TokenHash == "" || first.Endpoint == "" {
				t.Fatalf("incomplete isolated profile observation: %+v", first)
			}
			oldToken, err := os.ReadFile(filepath.Join(f.log+".credentials", first.TokenHash))
			if err != nil || len(oldToken) == 0 {
				t.Fatal("private old credential missing")
			}
			alignmentGatewayCredential(t, first.Endpoint, string(oldToken), true)
			if e := alignmentClose(t, a, ctx); e != nil {
				t.Fatal(e)
			}
			session := filepath.Join(first.Profile, "projects", "session-t20.jsonl")
			b, e := os.ReadFile(session)
			if e != nil || !bytes.Contains(b, []byte("first")) {
				t.Fatalf("session not retained: %q %v", b, e)
			}
			// Source gets changed after seeding. The clone must not be reseeded.
			if e = os.WriteFile(filepath.Join(f.profile, "settings.json"), []byte(`{"theme":"changed-after-seed"}`), 0600); e != nil {
				t.Fatal(e)
			}
			next := f.agent(t, opts...)
			var r *adaptor.Result
			if method == "Run" {
				r, e = next.Thread(key, adaptor.ResumeOnly()).Run(ctx, "second")
			} else {
				s := next.Thread(key, adaptor.ResumeOnly()).Stream(ctx, "second")
				var ev []adaptor.Event
				r, e, ev = alignmentDrain(t, s)
				alignmentEnvelope(t, ev, s.RunID(), "")
			}
			if e != nil {
				t.Fatal(e)
			}
			if r.Text != "answer-text" {
				t.Errorf("Text=%q", r.Text)
			}
			second := f.wait(t, "prompt", 2)[1]
			after, _ := store.Resolve(ctx, threadstore.Query{Key: key})
			if second.Profile != first.Profile || second.Resume != "session-t20" || !strings.Contains(second.Previous, "first") || after.ID != before.ID || after.Key != key {
				t.Errorf("cold resume lost disk/record: first=%+v second=%+v before=%+v after=%+v", first, second, before, after)
			}
			if first.TokenHash == second.TokenHash || first.Carrier == second.Carrier {
				t.Error("cold credentials did not rotate")
			}
			if _, err := os.Stat(filepath.Join(second.Profile, "settings.json")); !os.IsNotExist(err) {
				t.Errorf("source reseeded: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.profile, "mcp.json")); !os.IsNotExist(err) {
				t.Error("source polluted")
			}
			alignmentGatewayCredential(t, first.Endpoint, string(oldToken), false)
			if e = alignmentClose(t, next, ctx); e != nil {
				t.Fatal(e)
			}
			raw, e := os.ReadFile(filepath.Join(second.Profile, "mcp.json"))
			if e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			if bytes.Contains(raw, []byte("agent-adaptor-tools")) || bytes.Contains(raw, []byte(second.Carrier)) {
				t.Error("owned projection survived Close")
			}
		})
	}
}

// A timeout is not revocation evidence. The exact formerly accepted credential
// must now be explicitly rejected, or its loopback listener must refuse connection.
func alignmentGatewayCredential(t *testing.T, endpoint, token string, accepted bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":77,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t20","version":"1"}}}`))
	if err != nil {
		t.Fatal("invalid fixture endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if !accepted && alignmentConnectionRefused(runtime.GOOS, err) {
			return
		}
		t.Fatalf("credential probe did not establish expected state accepted=%v: %v", accepted, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal("credential probe response did not complete")
	}
	if !accepted {
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("old authenticated endpoint remains available: status %d", resp.StatusCode)
		}
		return
	}
	var reply struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		Error json.RawMessage
	}
	// This gateway uses JSON responses for initialize; success proves this token
	// was actually accepted before Close, not merely present in a carrier.
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &reply) != nil || reply.Result.ProtocolVersion == "" || len(reply.Error) != 0 {
		t.Fatalf("old credential never authenticated: status %d", resp.StatusCode)
	}
}

func alignmentConnectionRefused(goos string, err error) bool {
	// Go's Windows socket errors retain the Winsock code, not the portable
	// syscall.ECONNREFUSED value. Only explicit refusal proves revocation;
	// timeout, cancellation and arbitrary transport errors remain failures.
	const wsaConnectionRefused = syscall.Errno(10061) // WSAECONNREFUSED
	return errors.Is(err, syscall.ECONNREFUSED) || (goos == "windows" && errors.Is(err, wsaConnectionRefused))
}

func TestAlignmentLifecycleProfileCredentialRefusalOracle(t *testing.T) {
	for _, tc := range []struct {
		name, goos string
		err        error
		want       bool
	}{
		{"posix-refused", "linux", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), true},
		{"winsock-refused", "windows", fmt.Errorf("connectex: %w", syscall.Errno(10061)), true},
		{"foreign-code", "linux", syscall.Errno(10061), false},
		{"timeout", "windows", context.DeadlineExceeded, false},
		{"cancelled", "windows", context.Canceled, false},
		{"error-text", "windows", errors.New("connection refused"), false},
		{"other-socket-error", "windows", syscall.Errno(10060), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := alignmentConnectionRefused(tc.goos, tc.err); got != tc.want {
				t.Fatalf("refusal=%v want=%v: %v", got, tc.want, tc.err)
			}
		})
	}
}

type alignmentContenderInput struct {
	Common   driver.CommonConfig
	Source   string
	Identity adaptor.Identity
	Hold     bool
}

func alignmentContender() int {
	var in alignmentContenderInput
	if json.NewDecoder(os.Stdin).Decode(&in) != nil {
		return 80
	}
	a := adaptor.New(cursor.Driver(cursor.Config{CommonConfig: in.Common}), adaptor.WithProfile(profile.Dedicated(in.Source)), adaptor.WithIdentity(in.Identity), adaptor.WithTools(alignmentTool("one")))
	ctx, c := context.WithTimeout(context.Background(), 4*time.Second)
	defer c()
	_, e := a.Run(ctx, "contender")
	if in.Hold {
		if e != nil {
			fmt.Println(e)
			return 82
		}
		fmt.Println("held")
		var one [1]byte
		_, _ = os.Stdin.Read(one[:])
		return 0
	}
	defer alignmentCloseContext(a, context.Background())
	if errors.Is(e, profile.ErrInUse) {
		fmt.Println("in-use")
		return 0
	}
	fmt.Printf("unexpected outcome: %v\n", e)
	return 81
}
func TestAlignmentLifecycleProfileOwnership(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	ctx, c := alignmentContext(t)
	defer c()
	opts := []adaptor.Option{adaptor.WithTools(alignmentTool("one"))}
	a := f.agent(t, opts...)
	if _, e := a.Run(ctx, "owner"); e != nil {
		t.Fatal(e)
	}
	entry := f.wait(t, "prompt", 1)[0]
	b := f.agent(t, opts...)
	if _, e := b.Run(ctx, "collision"); !errors.Is(e, profile.ErrInUse) {
		t.Errorf("same-process conflict: %v", e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	in, _ := json.Marshal(alignmentContenderInput{Common: f.common, Source: f.profile})
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = []string{alignmentFixtureEnv + "=contender", "HOME=" + filepath.Join(f.root, "home"), "USERPROFILE=" + filepath.Join(f.root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(f.root, "home"), "AGENT_ADAPTOR_LIVE_CONFORMANCE=0", "AGENT_ADAPTOR_E2E=0", "AGENT_ADAPTOR_UPDATE_API_GOLDEN=0", "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	cmd.Stdin = bytes.NewReader(in)
	output, e := cmd.CombinedOutput()
	if e != nil || string(output) != "in-use\n" {
		t.Fatalf("cross-process conflict: %s %v", output, e)
	}
	if len(f.wait(t, "prompt", 1)) != 1 {
		t.Fatal("contender reached provider")
	}
	// An actually independent complete identity receives a different directory.
	other := f.agent(t, adaptor.WithTools(alignmentTool("one")), adaptor.WithIdentity(adaptor.Identity{Name: "different"}))
	if _, e := other.Run(ctx, "identity"); e != nil {
		t.Fatal(e)
	}
	if f.wait(t, "prompt", 2)[1].Profile == entry.Profile {
		t.Error("identity namespace collided")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{filepath.Dir(entry.Profile), entry.Profile} {
			st, e := os.Stat(path)
			if e != nil || st.Mode().Perm() != 0700 {
				t.Errorf("private directory %s: %v %v", path, st, e)
			}
		}
		for _, name := range []string{"owner.json", "owner.lock", "state.json", "seed.json"} {
			st, e := os.Stat(filepath.Join(filepath.Dir(entry.Profile), name))
			if e != nil || st.Mode().Perm() != 0600 {
				t.Errorf("private marker %s: %v %v", name, st, e)
			}
		}
	}
	if e := alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := b.Run(ctx, "successor"); e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(filepath.Dir(entry.Profile), "state.json")
	before, e := os.ReadFile(state)
	if e != nil {
		t.Fatal(e)
	}
	if e = alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(state)
	if !bytes.Equal(before, after) {
		t.Error("closed predecessor overwrote successor generation")
	}
}

func TestAlignmentLifecycleProfileUnsafeAndModes(t *testing.T) {
	scenarios := []string{"marker", "symlink", "mode-drift", "revision"}
	if runtime.GOOS != "windows" {
		scenarios = append(scenarios, "permissions")
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			f := newAlignmentFixture(t, "cursor")
			store := memory.NewStore()
			if scenario == "mode-drift" {
				if e := os.WriteFile(filepath.Join(f.profile, "mcp.json"), []byte(`{"mcpServers":{"external":{"url":"https://example.invalid"}}}`), 0644); e != nil {
					t.Fatal(e)
				}
			}
			opts := []adaptor.Option{adaptor.WithTools(alignmentTool("one")), adaptor.WithThreadStore(store)}
			a := f.agent(t, opts...)
			ctx, c := alignmentContext(t)
			defer c()
			if _, e := a.Thread("guard").Run(ctx, "healthy"); e != nil {
				t.Fatal(e)
			}
			entry := f.wait(t, "prompt", 1)[0]
			before, _ := store.Resolve(ctx, threadstore.Query{Key: "guard"})
			if e := alignmentClose(t, a, ctx); e != nil {
				t.Fatal(e)
			}
			want := profile.ErrUnsafe
			switch scenario {
			case "marker":
				path := filepath.Join(filepath.Dir(entry.Profile), "owner.json")
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":99`), 1)
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				path := filepath.Join(filepath.Dir(entry.Profile), "owner.lock")
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				external := filepath.Join(f.root, "external")
				if e := os.WriteFile(external, []byte("untouched"), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(external, path); e != nil {
					t.Fatal(e)
				}
			case "permissions":
				if e := os.Chmod(filepath.Join(filepath.Dir(entry.Profile), "owner.json"), 0644); e != nil {
					t.Fatal(e)
				}
			case "mode-drift":
				path := filepath.Join(entry.Profile, "mcp.json")
				mode := os.FileMode(0600)
				if runtime.GOOS == "windows" {
					mode = 0444
				}
				if e := os.Chmod(path, mode); e != nil {
					t.Fatal(e)
				}
				want = adaptor.ErrThreadIncompatible
			case "revision":
				opts = []adaptor.Option{adaptor.WithTools(alignmentTool("two")), adaptor.WithThreadStore(store)}
				want = adaptor.ErrThreadIncompatible
			}
			b := f.agent(t, opts...)
			_, e := b.Thread("guard", adaptor.ResumeOnly()).Run(ctx, "rejected")
			if !errors.Is(e, want) {
				t.Errorf("rejection=%v want=%v", e, want)
			}
			after, _ := store.Resolve(ctx, threadstore.Query{Key: "guard"})
			if !reflect.DeepEqual(before, after) {
				t.Error("rejection changed old checkpoint")
			}
			if len(f.wait(t, "prompt", 1)) != 1 {
				t.Error("rejected invocation delivered prompt")
			}
		})
	}
}

func TestAlignmentLifecycleProfileTemporaryCleanup(t *testing.T) {
	for _, mode := range []string{"default", "native", "clone-from", "clone-native"} {
		t.Run(mode, func(t *testing.T) {
			f := newAlignmentFixture(t, "cursor")
			native := filepath.Join(f.root, "home", ".cursor")
			if e := os.MkdirAll(native, 0700); e != nil {
				t.Fatal(e)
			}
			selection := profile.Default()
			switch mode {
			case "native":
				selection = profile.Native()
			case "clone-from":
				selection = profile.CloneFrom(f.profile, filepath.Join(f.root, "selected"))
			case "clone-native":
				selection = profile.CloneNative(filepath.Join(f.root, "selected"))
			}
			a := f.agent(t, adaptor.WithProfile(selection), adaptor.WithTools(alignmentTool("one")))
			ctx, c := alignmentContext(t)
			defer c()
			if _, e := a.Run(ctx, "temporary"); e != nil {
				t.Fatal(e)
			}
			entry := f.wait(t, "prompt", 1)[0]
			if entry.Profile == native || entry.Profile == f.profile {
				t.Error("temporary selection used source directly")
			}
			if e := alignmentClose(t, a, ctx); e != nil {
				t.Fatal(e)
			}
			if _, e := os.Stat(entry.Profile); !os.IsNotExist(e) {
				t.Errorf("temporary clone survived: %v", e)
			}
			for _, source := range []string{native, f.profile} {
				if _, e := os.Stat(source); e != nil {
					t.Errorf("source removed: %v", e)
				}
			}
		})
	}
}

// Losing the on-disk transcript is distinguishable from keeping a resume ID.
func TestAlignmentLifecycleProfileMissingSession(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	store := memory.NewStore()
	a := f.agent(t, adaptor.WithThreadStore(store), adaptor.WithTools(alignmentTool("one")))
	ctx, c := alignmentContext(t)
	defer c()
	if _, e := a.Thread("history").Run(ctx, "first"); e != nil {
		t.Fatal(e)
	}
	entry := f.wait(t, "prompt", 1)[0]
	before, _ := store.Resolve(ctx, threadstore.Query{Key: "history"})
	if e := alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(entry.Profile, "projects", "session-t20.jsonl")); e != nil {
		t.Fatal(e)
	}
	next := f.agent(t, adaptor.WithThreadStore(store), adaptor.WithTools(alignmentTool("one")))
	_, e := next.Thread("history", adaptor.ResumeOnly()).Run(ctx, "missing-history")
	if e == nil {
		t.Fatal("resume succeeded without disk transcript")
	}
	after, _ := store.Resolve(ctx, threadstore.Query{Key: "history"})
	if !reflect.DeepEqual(before, after) {
		t.Error("missing historical transcript changed healthy record")
	}
	if len(f.wait(t, "prompt", 2)) != 2 {
		t.Error("ResumeOnly replayed")
	}
	if _, e = next.Thread("history").Run(ctx, "recover-history"); e != nil {
		t.Fatal(e)
	}
	recovered, _ := store.Resolve(ctx, threadstore.Query{Key: "history"})
	if recovered.ID == before.ID || recovered.State.ResumeID != "recovered-t20" {
		t.Errorf("fallback did not atomically replace old record: %+v", recovered)
	}
	old, _ := store.Resolve(ctx, threadstore.Query{ID: before.ID, IncludeArchived: true})
	if old == nil || old.Status != threadstore.StatusArchived || !reflect.DeepEqual(old.State, before.State) {
		t.Error("old checkpoint was lost during fallback")
	}
	attempts := f.wait(t, "prompt", 4)
	if len(attempts) != 4 || attempts[2].Resume != "session-t20" || attempts[3].Resume != "" {
		t.Errorf("resume rejection retry count/selector: %+v", attempts)
	}
}

func TestAlignmentLifecycleProfileCloseCleanupRetry(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	opts := []adaptor.Option{adaptor.WithTools(alignmentTool("one"))}
	a := f.agent(t, opts...)
	ctx, c := alignmentContext(t)
	defer c()
	if _, e := a.Run(ctx, "close-retry"); e != nil {
		t.Fatal(e)
	}
	entry := f.wait(t, "prompt", 1)[0]
	path := filepath.Join(entry.Profile, "mcp.json")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e = alignmentClose(t, a, ctx); e == nil {
		t.Fatal("unowned projection cleanup was not rejected")
	}
	if _, e = a.Run(ctx, "closed"); !errors.Is(e, adaptor.ErrAgentClosed) {
		t.Errorf("admission reopened: %v", e)
	}
	b := f.agent(t, opts...)
	if _, e = b.Run(ctx, "blocked-owner"); !errors.Is(e, profile.ErrInUse) {
		t.Errorf("released before cleanup: %v", e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, original, 0600); e != nil {
		t.Fatal(e)
	}
	if e = alignmentClose(t, a, ctx); e != nil {
		t.Fatalf("retry: %v", e)
	}
	if _, e = b.Run(ctx, "successor"); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(entry.Profile, "projects", "session-t20.jsonl"))
	if e != nil || len(data) == 0 {
		t.Error("Close retry removed transcript")
	}
}

func TestAlignmentLifecycleProfileMCPModePreserved(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	source := filepath.Join(f.profile, "mcp.json")
	raw := []byte(`{"mcpServers":{"external":{"url":"https://example.invalid/mcp"}},"unknown":{"keep":true}}`)
	if e := os.WriteFile(source, raw, 0600); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(source)
	if e != nil {
		t.Fatal(e)
	}
	mode := info.Mode().Perm()
	a := f.agent(t, adaptor.WithTools(alignmentTool("one")))
	ctx, c := alignmentContext(t)
	defer c()
	th := a.Thread("mode")
	if _, e := th.Run(ctx, "first"); e != nil {
		t.Fatal(e)
	}
	first := f.wait(t, "prompt", 1)[0]
	info, e = os.Stat(filepath.Join(first.Profile, "mcp.json"))
	if e != nil || info.Mode().Perm() != mode {
		t.Errorf("MCP projection changed existing mode: %v %v want=%o", info, e, mode)
	}
	if _, e := a.Thread("mode", adaptor.ResumeOnly()).Run(ctx, "second"); e != nil {
		t.Fatal(e)
	}
	if e = alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(source)
	if e != nil || !bytes.Equal(raw, after) {
		t.Error("source changed")
	}
	info, e = os.Stat(filepath.Join(first.Profile, "mcp.json"))
	if e != nil || info.Mode().Perm() != mode {
		t.Error("MCP removal changed mode")
	}
}

func TestAlignmentLifecycleProfileIdentityEncoding(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	ctx, c := alignmentContext(t)
	defer c()
	identities := []adaptor.Identity{{}, {ID: "a", Tenant: "bc"}, {ID: "ab", Tenant: "c"}, {ID: "a\x00b"}, {Profile: "a\x00b"}, {Name: "中:/"}, {Tenant: "中:/"}}
	dirs := map[string]bool{}
	for i, id := range identities {
		a := f.agent(t, adaptor.WithIdentity(id), adaptor.WithTools(alignmentTool("one")))
		if _, e := a.Run(ctx, fmt.Sprintf("identity-%d", i)); e != nil {
			t.Fatal(e)
		}
		entry := f.wait(t, "prompt", i+1)[i]
		if dirs[entry.Profile] {
			t.Fatalf("identity directory collision %+v", id)
		}
		dirs[entry.Profile] = true
	}
	alias := filepath.Join(f.root, "source-alias")
	if e := os.Symlink(f.profile, alias); e != nil {
		t.Fatal(e)
	}
	a := f.agent(t, adaptor.WithProfile(profile.Dedicated(alias)), adaptor.WithTools(alignmentTool("one")))
	if _, e := a.Run(ctx, "canonical-collision"); !errors.Is(e, profile.ErrInUse) {
		t.Errorf("canonical source alias bypassed claim: %v", e)
	}
}

func TestAlignmentLifecycleProfileCrashRequiresRecovery(t *testing.T) {
	f := newAlignmentFixture(t, "cursor")
	ctx, c := alignmentContext(t)
	defer c()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = []string{alignmentFixtureEnv + "=contender", "HOME=" + filepath.Join(f.root, "home"), "USERPROFILE=" + filepath.Join(f.root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(f.root, "home"), "AGENT_ADAPTOR_LIVE_CONFORMANCE=0", "AGENT_ADAPTOR_E2E=0", "AGENT_ADAPTOR_UPDATE_API_GOLDEN=0", "GORACE=atexit_sleep_ms=0 halt_on_error=1"}
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	if e = json.NewEncoder(stdin).Encode(alignmentContenderInput{Common: f.common, Source: f.profile, Hold: true}); e != nil {
		t.Fatal(e)
	}
	f.wait(t, "prompt", 1)
	entry := f.wait(t, "prompt", 1)[0]
	state := filepath.Join(filepath.Dir(entry.Profile), "state.json")
	before, e := os.ReadFile(state)
	if e != nil {
		t.Fatal(e)
	}
	stdin.Close()
	if e = cmd.Wait(); e != nil {
		t.Fatalf("child crash fixture: %v %s", e, stdout.String())
	}
	if stdout.String() != "held\n" {
		t.Fatalf("child did not hold claim: %q", stdout.String())
	}
	a := f.agent(t, adaptor.WithTools(alignmentTool("one")))
	if _, e = a.Run(ctx, "must-not-replay"); !errors.Is(e, profile.ErrRecoveryRequired) {
		t.Errorf("crashed active claim accepted: %v", e)
	}
	after, _ := os.ReadFile(state)
	if !bytes.Equal(before, after) {
		t.Error("recovery refusal rewrote crashed generation")
	}
	if len(f.wait(t, "prompt", 1)) != 1 {
		t.Error("crash recovery automatically delivered prompt")
	}
}
