package a2a

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func TestLargeRequiredLifecycleRoundTrip(t *testing.T) {
	info := &a2asrv.ExecutorContext{TaskID: "task", ContextID: "context"}
	text := strings.Repeat("long answer 世界\n", 8<<10)
	for _, kind := range []string{"message.update", "turn.end", "done", "done.failed"} {
		t.Run(kind, func(t *testing.T) {
			translator := newStreamTranslator(info, ExposurePolicy{})
			var status *a2aproto.TaskStatusUpdateEvent
			switch kind {
			case "message.update":
				status = translator.Translate(adaptor.MessageUpdate{ID: "m", State: adaptor.MessageFailed, Reason: adaptor.MessageRunFailed, Text: text})[0].(*a2aproto.TaskStatusUpdateEvent)
			case "turn.end":
				status = translator.Translate(adaptor.TurnEnd{Turn: 1, Result: &adaptor.Result{Text: text}})[0].(*a2aproto.TaskStatusUpdateEvent)
			case "done":
				status = translator.terminal(a2aproto.NewStatusUpdateEvent(info, a2aproto.TaskStateCompleted, nil), &adaptor.Result{Text: text}, nil)
			case "done.failed":
				failure := &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "failed", Result: &adaptor.Result{Text: text}}
				status = translator.terminal(a2aproto.NewStatusUpdateEvent(info, a2aproto.TaskStateFailed, nil), nil, failure)
			}
			part := status.Status.Message.Parts[0].Content.(a2aproto.Data)
			// Exercise actual JSON wire normalization, including the decoder cap.
			raw, err := json.Marshal(part.Value)
			if err != nil || len(raw) <= adapterStreamMaxBytes {
				t.Fatalf("large lifecycle encoding: bytes=%d err=%v", len(raw), err)
			}
			var wire any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			decoded, matched, err := DecodeAdapterEventV1(wire)
			if err != nil || !matched {
				t.Fatalf("decode: matched=%v err=%v", matched, err)
			}
			if dropped, ok := decoded.(adaptor.Dropped); ok {
				t.Fatalf("required %s replaced by Dropped: %+v", kind, dropped)
			}
			switch event := decoded.(type) {
			case adaptor.MessageUpdate:
				if kind != "message.update" || event.ID != "m" || event.State != adaptor.MessageFailed || event.Reason != adaptor.MessageRunFailed || event.Text != text {
					t.Fatalf("failed message lost its receipt or original text: %+v", event)
				}
			case adaptor.TurnEnd:
				if kind != "turn.end" || event.Turn != 1 || event.Result == nil || event.Result.Text != text {
					t.Fatal("turn result lost its complete answer")
				}
			case adaptor.Done:
				if kind == "done.failed" {
					var failure *adaptor.RunError
					if event.Result != nil || !errors.As(event.Err, &failure) || failure.Reason != adaptor.ReasonAgentError || failure.Result == nil || failure.Result.Text != text {
						t.Fatal("Done lost its failure or complete partial result")
					}
				} else if kind != "done" || event.Err != nil || event.Result == nil || event.Result.Text != text {
					t.Fatal("Done lost its complete answer")
				}
			default:
				t.Fatalf("unexpected required event type %T", decoded)
			}
		})
	}
}

func TestLargeOptionalStreamEventRetainsCap(t *testing.T) {
	translator := newStreamTranslator(&a2asrv.ExecutorContext{TaskID: "task"}, ExposurePolicy{})
	text := strings.Repeat("x", 70<<10)
	events := translator.Translate(adaptor.TextDelta{Text: text})
	decoded, matched, err := DecodeAdapterEventV1(dataValue(t, events[0]))
	if err != nil || !matched {
		t.Fatalf("decode drop: matched=%v err=%v", matched, err)
	}
	drop, ok := decoded.(adaptor.Dropped)
	if !ok || drop.Count != 1 || !strings.Contains(drop.Reason, "exceeds 65536 bytes") {
		t.Fatalf("optional oversized event did not report a drop: %+v", decoded)
	}
	envelope := AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: AdapterStreamEventV1{Kind: "text.content", Delta: text}}
	if _, matched, err := DecodeAdapterEventV1(envelope); !matched || err == nil {
		t.Fatalf("oversized optional wire event bypassed decoder cap: matched=%v err=%v", matched, err)
	}
}
