package sse_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

func TestSSEHandlerAGUIRealEventBusSubagentEventsPrecedeParentFinished(t *testing.T) {
	t.Parallel()

	adapter := newDelegateBoundaryAdapter()
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(adapter, nil)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
	bus := a2adelegation.NewEventBus(8)
	srv := httptest.NewServer(sse.Handler(sdk, sse.Options{Protocol: sse.AGUI, SubagentBus: bus}))
	defer srv.Close()

	body := strings.NewReader(`{
		"threadId": "t-1",
		"runId": "r-1",
		"messages": [{"id":"m-1","role":"user","content":"delegate this"}]
	}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	runID := adapter.waitDelegateToolActive(t)
	for _, ev := range []a2adelegation.DelegationEvent{
		{
			RunID:            runID,
			ParentToolCallID: "tool-delegate-1",
			DelegationID:     "del-1",
			AgentKey:         "research",
			AgentName:        "Research Agent",
			Protocol:         a2adelegation.ProtocolA2A,
			Kind:             a2adelegation.DelegationStarted,
			Status:           "running",
		},
		{
			RunID:            runID,
			ParentToolCallID: "tool-delegate-1",
			DelegationID:     "del-1",
			AgentKey:         "research",
			Protocol:         a2adelegation.ProtocolA2A,
			RemoteMessageID:  "msg-1",
			Kind:             a2adelegation.DelegationTextDelta,
			Delta:            "subagent text",
		},
		{
			RunID:            runID,
			ParentToolCallID: "tool-delegate-1",
			DelegationID:     "del-1",
			AgentKey:         "research",
			Protocol:         a2adelegation.ProtocolA2A,
			RemoteArtifactID: "artifact-1",
			Kind:             a2adelegation.DelegationArtifactCreated,
			Artifact: &a2adelegation.DelegationArtifact{
				ID:        "artifact-1",
				Name:      "notes.md",
				MediaType: "text/markdown",
			},
		},
		{
			RunID:            runID,
			ParentToolCallID: "tool-delegate-1",
			DelegationID:     "del-1",
			AgentKey:         "research",
			Protocol:         a2adelegation.ProtocolA2A,
			Kind:             a2adelegation.DelegationFinished,
			Status:           "completed",
			Text:             "done",
		},
	} {
		if !bus.Publish(ev) {
			t.Fatalf("publish %s returned false", ev.Kind)
		}
	}

	adapter.finishParent()

	frames := readSSEFrames(t, resp.Body, 2*time.Second)
	var types []string
	customIndex := map[string]int{}
	runFinishedIndex := -1
	for _, f := range frames {
		if f.data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			t.Fatalf("decode frame data: %v (raw=%q)", err, f.data)
		}
		typ, _ := payload["type"].(string)
		if typ == "" {
			continue
		}
		idx := len(types)
		types = append(types, typ)
		if typ == "RUN_FINISHED" {
			runFinishedIndex = idx
		}
		if typ == "CUSTOM" {
			name, _ := payload["name"].(string)
			if _, exists := customIndex[name]; !exists {
				customIndex[name] = idx
			}
			assertSubagentCustomPayload(t, payload, runID)
		}
	}
	if runFinishedIndex < 0 {
		t.Fatalf("RUN_FINISHED not observed; types=%v", types)
	}
	for _, name := range []string{
		string(a2adelegation.DelegationStarted),
		string(a2adelegation.DelegationTextDelta),
		string(a2adelegation.DelegationArtifactCreated),
		string(a2adelegation.DelegationFinished),
	} {
		idx, ok := customIndex[name]
		if !ok {
			t.Fatalf("%s CUSTOM event not observed; types=%v custom=%v", name, types, customIndex)
		}
		if idx >= runFinishedIndex {
			t.Fatalf("%s CUSTOM event index %d must precede RUN_FINISHED index %d; types=%v", name, idx, runFinishedIndex, types)
		}
	}
}

func assertSubagentCustomPayload(t *testing.T, payload map[string]any, runID string) {
	t.Helper()
	value, _ := payload["value"].(map[string]any)
	if value == nil {
		t.Fatalf("CUSTOM payload missing value: %#v", payload)
	}
	if value["runId"] != runID || value["parentToolCallId"] != "tool-delegate-1" ||
		value["delegationId"] != "del-1" || value["agentKey"] != "research" {
		t.Fatalf("unexpected subagent custom value: %#v", value)
	}
}

type delegateBoundaryAdapter struct {
	started chan string
	release chan struct{}
	once    sync.Once
}

func newDelegateBoundaryAdapter() *delegateBoundaryAdapter {
	return &delegateBoundaryAdapter{
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
}

func (a *delegateBoundaryAdapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "delegate-boundary", DisplayName: "Delegate Boundary"}
}
func (a *delegateBoundaryAdapter) ValidateConfig(any) error { return nil }
func (a *delegateBoundaryAdapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{Native: true, TokenLevel: true, ToolCallArgs: true}
}
func (a *delegateBoundaryAdapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	if err := sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunStarted, ThreadID: "t", RunID: req.RunID}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if err := sink.EmitStream(agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamToolCallStart,
		ToolCallID: "tool-delegate-1",
		Name:       a2adelegation.DelegateToolName,
	}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if err := sink.EmitStream(agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamToolCallArgs,
		ToolCallID: "tool-delegate-1",
		Delta:      `{"agent":"research","objective":"collect evidence","constraints":{"stream":true}}`,
	}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	select {
	case a.started <- req.RunID:
	default:
	}

	select {
	case <-ctx.Done():
		return agentadaptor.DriverRunResult{}, ctx.Err()
	case <-a.release:
	}
	if err := sink.EmitStream(agentadaptor.StreamPayload{Kind: agentadaptor.StreamRunFinished, ThreadID: "t", RunID: req.RunID}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	return agentadaptor.DriverRunResult{Output: "done", ExitCode: 0}, nil
}

func (a *delegateBoundaryAdapter) waitDelegateToolActive(t *testing.T) string {
	t.Helper()
	select {
	case runID := <-a.started:
		return runID
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for delegate_to_agent tool call to become active")
		return ""
	}
}

func (a *delegateBoundaryAdapter) finishParent() {
	a.once.Do(func() {
		close(a.release)
	})
}
