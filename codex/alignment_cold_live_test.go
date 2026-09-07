//go:build codex_live

package codex

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
	toml "github.com/pelletier/go-toml/v2"
)

// R017 exercises the public lifecycle with the real provider. The nonce exists
// only in the first prompt and assertions; neither the tool nor the second
// Agent's configuration can supply it to a replacement conversation.
func TestAlignmentLiveDedicatedToolResumeAfterClose(t *testing.T) {
	cfg := alignmentLiveGate(t)
	var source string
	for _, binding := range cfg.Env {
		if binding.Name == "CODEX_HOME" {
			source = binding.Value
		}
	}
	if source == "" {
		t.Fatal("isolated Dedicated seed missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store := memory.NewStore()
	identity := adaptor.Identity{ID: "alignment-cold", Tenant: "alignment", Profile: "dedicated", Name: "Codex cold resume"}
	var calls atomic.Int32
	probe := tool.Define("alignment_cold_probe", "Return a fixed connectivity receipt; no conversation data.", func(context.Context, struct{}) (string, error) {
		calls.Add(1)
		return "alignment-tool-ack", nil
	}, tool.ReadOnly(), tool.Revision("alignment-cold-probe/v1"))
	newAgent := func() *adaptor.Agent {
		return adaptor.New(Driver(cfg), adaptor.WithThreadStore(store), adaptor.WithProfile(profile.Dedicated(source)),
			adaptor.WithWorkspace(cfg.CWD), adaptor.WithIdentity(identity), adaptor.WithTools(probe),
			adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
	}
	closeAgent := func(a *adaptor.Agent) {
		t.Helper()
		closeCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := a.Close(closeCtx); err != nil {
			t.Fatal("bounded Agent.Close failed")
		}
	}
	a := newAgent()
	t.Cleanup(func() { closeAgent(a) })
	key := "alignment/cold-thread"
	nonce := alignmentNonce(t)
	first := alignmentColdTurn(t, ctx, a.Thread(key), "Call alignment_cold_probe exactly once. Remember this conversation marker: "+nonce+". Do not write it to any file or use shell tools. Reply only with the marker.")
	if calls.Load() != 1 || strings.TrimSpace(first.Text) != nonce {
		t.Fatal("first turn did not actually call the tool and acknowledge the nonce")
	}
	checkpoint, err := a.Thread(key).Checkpoint(ctx)
	if err != nil || checkpoint == nil || !checkpoint.Valid || checkpoint.State == nil || checkpoint.State.ResumeID == "" {
		t.Fatal("first turn has no healthy public checkpoint")
	}
	old := alignmentColdMaterialization(t, source)
	sessions := alignmentColdSessionFiles(t, old.home, checkpoint.State.ResumeID)
	parsed, err := url.Parse(old.endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("hosted gateway was not a loopback HTTP endpoint")
	}
	// The live endpoint must exist before Close; no bearer secret is extracted.
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(old.endpoint)
	if err != nil {
		t.Fatal("first hosted gateway is unavailable")
	}
	response.Body.Close()
	client.CloseIdleConnections()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("hosted gateway did not require authentication")
	}
	closeAgent(a)
	alignmentColdFilesRetained(t, sessions)
	closedSessions := alignmentColdSessionFiles(t, old.home, checkpoint.State.ResumeID)
	// Binding the old address proves Close removed its listener and forces a
	// new concrete endpoint. The guard never accepts or forwards a request.
	guard, err := net.Listen("tcp4", parsed.Host)
	if err != nil {
		t.Fatal("old hosted gateway listener was not released by Close")
	}
	defer guard.Close()
	b := newAgent()
	t.Cleanup(func() { closeAgent(b) })
	secondPrompt := "Call alignment_cold_probe exactly once, then recall the conversation marker from our first exchange. Do not read files or use shell tools. Reply only with that marker."
	if strings.Contains(secondPrompt, nonce) {
		t.Fatal("second prompt reintroduced the nonce")
	}
	second := alignmentColdTurn(t, ctx, b.Thread(key, adaptor.ResumeOnly()), secondPrompt)
	if calls.Load() != 2 || strings.TrimSpace(second.Text) != nonce {
		t.Fatal("cold resumed turn did not call the new tool registration and recall prior history")
	}
	after, err := b.Thread(key, adaptor.ResumeOnly()).Checkpoint(ctx)
	if err != nil || after == nil || !after.Valid || after.State == nil || after.State.ResumeID != checkpoint.State.ResumeID {
		t.Fatal("cold resume changed provider session identity or lost its healthy checkpoint")
	}
	current := alignmentColdMaterialization(t, source)
	if current.home != old.home || current.endpoint == old.endpoint || current.carrier == old.carrier {
		t.Fatal("Dedicated session home changed or concrete gateway credentials did not rotate")
	}
	alignmentColdFilesRetained(t, sessions)
	closeAgent(b)
	alignmentColdFilesRetained(t, sessions)
	continued := false
	for path, before := range closedSessions {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(after, before) {
			t.Fatal("resumed turn did not retain the closed Agent's native session")
		}
		continued = continued || len(after) > len(before)
	}
	if !continued {
		t.Fatal("resumed provider did not append to the original native session")
	}
	t.Log("real cold-resume oracle: two tool calls, retained native rollout, same session, closed old listener and rotated endpoint/credential carrier")
}

func alignmentColdTurn(t *testing.T, ctx context.Context, thread *adaptor.Thread, prompt string) *adaptor.Result {
	t.Helper()
	stream := thread.Stream(ctx, prompt)
	completed, spawns := false, 0
	for event := range stream.Events() {
		switch e := event.(type) {
		case adaptor.ProcessInfo:
			if e.Kind == adaptor.ProcessSpawn {
				spawns++
			}
		case adaptor.CapabilityInvocation:
			if e.Invocation.Ref.Kind == capability.MCP && e.Invocation.Ref.Key == toolidentity.ServerKey &&
				e.Invocation.Ref.Operation == "alignment_cold_probe" && e.Invocation.Phase == capability.Completed &&
				e.Invocation.Source == capability.Provider && e.Invocation.Evidence == capability.ProviderProtocol {
				completed = true
			}
		}
	}
	result, err := stream.Result()
	if err != nil || result == nil || !completed || spawns != 1 || result.Raw().Stdout == "" || result.Raw().Terminal == nil || len(result.Transcript()) == 0 {
		t.Fatal("real cold turn lacks formal MCP completion, one fresh process, or complete output")
	}
	return result
}

type alignmentColdProfile struct{ home, endpoint, carrier string }

// Read only the test-owned profile tree. Discover the materialized native
// config instead of duplicating the private partition/fingerprint algorithm.
func alignmentColdMaterialization(t *testing.T, source string) alignmentColdProfile {
	t.Helper()
	var found []alignmentColdProfile
	err := filepath.WalkDir(filepath.Dir(source), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "config.toml" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var config struct {
			Servers map[string]struct {
				URL     string `toml:"url"`
				Carrier string `toml:"bearer_token_env_var"`
			} `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal(raw, &config); err != nil {
			return err
		}
		if server, ok := config.Servers[toolidentity.ServerKey]; ok {
			found = append(found, alignmentColdProfile{filepath.Dir(path), server.URL, server.Carrier})
		}
		return nil
	})
	if err != nil || len(found) != 1 || found[0].home == source || found[0].endpoint == "" || !toolidentity.IsBearerTokenEnvVar(found[0].carrier) {
		t.Fatal("expected one isolated persistent Dedicated tool materialization")
	}
	return found[0]
}

func alignmentColdSessionFiles(t *testing.T, home, sessionID string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), "-"+sessionID+".jsonl") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if len(raw) > 0 {
				files[path] = raw
			}
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatal("healthy provider session has no native rollout file")
	}
	return files
}

func alignmentColdFilesRetained(t *testing.T, files map[string][]byte) {
	t.Helper()
	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(after, before) {
			t.Fatal("Close/resume removed or rewrote prior native session contents")
		}
	}
}
