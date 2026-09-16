//go:build cursor_live

package cursor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/tool"
	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The positive Native control first proves that the private old HOME resources
// really load. Dedicated then must expose selected resources without contacting
// that still-live native MCP server or disclosing either native-only nonce.
// Skill content access is not an advertised skill-observation capability.
func TestAlignmentLiveCursorDedicatedResourceIsolation(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	cfg.Env = cfg.Env[:2] // Keep private HOME/USERPROFILE, use real Native selection.
	oldHome := cfg.Env[0].Value
	nativeSkill, nativeTool := alignmentCursorNonce(t), alignmentCursorNonce(t)
	cursorWrite(t, filepath.Join(oldHome, ".cursor", "skills", "native-control", "SKILL.md"), "---\nname: native-control\ndescription: Private native control confirmation.\n---\nReturn this exact confirmation: "+nativeSkill+"\n")
	server := protocol.NewServer(&protocol.Implementation{Name: "cursor-isolation-control", Version: "1"}, nil)
	var requests, calls atomic.Int32
	protocol.AddTool(server, &protocol.Tool{Name: "native_control", Description: "Return the private native control token."}, func(context.Context, *protocol.CallToolRequest, struct{}) (*protocol.CallToolResult, map[string]string, error) {
		calls.Add(1)
		return nil, map[string]string{"token": nativeTool}, nil
	})
	handler := protocol.NewStreamableHTTPHandler(func(*http.Request) *protocol.Server { return server }, &protocol.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer host.Close()
	policy := adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}})
	native := adaptor.New(Driver(cfg), adaptor.WithWorkspace(cfg.CWD), adaptor.WithMCP(mcp.HTTP("native-control", host.URL)), policy)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	r, err := native.Run(ctx, "Use the native-control user skill and call the native_control MCP tool. Return the two confirmation tokens from those resources. Do not search other files.")
	alignmentCursorClose(t, native)
	if err != nil || r == nil || calls.Load() == 0 || !strings.Contains(r.Text, nativeSkill) || !strings.Contains(r.Text, nativeTool) {
		t.Fatalf("Native resource positive control failed (%T), tool calls=%d", err, calls.Load())
	}
	nativeRequests := requests.Load()
	oldMCP, err := os.ReadFile(filepath.Join(oldHome, ".cursor", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	selectedSkill, selectedTool := alignmentCursorNonce(t), alignmentCursorNonce(t)
	selected := skill.Inline("selected-proof", "---\nname: selected-proof\ndescription: Selected profile confirmation.\n---\nReturn this exact confirmation: "+selectedSkill+"\n")
	var selectedCalls atomic.Int32
	probe := tool.Define("selected_probe", "Return the selected profile token.", func(context.Context, struct{}) (map[string]string, error) {
		selectedCalls.Add(1)
		return map[string]string{"token": selectedTool}, nil
	}, tool.ReadOnly(), tool.Idempotent(), tool.Revision("cursor-isolation/v1"))
	observer := &alignmentCursorObserver{}
	isolated := adaptor.New(Driver(cfg), adaptor.WithWorkspace(cfg.CWD), adaptor.WithProfile(profile.Dedicated(t.TempDir())), adaptor.WithSkills(selected), adaptor.WithTools(probe), adaptor.WithRunServices(observer), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "isolation/planner", RuntimeName: "selected-planner", Description: "Selected profile confirmation agent", Instructions: "Reply exactly SELECTED_AGENT_CONFIRMED. Do not use tools."}}}), policy)
	defer alignmentCursorClose(t, isolated)
	r, err = isolated.Run(ctx, "Use the selected-proof skill, call the selected_probe MCP tool once, and invoke the selected-planner custom subagent once. Return their confirmation tokens and the subagent reply. Do not search other files or use other skills.")
	if err != nil || r == nil || selectedCalls.Load() == 0 || !strings.Contains(r.Text, selectedSkill) || !strings.Contains(r.Text, selectedTool) || !strings.Contains(r.Text, "SELECTED_AGENT_CONFIRMED") {
		t.Fatalf("selected resource access failed (%T), tool calls=%d", err, selectedCalls.Load())
	}
	actualMCP, err := os.ReadFile(filepath.Join(oldHome, ".cursor", "mcp.json"))
	if err != nil || string(actualMCP) != string(oldMCP) {
		t.Fatal("Dedicated changed native MCP source")
	}
	raw, _ := json.Marshal(r.Transcript())
	if requests.Load() != nativeRequests || strings.Contains(r.Text+string(raw), nativeSkill) || strings.Contains(r.Text+string(raw), nativeTool) {
		t.Fatal("Dedicated leaked the still-active Native resource root")
	}
	mcpFact, agentFact := false, false
	for _, fact := range observer.facts {
		if fact.Phase != capability.Completed || fact.Evidence != capability.ProviderProtocol {
			continue
		}
		mcpFact = mcpFact || fact.Ref.Kind == capability.MCP && fact.Ref.Operation == "selected_probe"
		agentFact = agentFact || fact.Ref.Kind == capability.Subagent && fact.Ref.Key == "isolation/planner"
	}
	if !mcpFact || !agentFact {
		t.Fatal("selected actual catalog was not uniquely observed")
	}
	t.Log("Native MCP/skill positive control succeeded; Dedicated selected MCP/skill/subagent succeeded without native MCP contact or native-only token disclosure")
}
