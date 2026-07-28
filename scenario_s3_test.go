package adaptor_test

// Scenario S3 · Web chat service: SSE streaming + front-end approval card +
// Thread continuation (docs/api-v1-redesign.md §3 S3).
//
// Target shape, verbatim from the design doc:
//
//	th := agent.Thread(threadKeyFrom(r))        // 续聊：key 复用 / 自动新建
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
//	// “重新生成” = fork
//	alt := th.Fork(newThreadKey())
//
// The HTTP bridge is covered separately by bridges/sse contract tests.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

func TestScenarioS3WebChatApprovalCard(t *testing.T) {
	// The scripted "claude" driver. First turn: streams text, opens a tool
	// call, asks the release question, finishes according to the human's
	// answer, and checkpoints the conversation. Later turns (the thread
	// carries a resume state) continue in context.
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if req.Session != nil && req.Session.State != nil {
			resumeID := req.Session.State.ResumeID
			_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m2", Delta: "In the context of this chat: on it."})
			return driver.Response{
				Output:     "continued:" + resumeID,
				Checkpoint: &driver.Checkpoint{State: &driver.SessionState{ResumeID: resumeID}, Valid: true},
			}, nil
		}

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
		return driver.Response{
			Output:     "Deployed.",
			Checkpoint: &driver.Checkpoint{State: &driver.SessionState{ResumeID: "chat-sess-1"}, Valid: true},
		}, nil
	}

	store := memory.NewStore()
	agent := adaptor.New(fake,
		adaptor.WithThreadStore(store),
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

	// Request 1 — the chat handler, in the doc's exact consumption shape:
	// bind the thread from the request, stream, park approval cards.
	th := agent.Thread("tenant-1/chat-42")
	stream := th.Stream(context.Background(), "deploy release 1.4")
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

	// Request 2 — a later HTTP request builds a fresh handle from the same
	// thread key and continues the conversation (state lives in the store,
	// not in the handle).
	res2, err := agent.Thread("tenant-1/chat-42").Run(context.Background(), "and roll the docs site too")
	if err != nil {
		t.Fatalf("continuation run: %v", err)
	}
	if res2.Text != "continued:chat-sess-1" {
		t.Fatalf("res2.Text = %q, want the conversation continued from chat-sess-1", res2.Text)
	}
	sess := fake.request(t, 1).Session
	if sess == nil || sess.Mode != driver.SessionContinueOrStart || sess.State == nil || sess.State.ResumeID != "chat-sess-1" {
		t.Fatalf("continuation session = %+v, want continue_or_start with chat-sess-1", sess)
	}

	// "Regenerate" — fork the thread; the alternative takes its own key,
	// the original chat stays intact.
	res3, err := th.Fork("tenant-1/chat-42/alt-1").Run(context.Background(), "try a bolder wording")
	if err != nil {
		t.Fatalf("fork run: %v", err)
	}
	if res3.Text != "continued:chat-sess-1" {
		t.Fatalf("res3.Text = %q, want the fork to branch from chat-sess-1", res3.Text)
	}
	sess = fake.request(t, 2).Session
	if sess == nil || sess.Mode != driver.SessionFork || sess.State == nil || sess.State.ResumeID != "chat-sess-1" {
		t.Fatalf("fork session = %+v, want fork carrying chat-sess-1", sess)
	}

	parentRec, err := store.Resolve(context.Background(), threadstore.Query{Key: "tenant-1/chat-42"})
	if err != nil || parentRec == nil {
		t.Fatalf("resolve parent thread: rec=%v err=%v", parentRec, err)
	}
	altRec, err := store.Resolve(context.Background(), threadstore.Query{Key: "tenant-1/chat-42/alt-1"})
	if err != nil || altRec == nil {
		t.Fatalf("resolve fork thread: rec=%v err=%v", altRec, err)
	}
	if parentRec.Status != threadstore.StatusActive || altRec.ID == parentRec.ID {
		t.Fatalf("fork boundary violated: parent=%+v alt=%+v", parentRec, altRec)
	}
}
