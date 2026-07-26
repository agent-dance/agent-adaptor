package adaptertest

import (
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func clauseSet(violations []Violation) map[string]bool {
	out := map[string]bool{}
	for _, v := range violations {
		out[v.Clause] = true
	}
	return out
}

func TestVerifyStreamSequenceAcceptsCompliantStream(t *testing.T) {
	payloads := []driver.StreamPayload{
		{Kind: driver.StreamRunStarted},
		{Kind: driver.StreamStepStarted, Name: "turn"},
		{Kind: driver.StreamReasoningStart, MessageID: "r1"},
		{Kind: driver.StreamReasoningContent, MessageID: "r1", Delta: "thinking"},
		{Kind: driver.StreamReasoningEnd, MessageID: "r1"},
		{Kind: driver.StreamToolCallStart, ToolCallID: "c1", Name: "echo"},
		{Kind: driver.StreamToolCallArgs, ToolCallID: "c1", Delta: `{"a":1}`},
		{Kind: driver.StreamToolCallEnd, ToolCallID: "c1"},
		{Kind: driver.StreamToolCallResult, ToolCallID: "c1", Result: map[string]any{"ok": true}},
		{Kind: driver.StreamTextStart, MessageID: "m1"},
		{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "hel"},
		{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "lo"},
		{Kind: driver.StreamTextEnd, MessageID: "m1"},
		{Kind: driver.StreamStepFinished, Name: "turn"},
		{Kind: "x.vendor-extension"}, // unknown kinds are tolerated
		{Kind: driver.StreamRunFinished, Usage: &driver.Usage{OutputTokens: 2}},
	}
	if vs := VerifyStreamSequence(payloads); len(vs) != 0 {
		t.Fatalf("compliant stream produced violations: %v", vs)
	}
}

func TestVerifyStreamSequenceNegativeTable(t *testing.T) {
	started := driver.StreamPayload{Kind: driver.StreamRunStarted}
	finished := driver.StreamPayload{Kind: driver.StreamRunFinished}
	failure := &driver.RunFailure{Message: "boom", Code: driver.FailureAgentError}

	cases := []struct {
		name       string
		payloads   []driver.StreamPayload
		wantClause string
	}{
		{"text_before_started", []driver.StreamPayload{
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			started,
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "x"},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			finished,
		}, "EVT-01"},
		{"duplicate_started", []driver.StreamPayload{started, started, finished}, "EVT-01"},
		{"duplicate_finished", []driver.StreamPayload{started, finished, finished}, "EVT-02"},
		{"content_after_finished", []driver.StreamPayload{
			started, finished,
			{Kind: driver.StreamTextStart, MessageID: "m1"},
		}, "EVT-02"},
		{"finished_after_error", []driver.StreamPayload{
			started,
			{Kind: driver.StreamRunError, Error: failure},
			finished,
		}, "EVT-02"},
		{"error_without_failure", []driver.StreamPayload{
			started,
			{Kind: driver.StreamRunError},
		}, "EVT-02"},
		{"text_start_without_message_id", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart},
			finished,
		}, "EVT-03"},
		{"text_start_reopens_id", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			finished,
		}, "EVT-03"},
		{"text_content_outside_lifecycle", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextContent, MessageID: "m1", Delta: "x"},
			finished,
		}, "EVT-03"},
		{"text_content_empty_delta", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			{Kind: driver.StreamTextContent, MessageID: "m1"},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			finished,
		}, "EVT-04"},
		{"tool_start_without_name", []driver.StreamPayload{
			started,
			{Kind: driver.StreamToolCallStart, ToolCallID: "c1"},
			{Kind: driver.StreamToolCallEnd, ToolCallID: "c1"},
			finished,
		}, "EVT-05"},
		{"tool_args_before_start", []driver.StreamPayload{
			started,
			{Kind: driver.StreamToolCallArgs, ToolCallID: "c1", Delta: "{"},
			finished,
		}, "EVT-05"},
		{"tool_args_after_end", []driver.StreamPayload{
			started,
			{Kind: driver.StreamToolCallStart, ToolCallID: "c1", Name: "echo"},
			{Kind: driver.StreamToolCallEnd, ToolCallID: "c1"},
			{Kind: driver.StreamToolCallArgs, ToolCallID: "c1", Delta: "{"},
			finished,
		}, "EVT-05"},
		{"tool_result_unknown_id", []driver.StreamPayload{
			started,
			{Kind: driver.StreamToolCallResult, ToolCallID: "ghost"},
			finished,
		}, "EVT-05"},
		{"reasoning_content_outside_lifecycle", []driver.StreamPayload{
			started,
			{Kind: driver.StreamReasoningContent, MessageID: "r1", Delta: "x"},
			finished,
		}, "EVT-06"},
		{"step_without_name", []driver.StreamPayload{
			started,
			{Kind: driver.StreamStepStarted},
			finished,
		}, "EVT-07"},
		{"hitl_requested_without_envelope", []driver.StreamPayload{
			started,
			{Kind: driver.StreamHITLRequested},
			finished,
		}, "EVT-08"},
		{"role_set_by_driver", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart, MessageID: "m1", Role: driver.RoleUser},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			finished,
		}, "EVT-09"},
		{"sequence_set_by_driver", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart, MessageID: "m1", Sequence: 7},
			{Kind: driver.StreamTextEnd, MessageID: "m1"},
			finished,
		}, "EVT-10"},
		{"timestamp_set_by_driver", []driver.StreamPayload{
			{Kind: driver.StreamRunStarted, Timestamp: time.Unix(1, 0)},
			finished,
		}, "EVT-10"},
		{"open_lifecycle_at_finish", []driver.StreamPayload{
			started,
			{Kind: driver.StreamTextStart, MessageID: "m1"},
			finished,
		}, "EVT-11"},
		{"message_id_on_run_frame", []driver.StreamPayload{
			{Kind: driver.StreamRunStarted, MessageID: "m1"},
			finished,
		}, "EVT-13"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clauseSet(VerifyStreamSequence(tc.payloads))
			if !got[tc.wantClause] {
				t.Errorf("violations %v missing clause %s", got, tc.wantClause)
			}
		})
	}
}

func TestVerifyStreamCapabilityNegatives(t *testing.T) {
	payloads := []driver.StreamPayload{
		{Kind: driver.StreamRunStarted},
		{Kind: driver.StreamToolCallStart, ToolCallID: "c1", Name: "echo"},
		{Kind: driver.StreamToolCallArgs, ToolCallID: "c1", Delta: "{"},
		{Kind: driver.StreamToolCallEnd, ToolCallID: "c1"},
		{Kind: driver.StreamReasoningStart, MessageID: "r1"},
		{Kind: driver.StreamReasoningEnd, MessageID: "r1"},
		{Kind: driver.StreamHITLRequested, HITLRequested: &driver.HITLRequestedPayload{RequestID: "q1"}},
		{Kind: driver.StreamRunFinished},
	}
	got := clauseSet(VerifyStreamCapability(driver.StreamCapability{}, payloads))
	if !got["EVT-12"] {
		t.Errorf("want EVT-12 for undeclared args/reasoning/hitl, got %v", got)
	}
	if vs := VerifyStreamCapability(driver.StreamCapability{ToolCallArgs: true, Reasoning: true, HITL: true}, payloads); len(vs) != 0 {
		t.Errorf("fully declared capability produced violations: %v", vs)
	}
}

func TestVerifyRunEventsNegativeTable(t *testing.T) {
	cases := []struct {
		name       string
		events     []driver.RunEvent
		wantClause string
	}{
		{"chunk_bad_stream", []driver.RunEvent{{Type: driver.RunEventChunk, Stream: "combined", Bytes: []byte("x")}}, "RUN-01"},
		{"item_nil", []driver.RunEvent{{Type: driver.RunEventItem}}, "RUN-02"},
		{"seq_set_by_driver", []driver.RunEvent{{Type: driver.RunEventLifecycle, Text: "go", Seq: 3}}, "RUN-03"},
		{"assistant_without_text", []driver.RunEvent{{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptAssistant}}}, "TRN-01"},
		{"tool_call_without_name", []driver.RunEvent{{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptToolCall}}}, "TRN-02"},
		{"tool_result_without_id", []driver.RunEvent{{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptToolResult, Text: "ok"}}}, "TRN-03"},
		{"delta_on_user", []driver.RunEvent{{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptUser, Text: "hi", Delta: true}}}, "TRN-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clauseSet(VerifyRunEvents(tc.events))
			if !got[tc.wantClause] {
				t.Errorf("violations %v missing clause %s", got, tc.wantClause)
			}
		})
	}
}

func TestVerifyResponseNegativeTable(t *testing.T) {
	cases := []struct {
		name       string
		resp       driver.Response
		wantClause string
	}{
		{"valid_checkpoint_without_state", driver.Response{Checkpoint: &driver.Checkpoint{Valid: true}}, "RSP-01"},
		{"valid_checkpoint_without_resume_id", driver.Response{Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{}}}, "RSP-01"},
		{"reject_without_human_decision", driver.Response{Failure: &driver.RunFailure{Message: "no", Code: driver.FailureReject}}, "RSP-02"},
		{"agent_error_with_human_decision", driver.Response{Failure: &driver.RunFailure{
			Message:       "boom",
			Code:          driver.FailureAgentError,
			HumanDecision: &driver.HumanDecisionFailure{},
		}}, "RSP-02"},
		{"question_without_prompt", driver.Response{Question: &driver.RunQuestion{}}, "RSP-03"},
		{"output_is_stdout_dump", driver.Response{
			Output:     "{\"type\":\"a\"}\n{\"type\":\"b\"}",
			RawStreams: &driver.RawStreams{Stdout: "{\"type\":\"a\"}\n{\"type\":\"b\"}"},
		}, "RSP-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clauseSet(VerifyResponse(&tc.resp))
			if !got[tc.wantClause] {
				t.Errorf("violations %v missing clause %s", got, tc.wantClause)
			}
		})
	}
	if vs := VerifyResponse(nil); vs != nil {
		t.Errorf("VerifyResponse(nil) = %v, want nil", vs)
	}
}

func TestVerifyTranscriptMirror(t *testing.T) {
	item := driver.TranscriptItem{Kind: driver.TranscriptAssistant, Text: "hi"}
	events := []driver.RunEvent{{Type: driver.RunEventItem, Item: &item}}
	if vs := VerifyTranscriptMirror(events, []driver.TranscriptItem{item}); len(vs) != 0 {
		t.Errorf("matching mirror produced violations: %v", vs)
	}
	got := clauseSet(VerifyTranscriptMirror(events, nil))
	if !got["RUN-04"] {
		t.Errorf("want RUN-04 for a diverging mirror, got %v", got)
	}
}
