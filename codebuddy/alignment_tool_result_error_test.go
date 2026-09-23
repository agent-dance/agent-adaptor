package codebuddy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/mcp"
)

// CodeBuddy 2.157's DeferExecuteTool.convertMcpResult emits this metadata for
// MCP isError=true. Its stream-json transformer sets the independent outer flag
// from function-call status, so a completed call can have outer false/inner true.
func TestAlignmentToolResultInnerErrorFormalFrame(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	p.enableStreaming(p.runID)
	alignmentFeed(t, p,
		alignmentCall("DeferExecuteTool", "inner-error", `{"toolName":"mcp__知识_库___search","params":{}}`),
		alignmentResult("inner-error", `[{"type":"text","text":"synthetic MCP failure"}]`, false, `{"is_error":true}`),
		`{"type":"result","subtype":"success","session_id":"healthy-session","is_error":false,"result":"recovered"}`,
	)
	p.finalize()
	p.completeStream(p.failureForOutcome(0), 0, "", false)
	facts := alignmentCapabilities(rec)
	if len(facts) != 2 || facts[1].Phase != capability.Failed || facts[1].ErrorCode != capability.ToolFailed {
		t.Error("official inner error did not produce failed capability")
	}
	transcript, streamed := false, false
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptToolResult && item.ToolUseID == "inner-error" {
			transcript = true
			if !item.IsError {
				t.Error("official inner error lost in Transcript")
			}
		}
	}
	for _, item := range rec.StreamSnapshot() {
		if item.Kind == driver.StreamToolCallResult && item.ToolCallID == "inner-error" {
			streamed = true
			result := item.Result
			if result["is_error"] != true {
				t.Error("official inner error lost in typed ToolResult")
			}
		}
	}
	if !transcript || !streamed {
		t.Fatal("formal result missing from a typed projection")
	}
	if p.failureForOutcome(0) != nil || p.checkpoint(0) == nil || !p.checkpoint(0).Valid {
		t.Fatal("tool failure incorrectly changed healthy run/checkpoint")
	}
}

func TestAlignmentToolResultErrorBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, flags     string
		failed, invalid bool
	}{
		{"missing", ``, false, false},
		{"outer_false", `,"is_error":false`, false, false},
		{"outer_true", `,"is_error":true`, true, false},
		{"inner_true", `,"_meta":{"rawResponse":{"is_error":true}}`, true, false},
		{"inner_false", `,"_meta":{"rawResponse":{"is_error":false}}`, false, false},
		{"outer_false_inner_true", `,"is_error":false,"_meta":{"rawResponse":{"is_error":true}}`, true, false},
		{"outer_true_inner_false", `,"is_error":true,"_meta":{"rawResponse":{"is_error":false}}`, true, false},
		{"both_true", `,"is_error":true,"_meta":{"rawResponse":{"is_error":true}}`, true, false},
		{"error_code_only", `,"_meta":{"rawResponse":{"tool_error_code":"PRIVATE_ERROR_CODE"}}`, false, false},
		{"opaque_meta", `,"_meta":"PRIVATE_META"`, false, false},
		{"opaque_raw", `,"_meta":{"rawResponse":"PRIVATE_RAW"}`, false, false},
		{"array_raw", `,"_meta":{"rawResponse":[{"is_error":true}]}`, false, false},
		{"null_raw", `,"_meta":{"rawResponse":null}`, false, false},
		{"unknown_nested", `,"_meta":{"rawResponse":{"future":{"is_error":true}}}`, false, false},
		{"wrong_path", `,"rawResponse":{"is_error":true},"_meta":{"is_error":true}`, false, false},
		{"outer_null", `,"is_error":null`, false, true},
		{"outer_string", `,"is_error":"true"`, false, true},
		{"inner_null", `,"_meta":{"rawResponse":{"is_error":null}}`, false, true},
		{"inner_string", `,"_meta":{"rawResponse":{"is_error":"true"}}`, false, true},
		{"inner_number", `,"_meta":{"rawResponse":{"is_error":1}}`, false, true},
		{"inner_array", `,"_meta":{"rawResponse":{"is_error":[true]}}`, false, true},
		{"inner_object", `,"_meta":{"rawResponse":{"is_error":{"value":true}}}`, false, true},
		{"outer_true_inner_malformed", `,"is_error":true,"_meta":{"rawResponse":{"is_error":"false"}}`, true, true},
		{"outer_malformed_inner_true", `,"is_error":"false","_meta":{"rawResponse":{"is_error":true}}`, true, true},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/batch", true: "/stream"}[streaming], func(t *testing.T) {
				rec := &testutil.EventRecorder{}
				p := newParser(rec)
				p.configureObservations(context.Background(), alignmentObservationRequest())
				if streaming {
					p.enableStreaming(p.runID)
				}
				// The body deliberately resembles an error. Only named formal bools
				// can affect the classification, never text or error-code strings.
				result := `{"type":"user","parent_tool_use_id":null,"message":{"content":[{"type":"tool_result","tool_use_id":"flag-control","content":"PRIVATE error is_error=true"` + tc.flags + `}]}}`
				alignmentFeed(t, p, alignmentCall("DeferExecuteTool", "flag-control", `{"toolName":"mcp__知识_库___search","params":{}}`), result, result,
					`{"type":"result","subtype":"success","session_id":"healthy-session","is_error":false,"result":"recovered"}`)
				p.finalize()
				p.completeStream(p.failureForOutcome(0), 0, "", false)
				want := capability.Completed
				if tc.failed {
					want = capability.Failed
				}
				if tc.invalid {
					want = capability.Interrupted
				}
				facts := alignmentCapabilities(rec)
				if len(facts) != 2 || facts[1].Phase != want || alignmentNotice(rec, "observation_result_invalid") != tc.invalid {
					t.Fatal("formal flag validation/capability boundary changed")
				}
				count := 0
				for _, item := range p.transcript {
					if item.Kind == driver.TranscriptToolResult {
						count++
						if item.IsError != tc.failed {
							t.Fatal("Transcript formal bool OR changed")
						}
					}
				}
				if count != 2 {
					t.Fatal("raw duplicate removed Transcript audit")
				}
				count = 0
				for _, item := range rec.StreamSnapshot() {
					if item.Kind == driver.StreamToolCallResult {
						count++
						result := item.Result
						if result["is_error"] != tc.failed {
							t.Fatal("typed ToolResult formal bool OR changed")
						}
					}
				}
				if count != map[bool]int{false: 0, true: 1}[streaming] {
					t.Fatal("identical result wrapper replay boundary changed")
				}
				if p.failureForOutcome(0) != nil || p.checkpoint(0) == nil || !p.checkpoint(0).Valid {
					t.Fatal("per-tool flag changed official healthy terminal/checkpoint")
				}
				encoded, _ := json.Marshal(facts)
				if strings.Contains(string(encoded), "PRIVATE") {
					t.Fatal("capability copied private body or error code")
				}
			})
		}
	}
}

func TestAlignmentToolResultInnerErrorPublicRunStream(t *testing.T) {
	call := alignmentCall("DeferExecuteTool", "inner-error", `{"toolName":"mcp__knowledge__search","params":{}}`)
	result := alignmentResult("inner-error", `[{"type":"text","text":"PRIVATE_MCP_ERROR"}]`, false, `{"is_error":true,"tool_error_code":"PRIVATE_CODE"}`)
	terminal := `{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"recovered"}`
	protocol := strings.Join([]string{`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`, call, result, result, terminal, ""}, "\n")
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
	count := 0
	for event := range stream.Events() {
		if result, ok := event.(adaptor.ToolResult); ok && result.ID == "inner-error" {
			count++
			if result.Result["is_error"] != true {
				t.Fatal("public ToolResult lost formal inner failure")
			}
		}
	}
	second, err := stream.Result()
	if err != nil || count != 1 {
		t.Fatal("tool error changed run success or replay count")
	}
	for _, r := range []*adaptor.Result{first, second} {
		if r.Raw().Stdout != protocol || r.Raw().Terminal == nil || string(r.Raw().Terminal.JSON) != terminal || r.Text != "recovered" {
			t.Fatal("formal Raw/terminal/output changed")
		}
		count := 0
		for _, item := range r.Transcript() {
			if item.Kind == driver.TranscriptToolResult {
				count++
				if !item.IsError || item.Text != "PRIVATE_MCP_ERROR" {
					t.Fatal("public Transcript lost original body or failure")
				}
			}
		}
		if count != 2 {
			t.Fatal("public Transcript audit deduped")
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.events) != 2 {
		t.Fatal("Run and Stream did not use same observation pipeline")
	}
	for _, events := range service.events {
		var facts []capability.Invocation
		for _, event := range events {
			if fact, ok := event.(adaptor.CapabilityInvocation); ok {
				facts = append(facts, fact.Invocation)
			}
		}
		if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != capability.Failed || facts[1].ErrorCode != capability.ToolFailed {
			t.Fatal("public capability lost formal inner error")
		}
		raw, _ := json.Marshal(facts)
		if strings.Contains(string(raw), "PRIVATE") {
			t.Fatal("public capability copied error details")
		}
	}
}
