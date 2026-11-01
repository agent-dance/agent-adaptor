package adaptor

import (
	"encoding/json"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"reflect"
	"testing"
)

var driverStructuredForTest = driver.StructuredOutput{Valid: true, RawJSON: json.RawMessage(`{"answer":42}`)}

func TestComposeResultsUsesCoreAggregation(t *testing.T) {
	first := &Result{RunID: "one", Text: "first", Summary: "first summary", Turns: 2, Usage: &Usage{InputTokens: 3, CachedInputTokens: 2}, Metadata: map[string]string{"part": "one"}, raw: RawStreams{Stdout: "out1", Stderr: "err1", Terminal: &TerminalPayload{JSON: json.RawMessage(`{"one":1}`)}}, transcript: []TranscriptItem{{Text: "one", Metadata: map[string]string{"key": "value"}}}, services: []ServiceReport{{ID: "one", Metadata: map[string]string{"key": "value"}}}, messages: []MessageOutcome{{ID: "one", Turn: 1}}, subagents: []SubagentResult{{Task: "one", Usage: &Usage{InputTokens: 4}}}}
	last := &Result{RunID: "two", Text: "last", Summary: "last summary", Model: "m", Provider: "p", Turns: 3, Usage: &Usage{InputTokens: 5, OutputTokens: 7}, Metadata: map[string]string{"part": "two"}, raw: RawStreams{Stdout: "out2", Stderr: "err2"}, transcript: []TranscriptItem{{Text: "two"}}, services: []ServiceReport{{ID: "two"}}, messages: []MessageOutcome{{ID: "two", Turn: 1}}, subagents: []SubagentResult{{Task: "two"}}, structured: &driverStructuredForTest}
	combined := ComposeResults("composite", nil, first, last)
	if combined.RunID != "composite" || combined.Text != "last" || combined.Summary != "last summary" || combined.Model != "m" || combined.Provider != "p" || combined.Turns != 5 || *combined.Usage != (Usage{InputTokens: 8, OutputTokens: 7, CachedInputTokens: 2}) {
		t.Fatalf("result=%+v", combined)
	}
	if combined.Raw().Stdout != "out1out2" || combined.Raw().Stderr != "err1err2" || string(combined.Raw().Terminal.JSON) != `{"one":1}` || len(combined.Transcript()) != 2 || len(combined.Services()) != 2 || len(combined.Messages()) != 2 || len(combined.Subagents()) != 2 {
		t.Fatal("lost ordered audit layers")
	}
	if combined.Messages()[0].Turn != 1 || combined.Messages()[1].Turn != 3 {
		t.Fatal("message turns were not rebased")
	}
	combined.transcript[0].Metadata["key"] = "changed"
	combined.services[0].Metadata["key"] = "changed"
	combined.subagents[0].Usage.InputTokens = 99
	combined.Usage.InputTokens = 99
	combined.Metadata["part"] = "changed"
	combined.raw.Terminal.JSON[0] = 'x'
	if first.transcript[0].Metadata["key"] != "value" || first.services[0].Metadata["key"] != "value" || first.subagents[0].Usage.InputTokens != 4 || first.Usage.InputTokens != 3 || last.Metadata["part"] != "two" || first.raw.Terminal.JSON[0] != '{' {
		t.Fatal("composition mutated a child")
	}
	var decoded map[string]int
	if err := combined.Decode(&decoded); err != nil || decoded["answer"] != 42 {
		t.Fatal(decoded, err)
	}
	if got := ComposeResults("empty"); got.RunID != "empty" || got.Usage != nil || got.Turns != 0 {
		t.Fatal(got)
	}
}

func TestMessageAttributesAndTurnBudget(t *testing.T) {
	if got := MessageAttributes(); got.From.Kind != AuthorHuman || got.Timing != Soon || got.ID != "" {
		t.Fatal(got)
	}
	author := Author{Kind: AuthorAgent, ID: "peer", Name: "Peer"}
	opts := []CallOption{MessageID("old"), From(author), When(AfterTurn), MessageID("exact"), nil, WithModel("ignored")}
	if got := MessageAttributes(opts...); !reflect.DeepEqual(got, MessageContext{ID: "exact", From: author, Timing: AfterTurn}) {
		t.Fatal(got)
	}
	s := RunSettings{}
	s.SetPolicy(Policy{Sandbox: ReadOnly, Messages: MessagePolicy{MaxTurns: 8, MaxPending: 3, Disabled: true}})
	s.CapMessageTurns(5)
	if s.policy.Sandbox != ReadOnly || s.policy.Messages.MaxTurns != 5 || s.policy.Messages.MaxPending != 3 || !s.policy.Messages.Disabled {
		t.Fatal(s.policy)
	}
	s.CapMessageTurns(10)
	if s.policy.Messages.MaxTurns != 5 {
		t.Fatal("raised a caller budget")
	}
}
