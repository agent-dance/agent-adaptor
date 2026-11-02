package claude

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// This harness runs the real Claude Stop parser against the real core hub.
// The fake provider starts another turn only when the actual hook reply blocks.
type stopContinuationDriver struct {
	ready, release chan struct{}
	providerTurns  int
}

func (*stopContinuationDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "claude-stop-core-fixture", Messages: claudeMessageCapability()}
}
func (*stopContinuationDriver) ValidateConfig(any) error { return nil }

type stopContinuationStdin struct{ frames chan []byte }

func (w stopContinuationStdin) Write(b []byte) error {
	w.frames <- append([]byte(nil), b...)
	return nil
}
func (stopContinuationStdin) Close() error { return nil }

func (d *stopContinuationDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	p := newClaudeParser(sink)
	s := newInitializedMessageTestSession(ctx, req.Messages)
	p.messages = s
	w := stopContinuationStdin{frames: make(chan []byte, 1)}
	s.start(w, func() {})
	d.providerTurns = 1
	p.handlePayload("", map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "first"}},
	}})
	close(d.ready)
	select {
	case <-d.release:
	case <-ctx.Done():
		s.finish(ctx.Err())
		return driver.Response{}, ctx.Err()
	}
	p.handlePayload("", messageHook("Stop", "", ""))
	frame := <-w.frames
	s.finish(nil) // join the actual writer and all message receipts
	var reply struct {
		Response struct {
			Response struct {
				Decision string `json:"decision"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(frame, &reply); err != nil {
		return driver.Response{}, err
	}
	text := "first"
	if reply.Response.Response.Decision == "block" {
		d.providerTurns++
		text = "second"
	}
	terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false,
		"session_id": "session", "result": text}
	p.handlePayload(string(messageFrame(terminal)), terminal)
	return buildClaudeResponse(req, p, driver.RawStreams{}, 0, "", false, "", "", "")
}

func TestClaudeStopRespectsCoreTurnLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &stopContinuationDriver{ready: make(chan struct{}), release: make(chan struct{})}
	st := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 1}})).Send(ctx, "initial")
	<-d.ready
	if err := st.Send(ctx, "continue", adaptor.MessageID("next"), adaptor.When(adaptor.AfterTurn)); err != nil {
		t.Fatal(err)
	}
	close(d.release)
	result, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	if d.providerTurns != 1 {
		t.Fatalf("MaxTurns=1, but Claude Stop reply started %d provider turns; Result.Turns=%d, messages=%+v, undelivered=%+v", d.providerTurns, result.Turns, result.Messages(), result.Undelivered())
	}
	if result.Turns != 1 {
		t.Fatalf("Turns=%d", result.Turns)
	}
	pending := result.Undelivered()
	if len(pending) != 1 || pending[0].ID != "next" || pending[0].Text != "continue" || pending[0].Reason != adaptor.MessageLimitExceeded {
		t.Fatalf("undelivered=%+v", pending)
	}
}

func TestClaudeStopAttributesGuidanceToSuccessor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &stopContinuationDriver{ready: make(chan struct{}), release: make(chan struct{})}
	st := adaptor.New(d).Send(ctx, "initial")
	<-d.ready
	if err := st.Send(ctx, "continue", adaptor.MessageID("next"), adaptor.When(adaptor.AfterTurn)); err != nil {
		t.Fatal(err)
	}
	close(d.release)
	var ids []string
	for event := range st.Events() {
		if turn, ok := event.(adaptor.TurnEnd); ok && turn.Turn == 2 {
			ids = turn.MessageIDs
		}
	}
	result, err := st.Result()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"next"}) {
		t.Fatalf("Claude processed continuation in turn 2, but TurnEnd(2).MessageIDs=%v; messages=%+v", ids, result.Messages())
	}
}
