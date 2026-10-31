package adaptor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func livePiAgentDriver(t *testing.T) driver.Driver {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_PI_LIVE") != "1" {
		t.Skip("both live gates required")
	}
	model := os.Getenv("AGENT_ADAPTOR_PI_MODEL")
	if model == "" {
		t.Fatal("explicit live model required")
	}
	return pi.Driver(pi.Config{Command: os.Getenv("AGENT_ADAPTOR_PI_COMMAND"), Provider: os.Getenv("AGENT_ADAPTOR_PI_PROVIDER"), Model: model, Thinking: "low", NoExtensions: true, NoSkills: true})
}

func TestLivePiProfileAgents(t *testing.T) {
	d := livePiAgentDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "profile")
	spec := profile.SubAgent{Key: "host/proof", RuntimeName: "proof", Description: "Reports its private proof word", Instructions: "Return exactly PI_CHILD_PROOF_863 when asked for your proof word. Do not use tools."}
	a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	if _, err := a.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := adaptor.New(livePiAgentDriver(t), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithWorkspace(t.TempDir()))
	defer fresh.Close(context.Background())
	for _, mode := range []string{"durable", "invocation"} {
		t.Run(mode, func(t *testing.T) {
			want := "PI_CHILD_PROOF_863"
			var options []adaptor.CallOption
			if mode == "invocation" {
				want = "PI_CHILD_CALL_481"
				spec.Instructions = "Return exactly PI_CHILD_CALL_481 when asked for your proof word. Do not use tools."
				options = append(options, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
			}
			result, err := fresh.Run(ctx, "Use adaptor_subagent to ask agent proof for its private proof word. Return only that word.", options...)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, want)
			found := false
			for _, item := range result.Transcript() {
				if item.Kind == driver.TranscriptToolResult && item.ToolName == "adaptor_subagent" && !item.IsError && strings.Contains(item.Text, want) {
					found = true
				}
			}
			if !found || result.Metadata["pi.child_runs"] == "" {
				t.Fatal("child execution audit missing")
			}
		})
	}
}

func TestLivePiAgentResources(t *testing.T) {
	d := livePiAgentDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var initialized [2]atomic.Int32
	var called [2]atomic.Int32
	var servers []mcp.Server
	for i, key := range []string{"included", "excluded"} {
		server := durableProofServer(func() { called[i].Add(1) })
		handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read request", 400)
					return
				}
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var message struct{ Method string }
				if json.Unmarshal(raw, &message) == nil && message.Method == "initialize" {
					initialized[i].Add(1)
				}
			}
			handler.ServeHTTP(w, r)
		}))
		defer upstream.Close()
		servers = append(servers, mcp.Server{Key: key, Transport: mcp.TransportHTTP, URL: upstream.URL})
	}
	skillSpec := func(proof string) skill.Skill {
		return skill.Inline("host/preloaded", "---\nname: preloaded\ndescription: Private proof for a child agent\n---\nYour preloaded proof word is "+proof+". When asked for a combined proof, call durable_proof and return your proof word followed by the exact tool result, separated by one space. Do not read any files.")
	}
	spec := profile.SubAgent{Key: "worker", Description: "Combines a private preloaded skill proof with the MCP proof", Skills: []string{"host/preloaded"}, MCPServers: []string{"included"},
		Instructions: "Follow your preloaded skill when asked for the combined proof. You must call durable_proof and report both proof words.",
		ToolPolicy:   &profile.ToolPolicy{Deny: []string{"read", "bash", "edit", "write", "grep", "find", "ls"}}}
	directory := filepath.Join(t.TempDir(), "profile")
	seed := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())),
		adaptor.WithProfileResources(profile.Resources{Skills: []skill.Ref{skillSpec("PI_PRELOAD_DURABLE_937")}, MCP: servers, Agents: []profile.SubAgent{spec}}))
	if _, err := seed.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := adaptor.New(livePiAgentDriver(t), adaptor.WithWorkspace(t.TempDir()), adaptor.WithProfile(profile.Dedicated(directory)))
	defer fresh.Close(context.Background())
	for _, mode := range []string{"durable", "invocation"} {
		t.Run(mode, func(t *testing.T) {
			before := [2]int32{initialized[0].Load(), initialized[1].Load()}
			proof := "PI_PRELOAD_DURABLE_937"
			var options []adaptor.CallOption
			if mode == "invocation" {
				proof = "PI_PRELOAD_OVERRIDE_625"
				options = append(options, adaptor.WithSkills(skillSpec(proof)))
			}
			result, err := fresh.Run(ctx, "Use adaptor_subagent to ask worker for the combined proof from its preloaded skill and durable_proof tool. You must not perform its task yourself. Return exactly the child's answer.", options...)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, proof+" DURABLE_MCP_847")
			childTool := false
			for _, item := range result.Transcript() {
				if item.Kind == driver.TranscriptToolResult && item.Metadata["pi.agent"] == "worker" && strings.HasPrefix(item.ToolName, "mcp_") && !item.IsError && strings.Contains(item.Text, "DURABLE_MCP_847") {
					childTool = true
				}
			}
			if !childTool || called[0].Load() == 0 || called[1].Load() != 0 {
				t.Fatal("scoped child MCP execution not observed")
			}
			if initialized[0].Load()-before[0] != 2 || initialized[1].Load()-before[1] != 1 {
				t.Fatalf("unexpected parent/child MCP sessions: included=%d excluded=%d", initialized[0].Load()-before[0], initialized[1].Load()-before[1])
			}
		})
	}
}

func TestLivePiDelegatedApprovals(t *testing.T) {
	for _, mode := range []string{"read", "question", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d := livePiAgentDriver(t)
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("PI_CHILD_READ_763"), 0600); err != nil {
				t.Fatal(err)
			}
			spec := profile.SubAgent{Key: "worker", Description: "Performs the requested tool operation and reports the result", Instructions: "Perform only the requested tool operation. Do not delegate. Report the exact result.", ToolPolicy: &profile.ToolPolicy{Allow: []string{"read", "write", "adaptor_ask_user"}}}
			a := adaptor.New(d, adaptor.WithWorkspace(workspace), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}
			operation, want := "Read marker.txt with read and return the exact contents.", "PI_CHILD_READ_763"
			if mode == "question" {
				policy.Question = adaptor.QuestionAsk
				operation = "Use adaptor_ask_user to ask the host for the secret word and return it."
				want = "PI_CHILD_ANSWER_427"
			}
			if mode == "deny" || mode == "cancel" {
				operation = "Use write to create denied.txt containing TEST. Do not read files first."
			}
			var children atomic.Int32
			respond := func(ctx context.Context, request *adaptor.ApprovalRequest) error {
				if strings.HasPrefix(request.ToolCallID, "pi-child:") {
					children.Add(1)
					switch mode {
					case "deny":
						return request.Deny(ctx, "child write denied")
					case "cancel":
						cancel()
						return nil
					case "question":
						if request.Kind == adaptor.ApprovalQuestion {
							return request.Answer(ctx, want)
						}
					}
				}
				return request.Approve(ctx)
			}
			options := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: policy})}
			if mode == "read" || mode == "deny" {
				options = append(options, adaptor.OnApproval(respond))
			}
			stream := a.Stream(ctx, "Use adaptor_subagent to delegate to worker with this task: "+operation+" You must not perform the operation yourself. Report only its result.", options...)
			for event := range stream.Events() {
				if request, ok := event.(*adaptor.ApprovalRequest); ok && mode != "read" && mode != "deny" {
					if err := respond(ctx, request); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := stream.Result()
			if children.Load() == 0 {
				t.Fatalf("no child approval: %v", err)
			}
			switch mode {
			case "deny":
				var failed *adaptor.RunError
				if !errors.As(err, &failed) || failed.Reason != adaptor.ReasonApprovalDenied {
					t.Fatalf("denial: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				assertLiveResult(t, result, want)
			}
			if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
				t.Fatal("denied child write executed")
			}
		})
	}
}
