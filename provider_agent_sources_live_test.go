package adaptor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestLiveProviderAgentSources(t *testing.T) {
	for _, provider := range []string{"pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_"+strings.ToUpper(provider)+"_LIVE") != "1" {
				t.Skip("both live gates required")
			}
			model := os.Getenv("AGENT_ADAPTOR_" + strings.ToUpper(provider) + "_MODEL")
			if model == "" {
				t.Fatal("explicit live model required")
			}
			makeDriver := func() driver.Driver {
				if provider == "pi" {
					return livePiAgentDriver(t)
				}
				return opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true})
			}
			if provider == "pi" && !strings.Contains(model, "/") && os.Getenv("AGENT_ADAPTOR_PI_PROVIDER") != "" {
				model = os.Getenv("AGENT_ADAPTOR_PI_PROVIDER") + "/" + model
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			d := makeDriver()
			native, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			writeSource := func(path, proofFile string) {
				t.Helper()
				encodedModel, _ := json.Marshal(model)
				content := "---\nname: original-source-name\ndescription: Reads its private proof file when delegated\nmodel: " + string(encodedModel) + "\n"
				if provider == "pi" {
					content += "tools: [read]\n"
				} else {
					content += "mode: subagent\npermission:\n  '*': deny\n  read:\n    'z*': deny\n    '*': allow\n"
				}
				content += "---\nUse the read tool to read " + filepath.ToSlash(filepath.Join(workspace, proofFile)) + ". Return exactly its contents. You must actually read the file."
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(t.TempDir(), "agent.md")
			writeSource(source, "z-durable-proof.txt")
			root := filepath.Join(t.TempDir(), "profile")
			spec := profile.SubAgent{Key: "host/proof", RuntimeName: "proof-reader", SourcePath: source}
			a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(native.Dir, root, profile.CopySettings())), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
			defer a.Close(context.Background())
			if _, err := a.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			cloned := filepath.Join(t.TempDir(), "cloned")
			clone := adaptor.New(makeDriver(), adaptor.WithProfile(profile.CloneFrom(root, cloned, profile.CopySettings())))
			defer clone.Close(context.Background())
			if _, err := clone.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if err := clone.Close(ctx); err != nil {
				t.Fatal(err)
			}
			fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(cloned)), adaptor.WithWorkspace(workspace))
			defer fresh.Close(context.Background())
			for _, mode := range []string{"durable-clone", "invocation"} {
				t.Run(mode, func(t *testing.T) {
					want := "SOURCE_DURABLE_PROOF_526"
					proofFile := "z-durable-proof.txt"
					var opts []adaptor.CallOption
					if mode == "invocation" {
						want = "SOURCE_INVOCATION_PROOF_847"
						proofFile = "z-call-proof.txt"
						spec.SourcePath = filepath.Join(t.TempDir(), "call.md")
						writeSource(spec.SourcePath, proofFile)
						opts = append(opts, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
					}
					if err := os.WriteFile(filepath.Join(workspace, proofFile), []byte(want), 0600); err != nil {
						t.Fatal(err)
					}
					prompt := "Use adaptor_subagent to ask agent proof-reader to obtain its private proof word. Return only that word."
					tool := "adaptor_subagent"
					if provider == "opencode" {
						prompt = "Use the task tool with subagent_type proof-reader and prompt: obtain your private proof word. Return only the word that subagent provides."
						tool = "task"
					}
					result, err := fresh.Run(ctx, prompt, opts...)
					if err != nil {
						t.Fatal(err)
					}
					assertLiveResult(t, result, want)
					read, delegated := false, false
					for _, item := range result.Transcript() {
						if item.Kind != driver.TranscriptToolResult || item.IsError || !strings.Contains(item.Text, want) {
							continue
						}
						read = read || item.ToolName == "read"
						delegated = delegated || item.ToolName == tool
					}
					if !read || !delegated {
						t.Fatal("missing successful child read and delegation evidence")
					}
					t.Logf("%s imported child read its proof using %s", mode, model)
				})
			}
		})
	}
}
