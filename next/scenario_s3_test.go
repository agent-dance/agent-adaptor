package adaptor_test

// Scenario S3 · Web chat service: SSE streaming + front-end approval card
// (docs/api-v1-redesign.md §3 S3).
//
// Target shape, verbatim from the design doc:
//
//	stream := th.Stream(r.Context(), promptFrom(r))
//	for ev := range stream.Events() {
//	    switch e := ev.(type) {
//	    case adaptor.TextDelta:
//	        sseWrite(w, "delta", e.Text)
//	    case adaptor.ToolCall:
//	        sseWrite(w, "tool", e)
//	    case *adaptor.ApprovalRequest:
//	        pending.Store(e.ID, e)          // 请求自带应答器
//	        sseWrite(w, "approval", e)
//	    }
//	}
//	if _, err := stream.Result(); err != nil {
//	    sseWrite(w, "error", err.Error())
//	}
//
//	// 审批回包端点
//	if req, ok := pending.LoadAndDelete(idFrom(r)); ok {
//	    req.(*adaptor.ApprovalRequest).Answer(r.Context(), optionFrom(r))
//	}
//
// Thread continuation (agent.Thread(threadKeyFrom(r)) / th.Fork) joins in
// P2 — until then the same consumption shape runs on the stateless Agent
// below. The sse.Handler(agent) one-liner bridge returns in P4.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestScenarioS3WebChatApprovalCard(t *testing.T) {
	// The scripted "claude" driver: streams text, opens a tool call, asks
	// the release question, and finishes according to the human's answer.
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		ds := sink.(driver.DecisionCapableSink)
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "Deploying the release"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "c1", Name: "bash", Args: map[string]any{"cmd": "make deploy"}})

		resp, err := ds.RequestDecision(ctx, driver.DecisionRequest{
			Kind:   driver.HumanDecisionQuestion,
			Source: "release-gate",
			Prompt: "ship release 1.4?",
			Choices: []driver.DecisionChoice{
				{Key: "deploy", Label: "Ship it"},
				{Key: "hold", Label: "Hold"},
			},
		})
		if err != nil {
			return driver.Response{}, err
		}
		if resp.Result != driver.DecisionAnswered || resp.Choice != "deploy" {
			return driver.Response{Output: "held"}, nil
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m1", Delta: " — approved, shipping."})
		return driver.Response{Output: "Deployed."}, nil
	}

	agent := adaptor.New(fake,
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}),
	)

	// The SSE response writer stand-in: frames in arrival order.
	var frames []string
	sseWrite := func(event string, payload any) {
		frames = append(frames, fmt.Sprintf("%s:%v", event, payload))
	}

	// The approval-card round trip: the chat handler parks the request in
	// `pending`, the front end answers through the /resolve endpoint.
	var pending sync.Map
	resolveReq := make(chan string, 1)
	resolveDone := make(chan struct{})
	go func() { // the /resolve endpoint
		defer close(resolveDone)
		id := <-resolveReq
		if req, ok := pending.LoadAndDelete(id); ok {
			_ = req.(*adaptor.ApprovalRequest).Answer(context.Background(), "deploy")
		}
	}()

	// The chat handler, in the doc's exact consumption shape.
	stream := agent.Stream(context.Background(), "deploy release 1.4")
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			sseWrite("delta", e.Text)
		case adaptor.ToolCall:
			sseWrite("tool", e.Name)
		case *adaptor.ApprovalRequest:
			pending.Store(e.ID, e) // the request carries its own responder
			sseWrite("approval", e.Title)
			resolveReq <- e.ID
		}
	}
	res, err := stream.Result()
	if err != nil {
		sseWrite("error", err.Error())
		t.Fatalf("Result: %v", err)
	}
	<-resolveDone

	if res.Text != "Deployed." {
		t.Fatalf("res.Text = %q, want the approved deployment", res.Text)
	}

	want := []string{
		"delta:Deploying the release",
		"tool:bash",
		"approval:ship release 1.4?",
		"delta: — approved, shipping.",
	}
	if len(frames) != len(want) {
		t.Fatalf("frames = %q, want %q", frames, want)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Errorf("frames[%d] = %q, want %q", i, frames[i], want[i])
		}
	}

	// Nothing is left parked: the card was consumed by the resolver.
	pending.Range(func(k, v any) bool {
		t.Errorf("pending approval %v left behind", k)
		return true
	})
}
