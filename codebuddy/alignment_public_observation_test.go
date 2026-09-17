package codebuddy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
)

func TestAlignmentObservationPublicDeferredMCPAudit(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		failed      bool
		want        capability.Phase
	}{
		{"success", `{"toolName":"mcp__knowledge__search","params":{"query":"PRIVATE_ARGUMENT"}}`, false, capability.Completed},
		{"failed", `{"toolName":"mcp__knowledge__search","params":{"query":"PRIVATE_ARGUMENT"}}`, true, capability.Failed},
		{"unknown", `{"toolName":"mcp__unknown__search","params":{}}`, false, ""},
		{"nested", `{"toolName":"DeferExecuteTool","params":{"toolName":"mcp__knowledge__search","params":{}}}`, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := alignmentCall("DeferExecuteTool", "wrapper-id", tc.input)
			result := alignmentResult("wrapper-id", `"PRIVATE_RESULT"`, tc.failed, "")
			terminal := `{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"prefix PRIVATE_MARKER suffix","usage":{"input_tokens":7,"output_tokens":3}}`
			protocol := strings.Join([]string{`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`, alignmentCatalogPartial("DeferExecuteTool", "wrapper-id", tc.input), call, result, result, terminal, ""}, "\n")
			path := filepath.Join(t.TempDir(), "protocol")
			if err := os.WriteFile(path, []byte(protocol), 0600); err != nil {
				t.Fatal(err)
			}
			service := &alignmentObservationService{events: map[string][]adaptor.Event{}}
			fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path}}, adaptor.WithMCP(mcp.Stdio("knowledge", "unused-mcp")), adaptor.WithRunServices(service), adaptor.WithBlockingEvents())
			defer fx.close()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			first, err := fx.agent.Run(ctx, "run")
			if err != nil {
				t.Fatal(err)
			}
			stream := fx.agent.Stream(ctx, "stream")
			calls, results := 0, 0
			var deltas strings.Builder
			for e := range stream.Events() {
				switch v := e.(type) {
				case adaptor.ToolCall:
					if v.Phase == adaptor.PhaseStart {
						calls++
						if v.Name != "DeferExecuteTool" || v.ID != "wrapper-id" || v.Args != nil {
							t.Fatal("original incremental wrapper ToolCall rewritten")
						}
					}
					deltas.WriteString(v.ArgsDelta)
				case adaptor.ToolResult:
					if v.ID == "wrapper-id" {
						results++
					}
				}
			}
			second, err := stream.Result()
			if err != nil || calls != 1 || results != 1 || deltas.String() != tc.input {
				t.Fatalf("public wrapper lifecycle calls=%d results=%d error=%v", calls, results, err)
			}
			for _, r := range []*adaptor.Result{first, second} {
				if r.Raw().Stdout != protocol || r.Raw().Stderr != "" || r.Raw().Terminal == nil || string(r.Raw().Terminal.JSON) != terminal || r.Text != "prefix PRIVATE_MARKER suffix" || r.Usage == nil || r.Usage.InputTokens != 7 || r.Usage.OutputTokens != 3 {
					t.Fatal("exact original Raw/terminal/Text/Usage changed")
				}
				transcriptCalls, transcriptResults := 0, 0
				for _, item := range r.Transcript() {
					if item.ToolUseID != "wrapper-id" {
						continue
					}
					switch item.Kind {
					case driver.TranscriptToolCall:
						transcriptCalls++
						if item.ToolName != "DeferExecuteTool" {
							t.Fatal("original Transcript wrapper rewritten")
						}
					case driver.TranscriptToolResult:
						transcriptResults++
						if item.Text != "PRIVATE_RESULT" || item.IsError != tc.failed {
							t.Fatal("original Transcript result changed")
						}
					}
				}
				if transcriptCalls != 1 || transcriptResults != 2 {
					t.Fatal("wrapper dedupe removed original Transcript audit")
				}
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			wantRuns := 2
			if tc.want == "" {
				wantRuns = 0
			}
			if len(service.events) != wantRuns {
				t.Fatal("Run/Stream observations did not use the same pipeline")
			}
			for _, events := range service.events {
				var facts []capability.Invocation
				for _, e := range events {
					if v, ok := e.(adaptor.CapabilityInvocation); ok {
						facts = append(facts, v.Invocation)
					}
				}
				if tc.want == "" {
					if len(facts) != 0 {
						t.Fatal("unknown/nested wrapper invented public capability")
					}
					continue
				}
				if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != tc.want {
					t.Fatalf("public deferred phases=%v", facts)
				}
				for _, fact := range facts {
					if fact.InvocationID != "wrapper-id" || fact.Ref != (capability.Ref{Kind: capability.MCP, Key: "knowledge", Operation: "search"}) || fact.Evidence != capability.ProviderProtocol {
						t.Fatal("public deferred canonical identity changed")
					}
				}
				raw, _ := json.Marshal(facts)
				if strings.Contains(string(raw), "PRIVATE") {
					t.Fatal("public capability leaked original arguments/results")
				}
			}
		})
	}
}

type alignmentObservationService struct {
	mu     sync.Mutex
	events map[string][]adaptor.Event
}

func (s *alignmentObservationService) AttachRun(_ context.Context, id string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}, Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.events[id] = append(s.events[id], e)
		return nil
	}}, nil
}
func (*alignmentObservationService) DetachRun(context.Context, string) error { return nil }
func TestAlignmentObservationPublicRunStreamAndPersistent(t *testing.T) {
	const native = "T15_NATIVE_RAW_NONCE"
	protocol := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`,
		alignmentCall("mcp__knowledge__search", "m", `{"secret":"not a fact"}`),
		alignmentResult("m", `"ok"`, false, ""),
		alignmentCall("TodoWrite", "clear", `{"newTodos":[]}`),
		alignmentResult("clear", `"Todo list updated successfully"`, false, ""),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"intermediate"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"` + native + `","usage":{"input_tokens":0,"output_tokens":0}}`, ""}, "\n")
	path := filepath.Join(t.TempDir(), "protocol")
	if err := os.WriteFile(path, []byte(protocol), 0600); err != nil {
		t.Fatal(err)
	}
	service := &alignmentObservationService{events: map[string][]adaptor.Event{}}
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path}}, adaptor.WithAppendSystemPrompt(native), adaptor.WithMCP(mcp.Stdio("knowledge", "fixture-unused-mcp")), adaptor.WithRunServices(service))
	defer fx.close()
	for _, runner := range []adaptor.Runner{fx.agent, fx.thread} {
		first, err := runner.Run(context.Background(), "first")
		if err != nil {
			t.Fatal(err)
		}
		stream := runner.Stream(context.Background(), "second")
		var capCount, todoCount int
		for e := range stream.Events() {
			switch event := e.(type) {
			case adaptor.CapabilityInvocation:
				capCount++
				raw, _ := json.Marshal(event)
				if strings.Contains(string(raw), "secret") {
					t.Fatal("args leaked into fact")
				}
			case adaptor.TodoUpdated:
				todoCount++
				if event.Snapshot.Items == nil || len(event.Snapshot.Items) != 0 || event.Snapshot.Revision != 1 {
					t.Fatal("public clear lost")
				}
			case adaptor.Notice:
				raw, _ := json.Marshal(event)
				if event.Kind != adaptor.NoticeTranscriptItem && strings.Contains(string(raw), native) {
					t.Fatalf("native text leaked into SDK notice kind=%s", event.Kind)
				}
			}
		}
		second, err := stream.Result()
		if err != nil {
			t.Fatal(err)
		}
		if capCount != 2 || todoCount != 1 {
			t.Fatalf("public facts %d/%d", capCount, todoCount)
		}
		for _, result := range []*adaptor.Result{first, second} {
			if result.Text != native || result.Summary != "" || !strings.Contains(result.Raw().Stdout, native) || result.Raw().Terminal == nil || len(result.Transcript()) < 5 || result.Usage == nil || result.Usage.InputTokens != 0 {
				t.Fatalf("output layers changed %#v", result)
			}
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.events) != 4 {
		t.Fatalf("observer runs=%d", len(service.events))
	}
	for _, events := range service.events {
		if len(events) != 3 {
			t.Fatalf("observer facts=%d", len(events))
		}
	}
	if fx.spawnCount(t) != 3 {
		t.Fatal("stateless runs + one reused persistent process expected")
	}
}
