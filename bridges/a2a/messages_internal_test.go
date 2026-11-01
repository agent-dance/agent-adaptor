package a2a

import (
	"context"
	"errors"
	"strings"
	"testing"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func TestMessageAuthorRoleAndMetadata(t *testing.T) {
	tests := []struct {
		name     string
		role     a2aproto.MessageRole
		metadata map[string]any
		want     adaptor.Author
		invalid  bool
	}{
		{name: "human", role: a2aproto.MessageRoleUser, want: adaptor.Author{Kind: adaptor.AuthorHuman}},
		{name: "agent", role: a2aproto.MessageRoleAgent, want: adaptor.Author{Kind: adaptor.AuthorAgent}},
		{name: "peer metadata", role: a2aproto.MessageRoleUser, metadata: map[string]any{"agentadaptor.author": map[string]any{"kind": "agent", "id": "peer", "name": "Reviewer"}}, want: adaptor.Author{Kind: adaptor.AuthorAgent, ID: "peer", Name: "Reviewer"}},
		{name: "host authority rejected", role: a2aproto.MessageRoleUser, metadata: map[string]any{"agentadaptor.author": map[string]any{"kind": "system"}}, invalid: true},
		{name: "peer elevation rejected", role: a2aproto.MessageRoleAgent, metadata: map[string]any{"agentadaptor.author": map[string]any{"kind": "human"}}, invalid: true},
		{name: "malformed author rejected", role: a2aproto.MessageRoleUser, metadata: map[string]any{"agentadaptor.author": "agent"}, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			author, err := messageAuthor(Message{Role: string(test.role), Metadata: test.metadata})
			if (err != nil) != test.invalid || (err == nil && author != test.want) {
				t.Fatal(author, err)
			}
		})
	}
}

func TestLifecycleRoundTripPreservesJoinedFailureIdentity(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		original := errors.Join(&adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "teardown", Result: &adaptor.Result{Text: "partial"}}, sentinel)
		decoded, err := decodeLifecycle(AdapterStreamEventV1{Kind: "done", Result: lifecyclePayload(nil, original, ExposurePolicy{})})
		if err != nil {
			t.Fatal(err)
		}
		done := decoded.(adaptor.Done)
		var runErr *adaptor.RunError
		if !errors.Is(done.Err, sentinel) || !errors.As(done.Err, &runErr) || runErr.Result == nil || runErr.Result.Text != "partial" {
			t.Fatalf("joined error lost identity or partial result: %#v", done)
		}
	}
}

func TestBusyContextRaceReturnsRejectedTaskWithActiveIdentity(t *testing.T) {
	exec := &executor{
		session: Stateless(), prompt: defaultPrompt,
		contexts: map[string]a2aproto.TaskID{"context": "active"},
	}
	input := &a2asrv.ExecutorContext{TaskID: "new", ContextID: "context", Message: &a2aproto.Message{ID: "input", Role: a2aproto.MessageRoleUser, Parts: a2aproto.ContentParts{a2aproto.NewTextPart("objective")}}}
	var final *a2aproto.TaskStatusUpdateEvent
	for event, err := range exec.Execute(t.Context(), input) {
		if err != nil {
			t.Fatal(err)
		}
		if status, ok := event.(*a2aproto.TaskStatusUpdateEvent); ok {
			final = status
		}
	}
	if final == nil || final.Status.State != a2aproto.TaskStateRejected || final.Status.Message == nil {
		t.Fatalf("busy execution did not return a rejected task: %#v", final)
	}
	part := final.Status.Message.Parts[0]
	if !strings.Contains(string(part.Content.(a2aproto.Text)), "active") {
		t.Fatalf("active task identity absent: %#v", final)
	}
	details := part.Metadata["agentadaptor.failure"].(map[string]any)
	if details["code"] != "task_in_progress" || details["activeTaskId"] != "active" {
		t.Fatalf("busy protocol details: %#v", details)
	}
}

func TestStreamingRejectsInvalidMessageBeforeExecution(t *testing.T) {
	// No wrapped handler is installed: reaching execution would panic. Both
	// task creation and same-task input must enforce the protocol role.
	handler := &messageRouter{streaming: true}
	for _, taskID := range []a2aproto.TaskID{"", "active"} {
		request := &a2aproto.SendMessageRequest{Message: &a2aproto.Message{
			ID: "input", TaskID: taskID, Role: "system",
			Parts: a2aproto.ContentParts{a2aproto.NewTextPart("objective")},
		}}
		count := 0
		for event, err := range handler.SendStreamingMessage(t.Context(), request) {
			count++
			if event != nil || !errors.Is(err, a2aproto.ErrInvalidParams) {
				t.Fatalf("invalid role was not rejected: %#v %v", event, err)
			}
		}
		if count != 1 {
			t.Fatalf("rejections=%d", count)
		}
	}
}

func TestLifecycleDecoderRejectsMalformedPayloads(t *testing.T) {
	for _, event := range []AdapterStreamEventV1{
		{Kind: "message.update"},
		{Kind: "message.update", Result: map[string]any{"ID": "m", "State": 0}},
		{Kind: "turn.end", Result: map[string]any{"Turn": 0}},
		{Kind: "done", Result: map[string]any{}},
	} {
		if _, err := decodeLifecycle(event); err == nil {
			t.Fatalf("accepted malformed lifecycle: %+v", event)
		}
	}
}
