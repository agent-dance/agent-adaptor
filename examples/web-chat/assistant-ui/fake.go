package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// scriptedDriver produces deterministic protocol-demo content without running
// a shell or model. The actual Agent is the Runner; core owns Thread resume.
type scriptedDriver struct{ pace time.Duration }

func (d scriptedDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "assistant-ui-script", DisplayName: "Scripted demo", Sessions: driver.SessionCapability{SupportsResume: true}}
}

// scriptedActivity adds a simulated host review to turn one's failed command.
// Derive identity and attribution from the projected tool, so both live and
// replay use the same scoped IDs. No delegated work or extra model call occurs.
func scriptedActivity(event events.Event) []events.Event {
	tool, ok := event.(*events.ToolCallResultEvent)
	if !ok || tool.Content != "command 13 failed\n[exit=1]" {
		return []events.Event{event}
	}
	id := tool.ToolCallID + ":review"
	snapshot := events.NewActivitySnapshotEvent(id, "command-review", map[string]any{
		"title": "Command review", "status": "running", "parentToolCallId": tool.ToolCallID,
	})
	delta := events.NewActivityDeltaEvent(id, "command-review", []events.JSONPatchOperation{
		{Op: "add", Path: "/text", Value: "Checked 13 commands; command 13 needs attention."},
		{Op: "replace", Path: "/status", Value: "completed"},
	})
	snapshot.TimestampMs = event.GetBaseEvent().TimestampMs
	delta.TimestampMs = event.GetBaseEvent().TimestampMs
	return []events.Event{event, snapshot, delta}
}
func (d scriptedDriver) ValidateConfig(any) error { return nil }
func (d scriptedDriver) SessionConfigFingerprint() (string, error) {
	return "assistant-ui-script/v1", nil
}
func (d scriptedDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, TokenLevel: true, Reasoning: true}
}
func (d scriptedDriver) SessionCodec() driver.SessionCodec { return scriptCodec{} }
func (d scriptedDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	turn := 1
	providerID := "script-provider-" + req.RunID
	if req.Session != nil && req.Session.State != nil {
		previous, err := strconv.Atoi(req.Session.State.Data["turn"])
		if err != nil || previous < 1 {
			return driver.Response{}, fmt.Errorf("invalid scripted checkpoint")
		}
		turn = previous + 1
		providerID = req.Session.EngineSessionID
	}
	emit := func(p driver.StreamPayload) error {
		if d.pace > 0 {
			timer := time.NewTimer(d.pace)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		p.ThreadID = providerID
		return sink.EmitStream(p)
	}
	text := "**Turn 1:** I will check 13 commands."
	if turn > 1 {
		text = fmt.Sprintf("Turn %d continues turn 1 on the same Thread using checkpoint %d. Command 13 failed with exit code 1.", turn, turn-1)
	}
	ps := []driver.StreamPayload{{Kind: driver.StreamTextStart, MessageID: "answer"}, {Kind: driver.StreamTextContent, MessageID: "answer", Delta: text}}
	if turn == 1 {
		ps = []driver.StreamPayload{{Kind: driver.StreamReasoningStart, MessageID: "reason"}, {Kind: driver.StreamReasoningContent, MessageID: "reason", Delta: "I will inspect each scripted command and report failures."}, {Kind: driver.StreamReasoningEnd, MessageID: "reason"}, ps[0], ps[1]}
		for i := 1; i <= 13; i++ {
			id := fmt.Sprintf("command-%02d", i)
			exit := 0
			output := fmt.Sprintf("command %d succeeded", i)
			if i == 13 {
				exit = 1
				output = "command 13 failed"
			}
			ps = append(ps, driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: id, Name: "shell", Args: map[string]any{"command": fmt.Sprintf("printf 'command %d'", i)}}, driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: id, Name: "shell"}, driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: id, Name: "shell", Result: map[string]any{"output": output, "exitCode": exit}})
		}
		text += "\n\nFinished: 12 succeeded; **command 13 failed (exit code 1)**. Ask again to continue this Thread."
		ps = append(ps, driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "answer", Delta: "\n\nFinished: 12 succeeded; **command 13 failed (exit code 1)**. Ask again to continue this Thread."})
	}
	ps = append(ps, driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "answer"})
	ps = append([]driver.StreamPayload{{Kind: driver.StreamRunStarted}}, ps...)
	ps = append(ps, driver.StreamPayload{Kind: driver.StreamRunFinished})
	for _, p := range ps {
		if err := emit(p); err != nil {
			return driver.Response{}, err
		}
	}
	// Core turns each provider turn into TurnEnd; guidance sent during this turn
	// is delivered at its end by resuming the checkpoint in the same execution.
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Text: text, CompletedAt: time.Now().UTC()}})
	return driver.Response{Output: text, Summary: fmt.Sprintf("Scripted turn %d", turn), Provider: "script", Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: providerID, Data: map[string]string{"turn": strconv.Itoa(turn)}}}}, nil
}

type scriptCodec struct{}

func (scriptCodec) Name() string { return "assistant-ui-script/v1" }
func (scriptCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: s.DisplayID, Values: s.Data}
}
func (scriptCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" && p.DisplayID == "" && len(p.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: p.DisplayID, Data: p.Values}
}

// The turn counter is checkpoint content, not an environment compatibility guard.
func (scriptCodec) GuardFingerprint(driver.SessionParams) string { return "assistant-ui-script/v1" }
