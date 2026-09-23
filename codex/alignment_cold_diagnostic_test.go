//go:build codex_live

package codex

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
)

func TestAlignmentColdFirstTurnDiagnosticPredicates(t *testing.T) {
	const nonce = "标记-closed"
	for _, tc := range []struct {
		name                        string
		calls                       int32
		text                        string
		fail, exact, trim, contains bool
	}{
		{"exact", 1, nonce, false, true, true, true},
		{"whitespace", 1, " \n" + nonce + "\t", false, false, true, true},
		{"no-callback", 0, nonce, true, true, true, true},
		{"duplicate-callback", 2, nonce, true, true, true, true},
		{"extra-text", 1, "reply: " + nonce, true, false, false, true},
		{"different-text", 1, "different", true, false, false, false},
		{"both-fail", 2, "", true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// These are concrete counterexamples to the original combined guard;
			// diagnostics must distinguish its operands without changing the oracle.
			if got := tc.calls != 1 || strings.TrimSpace(tc.text) != nonce; got != tc.fail {
				t.Fatal("original guard classification changed")
			}
			got := alignmentColdFirstTurnDiagnostic(tc.calls, tc.text, nonce, nil)
			if got.Callbacks != tc.calls || got.TextExact != tc.exact || got.TrimmedExact != tc.trim || got.NonceContained != tc.contains {
				t.Fatalf("wrong closed predicates: %+v", got)
			}
			if got.TextBytes != len(tc.text) || got.TrimmedBytes != len(strings.TrimSpace(tc.text)) {
				t.Fatal("byte counts changed")
			}
		})
	}
}

func TestAlignmentColdFirstTurnDiagnosticTranscript(t *testing.T) {
	probe := toolidentity.ServerKey + "/alignment_cold_probe"
	items := []adaptor.TranscriptItem{
		{Kind: driver.TranscriptToolCall, ToolUseID: "probe", ToolName: probe},
		{Kind: driver.TranscriptToolResult, ToolUseID: "probe"},
		{Kind: driver.TranscriptToolResult, ToolUseID: "probe", IsError: true},
		{Kind: driver.TranscriptToolCall, ToolUseID: "other", ToolName: "other"},
		{Kind: driver.TranscriptToolResult, ToolUseID: "other", IsError: true},
		{Kind: driver.TranscriptToolResult, ToolUseID: "missing"},
		{Kind: driver.TranscriptToolCall, ToolName: probe},
		{Kind: driver.TranscriptToolResult},
		{Kind: driver.TranscriptToolCall, ToolUseID: "conflict", ToolName: probe},
		{Kind: driver.TranscriptToolCall, ToolUseID: "conflict", ToolName: "other"},
		{Kind: driver.TranscriptToolResult, ToolUseID: "conflict"},
		{Kind: driver.TranscriptToolCall, ScopeID: "a", ToolUseID: "nested", ToolName: probe},
		{Kind: driver.TranscriptToolResult, ScopeID: "b", ToolUseID: "nested"},
		{Kind: driver.TranscriptToolCall, ParentScopeID: "a", ToolUseID: "parent-scope", ToolName: probe},
		{Kind: driver.TranscriptToolResult, ParentScopeID: "b", ToolUseID: "parent-scope"},
		{Kind: driver.TranscriptToolCall, ParentToolCallID: "a", ToolUseID: "parent-call", ToolName: probe},
		{Kind: driver.TranscriptToolResult, ParentToolCallID: "b", ToolUseID: "parent-call"},
		{Kind: driver.TranscriptAssistant, Text: "not a tool result"},
	}
	got := alignmentColdFirstTurnDiagnostic(1, "marker", "marker", items)
	if got.ToolCalls != 8 || got.ProbeCalls != 6 || got.ToolResults != 9 || got.ToolErrors != 2 || got.ProbeResults != 2 || got.ProbeErrors != 1 || got.UnmatchedResults != 5 || got.AmbiguousResults != 1 {
		t.Fatalf("wrong formal item counts: %+v", got)
	}
	// Replayed identical call coordinates remain unambiguous. Counts remain
	// item counts and are never substituted for the callback oracle.
	items = append(items[:1:1], items[0], items[1])
	got = alignmentColdFirstTurnDiagnostic(1, "marker", "marker", items)
	if got.ProbeCalls != 2 || got.ProbeResults != 1 || got.AmbiguousResults != 0 {
		t.Fatalf("same-name replay became ambiguous: %+v", got)
	}
}

func TestAlignmentColdFirstTurnDiagnosticClosedOutput(t *testing.T) {
	const secret = "DO-NOT-LOG-opaque-sensitive-value"
	items := []adaptor.TranscriptItem{
		{Kind: driver.TranscriptToolCall, ScopeID: secret, ParentScopeID: secret, ParentToolCallID: secret,
			ToolUseID: secret, ToolName: secret, Input: map[string]any{"secret": secret}},
		{Kind: driver.TranscriptToolResult, ScopeID: secret, ParentScopeID: secret, ParentToolCallID: secret,
			ToolUseID: secret, Text: secret, Errors: []string{secret}, Data: map[string]any{"secret": secret}, Metadata: map[string]string{"secret": secret}},
	}
	facts := alignmentColdFirstTurnDiagnostic(2, "extra "+secret, secret, items)
	for _, field := range reflect.VisibleFields(reflect.TypeOf(facts)) {
		switch field.Type.Kind() {
		case reflect.Bool, reflect.Int, reflect.Int32:
		default:
			t.Fatalf("diagnostic field %s can expose content", field.Name)
		}
	}
	output := fmt.Sprintf(alignmentColdFirstTurnDiagnosticFormat, facts)
	if strings.Contains(output, secret) || strings.Contains(output, "extra ") || len(output) > 512 {
		t.Fatal("diagnostic output contains source content or exceeds the fixed bound")
	}
	for _, want := range []string{"Callbacks:2", "TextExact:false", "TrimmedExact:false", "NonceContained:true", "ToolResults:1"} {
		if !strings.Contains(output, want) {
			t.Fatal("diagnostic output lost a closed predicate")
		}
	}
}
