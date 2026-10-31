package pi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeAgentSkill(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentSkillSelectionAndCapturedContent(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	writeAgentSkill(t, filepath.Join(root, "skills", "native"), "native body")
	writeAgentSkill(t, filepath.Join(root, "skills", "override"), "old installation")
	path := writeAgentSkill(t, source, "selected body\nrelative.md")
	req := driver.Request{Skills: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "host/skill", RuntimeName: "override", SourcePath: source}}}}
	req.ProfilePayload.Agents.Agents = []driver.AgentSpec{{Key: "worker", RuntimeName: "worker", Skills: []string{"host/skill", "override", "native"}}}
	plans, err := resolveAgentResources(context.Background(), req, root)
	if err != nil {
		t.Fatal(err)
	}
	preloads := plans["worker"].Skills
	if len(preloads) != 2 || preloads[0].Content != "selected body\nrelative.md" || preloads[0].Path != path || preloads[1].Content != "native body" {
		t.Fatalf("preloads=%+v", preloads)
	}
	writeAgentSkill(t, source, "changed body")
	if preloads[0].Content != "selected body\nrelative.md" {
		t.Fatal("resolved content changed after parent started")
	}
	if _, err := findAgentSkill([]driver.SnapshotEntry{{Key: "one", RuntimeName: "same"}, {Key: "two", RuntimeName: "same"}}, "same"); err == nil {
		t.Fatal("ambiguous runtime name accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveAgentResources(canceled, req, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestAgentPreloadUsesActualInstalledCopy(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	writeAgentSkill(t, source, "source before copy")
	payload := driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "host/copied", RuntimeName: "copied", SourcePath: source}}}
	if _, err := skillruntime.SyncProfileSkills(context.Background(), DriverType, root, driver.ProfileKindShared, payload, payload.Keys(), nil); err != nil {
		t.Fatal(err)
	}
	req := driver.Request{}
	req.ProfilePayload.Agents.Agents = []driver.AgentSpec{{Key: "worker", RuntimeName: "worker", Skills: []string{"host/copied"}}}
	plans, err := resolveAgentResources(context.Background(), req, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans["worker"].Skills) != 1 || plans["worker"].Skills[0].Path != filepath.Join(root, "skills", "copied", "SKILL.md") {
		t.Fatalf("did not read native installed path: %+v", plans)
	}
}

func TestAgentResourcesFailBeforeParentLaunch(t *testing.T) {
	for _, mode := range []string{"missing-skill", "missing-mcp", "bad-utf8", "empty-skill", "large-skill", "directory"} {
		t.Run(mode, func(t *testing.T) {
			r, _, capture := delegatedRunFixture(t, happy)
			root := r.config.Env[0].Value
			spec := r.request.ProfilePayload.Agents.Agents[0]
			spec.Skills = []string{"native"}
			switch mode {
			case "missing-mcp":
				spec.Skills, spec.MCPServers = nil, []string{"missing"}
			case "bad-utf8":
				writeAgentSkill(t, filepath.Join(root, "skills", "native"), string([]byte{0xff}))
			case "empty-skill":
				writeAgentSkill(t, filepath.Join(root, "skills", "native"), " \n")
			case "large-skill":
				writeAgentSkill(t, filepath.Join(root, "skills", "native"), strings.Repeat("a", maxAgentSkillBytes+1))
			case "directory":
				if err := os.MkdirAll(filepath.Join(root, "skills", "native", "SKILL.md"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			r.request.ProfilePayload.Declared.Agents = true
			r.request.ProfilePayload.Agents.Agents = []driver.AgentSpec{spec}
			if _, err := r.driver.Run(r.ctx, r.request, r.sink.base); err == nil {
				t.Fatal("invalid reference reached parent")
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("parent process launched")
			}
		})
	}
}

func TestAgentMCPScopeDoesNotRediscoverDurableServers(t *testing.T) {
	for _, mode := range []string{"select", "inherit-empty", "inherit"} {
		t.Run(mode, func(t *testing.T) {
			r, _, _ := delegatedRunFixture(t, happy)
			var counts [2]atomic.Int32
			var servers []driver.MCPServerSpec
			for i, key := range []string{"selected", "excluded"} {
				server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: key, Version: "1"}, nil)
				handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					counts[i].Add(1)
					handler.ServeHTTP(w, req)
				}))
				t.Cleanup(upstream.Close)
				servers = append(servers, driver.MCPServerSpec{Key: key, Transport: driver.MCPTransportHTTP, URL: upstream.URL})
			}
			entries, err := mcpProfileEntries(driver.MCPPayload{Servers: servers})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := profilejson.Sync(r.ctx, mcpProfileLayout(r.config.Env[0].Value), entries, ""); err != nil {
				t.Fatal(err)
			}
			if mode != "inherit-empty" {
				r.request.MCP.Servers = servers
			}
			if mode == "select" {
				r.request.ProfilePayload.Agents.Agents[0].MCPServers = []string{"selected"}
			}
			r.resources, err = resolveAgentResources(r.ctx, r.request, r.config.Env[0].Value)
			if err != nil {
				t.Fatal(err)
			}
			observeDelegation(r, "scoped", "task")
			if _, err := r.execute(r.ctx, "scoped", "worker", "task"); err != nil {
				t.Fatal(err)
			}
			want := []bool{mode != "inherit-empty", mode == "inherit"}
			got := []bool{counts[0].Load() > 0, counts[1].Load() > 0}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("connected=%v want=%v", got, want)
			}
		})
	}
}

func TestAgentPreloadExtension(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute native extension contract")
	}
	bridge := &mcpBridge{scope: &agentScope{skills: []agentPreload{{Key: "key", Path: "/skills/a/SKILL.md", Content: "PRIVATE_BODY\nUse ./asset.md"}}}}
	source, err := bridge.extensionSource("url", "token")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `import assert from "node:assert/strict";
import extension from "./extension.mjs";
const handlers=new Map(); extension({on(name,fn){handlers.set(name,fn)},registerTool(){}});
const hook=handlers.get("before_agent_start"); assert.equal(typeof hook,"function");
for(const systemPrompt of ["Native prompt","Replacement prompt"]){
  const result=hook({systemPrompt}); assert.ok(result.systemPrompt.startsWith(systemPrompt));
  assert.ok(result.systemPrompt.includes("PRIVATE_BODY\nUse ./asset.md"));
  assert.ok(result.systemPrompt.includes("/skills/a/SKILL.md"));
  assert.equal(result.systemPrompt.match(/PRIVATE_BODY/g).length,1);
}
`
	file := filepath.Join(root, "verify.mjs")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, file).CombinedOutput(); err != nil {
		t.Fatalf("extension: %s %v", output, err)
	}
}

func TestAgentPreloadDriftReplacesThread(t *testing.T) {
	f := persistentRPCFixture(t)
	source := t.TempDir()
	writeAgentSkill(t, source, "---\nname: preload\ndescription: Child skill\n---\nfirst body")
	selected := skill.Dir(source)
	selected.Key = "host/preload"
	options := []adaptor.CallOption{
		adaptor.WithSkills(selected),
		adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "worker", Skills: []string{"host/preload"}}}}),
	}
	f.run(t, options...)
	f.run(t, options...)
	if len(f.pids(t)) != 1 {
		t.Fatal("unchanged skill replaced writer")
	}
	writeAgentSkill(t, source, "---\nname: preload\ndescription: Child skill\n---\nchanged body")
	f.run(t, options...)
	if len(f.pids(t)) != 2 {
		t.Fatal("changed preload retained old session")
	}
}
