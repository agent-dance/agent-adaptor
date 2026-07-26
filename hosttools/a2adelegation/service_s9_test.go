package a2adelegation_test

// S9 (CI version, P4.10): the team-collaboration scenario on fake drivers.
// A fake leader driver reaches the Service's per-run MCP sidecar over real
// HTTP (bearer auth, JSON-RPC tools/call), delegates to three Local roles
// backed by scripted fake drivers, and the delegation events converge onto
// the leader's own stream through subagentstream.Merge. Assertions cover:
// event confluence (per-agent ordering, projected kinds, delta fidelity),
// result recording (team.Result + HasLine, recorded before the terminal
// event is visible), the Observe tap, and Close draining. No sleeps: all
// synchronization is channel-based, so the test is -count=5 stable.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// scriptedRoleDriver is a deterministic fake driver.Driver for team-member
// roles: it streams one text lifecycle (start / deltas / end) and finishes
// with a final output or a business failure.
type scriptedRoleDriver struct {
	kind   string
	deltas []string
	final  string
	fail   *driver.RunFailure
}

func (d *scriptedRoleDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: d.kind, DisplayName: d.kind}
}

func (d *scriptedRoleDriver) ValidateConfig(any) error { return nil }

func (d *scriptedRoleDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if len(d.deltas) > 0 {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, RunID: req.RunID, MessageID: "m1"})
		for _, delta := range d.deltas {
			_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, RunID: req.RunID, MessageID: "m1", Delta: delta})
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, RunID: req.RunID, MessageID: "m1"})
	}
	if d.fail != nil {
		return driver.Response{Failure: d.fail}, nil
	}
	return driver.Response{Output: d.final}, nil
}

type leaderCall struct {
	agent     string
	objective string
}

// mcpLeaderDriver is the fake leader driver.Driver: it asks the Service for
// its per-run sidecar and performs each delegation through the real MCP
// HTTP endpoint, exactly like a production CLI driver configured with an
// mcp_servers entry would.
type mcpLeaderDriver struct {
	service *a2adelegation.Service
	calls   []leaderCall
	output  string
}

func (d *mcpLeaderDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "fake-leader", DisplayName: "fake leader"}
}

func (d *mcpLeaderDriver) ValidateConfig(any) error { return nil }

func (d *mcpLeaderDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	sc, err := d.service.EnsureSidecar(req.RunID)
	if err != nil {
		return driver.Response{}, err
	}
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, RunID: req.RunID, MessageID: "leader-m1"})
	for _, call := range d.calls {
		result, err := callDelegateTool(ctx, sc, call.agent, call.objective)
		if err != nil {
			return driver.Response{}, err
		}
		if result.Status != "completed" {
			return driver.Response{Failure: &driver.RunFailure{Message: fmt.Sprintf("delegation to %s ended %s", call.agent, result.Status)}}, nil
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, RunID: req.RunID, MessageID: "leader-m1", Delta: call.agent + " done. "})
	}
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, RunID: req.RunID, MessageID: "leader-m1"})
	return driver.Response{Output: d.output}, nil
}

// callDelegateTool performs one delegate_to_agent tools/call against the
// sidecar endpoint and decodes the DelegationResult from the tool result.
func callDelegateTool(ctx context.Context, sc a2adelegation.Sidecar, agent, objective string) (a2adelegation.DelegationResult, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      a2adelegation.DelegateToolName,
			"arguments": map[string]any{"agent": agent, "objective": objective},
		},
	})
	if err != nil {
		return a2adelegation.DelegationResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, sc.URL, bytes.NewReader(body))
	if err != nil {
		return a2adelegation.DelegationResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+sc.BearerToken)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return a2adelegation.DelegationResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return a2adelegation.DelegationResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return a2adelegation.DelegationResult{}, fmt.Errorf("sidecar status %d: %s", resp.StatusCode, raw)
	}
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return a2adelegation.DelegationResult{}, fmt.Errorf("decode rpc response: %w (%s)", err, raw)
	}
	if rpc.Error != nil {
		return a2adelegation.DelegationResult{}, fmt.Errorf("rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}
	if len(rpc.Result.Content) == 0 {
		return a2adelegation.DelegationResult{}, fmt.Errorf("empty tool result: %s", raw)
	}
	var out a2adelegation.DelegationResult
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &out); err != nil {
		return a2adelegation.DelegationResult{}, fmt.Errorf("decode delegation result: %w (%s)", err, rpc.Result.Content[0].Text)
	}
	return out, nil
}

func TestServiceTeamCollaborationS9(t *testing.T) {
	planner := adaptor.New(&scriptedRoleDriver{
		kind:   "fake-plan",
		deltas: []string{"drafting ", "the plan"},
		final:  "PLAN_READY\n1. implement\n2. review",
	})
	implementer := adaptor.New(&scriptedRoleDriver{
		kind:   "fake-impl",
		deltas: []string{"implementing"},
		final:  "IMPL_DONE",
	})
	reviewer := adaptor.New(&scriptedRoleDriver{
		kind:   "fake-review",
		deltas: []string{"reviewing"},
		final:  "all checks passed\nTEAM_REVIEW_APPROVED",
	})

	var observedMu sync.Mutex
	var observed []a2adelegation.Event

	team, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("plan", planner, a2adelegation.Policy{MaxTimeout: time.Minute}),
			a2adelegation.Local("impl", implementer, a2adelegation.Policy{MaxTimeout: time.Minute}),
			a2adelegation.Local("review", reviewer, a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
		ToolTimeout: time.Minute,
		Observe: func(ev a2adelegation.Event) {
			observedMu.Lock()
			observed = append(observed, ev)
			observedMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer team.Close()

	leader := adaptor.New(&mcpLeaderDriver{
		service: team,
		calls: []leaderCall{
			{agent: "plan", objective: "draft the plan"},
			{agent: "impl", objective: "implement the plan"},
			{agent: "review", objective: "review the implementation"},
		},
		output: "team workflow complete",
	})

	ctx := context.Background()
	stream := leader.Stream(ctx, "coordinate the team")
	merged := subagentstream.Merge(ctx, stream, team.Bus())
	if merged.RunID() != stream.RunID() {
		t.Fatalf("merged.RunID() = %q, want %q", merged.RunID(), stream.RunID())
	}
	runID := merged.RunID()

	var leaderText strings.Builder
	perAgent := map[string][]adaptor.SubagentUpdate{}
	var agentOrder []string
	for ev := range merged.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			leaderText.WriteString(e.Text)
		case adaptor.SubagentUpdate:
			if len(perAgent[e.Agent]) == 0 {
				agentOrder = append(agentOrder, e.Agent)
			}
			perAgent[e.Agent] = append(perAgent[e.Agent], e)
			if e.Kind == adaptor.SubagentFinished {
				// The Service records the result in its lifecycle hook
				// before the Delegator flushes the buffered terminal
				// event, so at this point Result must already answer.
				if _, ok := team.Result(runID, e.Agent); !ok {
					t.Errorf("team.Result(%q, %q) not recorded before terminal SubagentUpdate", runID, e.Agent)
				}
			}
		}
	}

	res, err := merged.Result()
	if err != nil {
		t.Fatalf("merged.Result: %v", err)
	}
	if res.Text != "team workflow complete" {
		t.Errorf("leader result text = %q, want %q", res.Text, "team workflow complete")
	}
	if got, want := leaderText.String(), "plan done. impl done. review done. "; got != want {
		t.Errorf("leader stream text = %q, want %q", got, want)
	}

	wantOrder := []string{"plan", "impl", "review"}
	if len(agentOrder) != len(wantOrder) {
		t.Fatalf("subagent order = %v, want %v", agentOrder, wantOrder)
	}
	for i, agent := range wantOrder {
		if agentOrder[i] != agent {
			t.Fatalf("subagent order = %v, want %v", agentOrder, wantOrder)
		}
	}

	wantDeltas := map[string]string{
		"plan":   "drafting the plan",
		"impl":   "implementing",
		"review": "reviewing",
	}
	for _, agent := range wantOrder {
		updates := perAgent[agent]
		if len(updates) < 3 {
			t.Fatalf("agent %q: got %d subagent updates, want at least started/delta/finished", agent, len(updates))
		}
		first, last := updates[0], updates[len(updates)-1]
		if first.Kind != adaptor.SubagentStarted {
			t.Errorf("agent %q: first update kind = %q, want started", agent, first.Kind)
		}
		if got := first.Data["kind"]; got != string(a2adelegation.DelegationStarted) {
			t.Errorf("agent %q: first Data[kind] = %v, want %q", agent, got, a2adelegation.DelegationStarted)
		}
		if last.Kind != adaptor.SubagentFinished {
			t.Errorf("agent %q: last update kind = %q, want finished", agent, last.Kind)
		}
		if got := last.Data["kind"]; got != string(a2adelegation.DelegationFinished) {
			t.Errorf("agent %q: last Data[kind] = %v, want %q", agent, got, a2adelegation.DelegationFinished)
		}
		for _, mid := range updates[1 : len(updates)-1] {
			if mid.Kind != adaptor.SubagentDelta {
				t.Errorf("agent %q: middle update kind = %q (Data %v), want delta", agent, mid.Kind, mid.Data)
			}
		}
		var text strings.Builder
		for _, u := range updates {
			if u.Data["kind"] == string(a2adelegation.DelegationTextDelta) {
				text.WriteString(u.Delta)
			}
		}
		if text.String() != wantDeltas[agent] {
			t.Errorf("agent %q: streamed text = %q, want %q", agent, text.String(), wantDeltas[agent])
		}
	}

	// §9.7 result recording: team.Result + HasLine sentinel gating.
	review, ok := team.Result(runID, "review")
	if !ok {
		t.Fatalf("team.Result(%q, review): not recorded", runID)
	}
	if review.Status != "completed" {
		t.Errorf("review status = %q, want completed", review.Status)
	}
	if !review.HasLine("TEAM_REVIEW_APPROVED") {
		t.Errorf("review.HasLine(TEAM_REVIEW_APPROVED) = false, result %+v", review)
	}
	if review.HasLine("TEAM_REVIEW_REJECTED") {
		t.Errorf("review.HasLine(TEAM_REVIEW_REJECTED) = true, want false")
	}
	plan, ok := team.Result(runID, "plan")
	if !ok {
		t.Fatalf("team.Result(%q, plan): not recorded", runID)
	}
	if !plan.HasLine("PLAN_READY") {
		t.Errorf("plan.HasLine(PLAN_READY) = false, summary %q", plan.Summary)
	}
	if results := team.Results(runID); len(results) != 3 {
		t.Errorf("team.Results len = %d, want 3", len(results))
	}

	// Close waits for the Observe forwarder to drain; afterwards the
	// callback slice is stable and must contain one terminal per role.
	if err := team.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	terminals := map[string]int{}
	for _, ev := range observed {
		if ev.Kind == a2adelegation.DelegationFinished {
			terminals[ev.AgentKey]++
		}
	}
	for _, agent := range wantOrder {
		if terminals[agent] != 1 {
			t.Errorf("observed %d DelegationFinished for %q, want 1 (observed %d events total)", terminals[agent], agent, len(observed))
		}
	}
}
