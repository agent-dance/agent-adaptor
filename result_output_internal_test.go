package adaptor

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestResultFromResponseDeepCopiesAuditLayers(t *testing.T) {
	terminalJSON := json.RawMessage(`{"status":"completed","nested":{"value":1}}`)
	usage := &driver.Usage{InputTokens: 3, OutputTokens: 5}
	resp := driver.Response{
		Output:   "assistant text",
		Summary:  "bounded summary",
		Usage:    usage,
		Metadata: map[string]string{"source": "driver"},
		RawStreams: &driver.RawStreams{
			Stdout: "full stdout",
			Stderr: "full stderr",
			Terminal: &driver.TerminalPayload{
				Event: "turn/completed",
				JSON:  terminalJSON,
			},
		},
		Transcript: []driver.TranscriptItem{{
			Kind:     driver.TranscriptResult,
			Metadata: map[string]string{"kind": "final"},
			Data:     map[string]any{"nested": map[string]any{"value": float64(1)}},
		}},
		StructuredOutput: &driver.StructuredOutput{
			RawJSON:          json.RawMessage(`{"answer":42}`),
			Value:            map[string]any{"answer": float64(42)},
			Valid:            true,
			ValidationErrors: []string{"copied"},
		},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "svc", Metadata: map[string]string{"observed": "yes"}}},
	}

	result := resultFromResponse("run-1", resp)
	resp.Metadata["source"] = "mutated"
	usage.InputTokens = 99
	resp.RawStreams.Terminal.JSON[0] = '['
	resp.Transcript[0].Metadata["kind"] = "mutated"
	resp.Transcript[0].Data["nested"].(map[string]any)["value"] = float64(99)
	resp.StructuredOutput.RawJSON[0] = '['
	resp.RuntimeServices[0].Metadata["observed"] = "mutated"

	raw := result.Raw()
	if raw.Stdout != "full stdout" || raw.Stderr != "full stderr" || raw.Terminal == nil || string(raw.Terminal.JSON) != `{"status":"completed","nested":{"value":1}}` {
		t.Fatalf("Raw = %#v", raw)
	}
	transcript := result.Transcript()
	if transcript[0].Metadata["kind"] != "final" || transcript[0].Data["nested"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("Transcript = %#v", transcript)
	}
	if result.Services()[0].Metadata["observed"] != "yes" {
		t.Fatalf("Services = %#v", result.Services())
	}
	if result.Usage == nil || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 5 || result.Usage == usage {
		t.Fatalf("Usage = %#v; want independent observed usage copy", result.Usage)
	}

	// Accessors return fresh deep copies too.
	raw.Terminal.JSON[0] = '['
	transcript[0].Metadata["kind"] = "changed"
	services := result.Services()
	services[0].Metadata["observed"] = "changed"
	if string(result.Raw().Terminal.JSON) != `{"status":"completed","nested":{"value":1}}` || result.Transcript()[0].Metadata["kind"] != "final" || result.Services()[0].Metadata["observed"] != "yes" {
		t.Fatal("audit accessor mutation escaped into Result")
	}
}

func TestResultFromResponsePreservesUsageObservation(t *testing.T) {
	unobserved := resultFromResponse("unobserved", driver.Response{})
	if unobserved.Usage != nil {
		t.Fatalf("unobserved Usage = %#v, want nil", unobserved.Usage)
	}

	providerZero := &driver.Usage{}
	observedZero := resultFromResponse("observed-zero", driver.Response{Usage: providerZero})
	if observedZero.Usage == nil || *observedZero.Usage != (Usage{}) {
		t.Fatalf("observed zero Usage = %#v, want non-nil zero value", observedZero.Usage)
	}
	providerZero.InputTokens = 1
	if observedZero.Usage.InputTokens != 0 {
		t.Fatalf("provider mutation escaped into Result.Usage: %#v", observedZero.Usage)
	}
}

func TestResultSummaryNeverFallsBackToText(t *testing.T) {
	result := resultFromResponse("run-summary", driver.Response{Output: "an arbitrarily long assistant answer"})
	if result.Text == "" || result.Summary != "" {
		t.Fatalf("Text/Summary = %q/%q", result.Text, result.Summary)
	}
}

func TestMergeServiceReportsByStableID(t *testing.T) {
	ensured := []ServiceReport{
		{ID: "shared", Name: "sdk name", URL: "http://sdk", Status: driver.RuntimeServiceRunning, Metadata: map[string]string{"sdk": "yes", "winner": "sdk"}},
		{ID: "sdk-only", Name: "only sdk"},
	}
	observed := []ServiceReport{
		{ID: "shared", Name: "driver name", Health: driver.RuntimeHealthUnhealthy, Metadata: map[string]string{"driver": "yes", "winner": "driver"}},
		{ID: "driver-only", Name: "only driver"},
		{Name: "anonymous one"},
		{Name: "anonymous two"},
	}

	got := mergeServiceReports(ensured, observed)
	if len(got) != 5 {
		t.Fatalf("merged len = %d: %#v", len(got), got)
	}
	shared := got[0]
	if shared.ID != "shared" || shared.Name != "driver name" || shared.URL != "http://sdk" || shared.Status != driver.RuntimeServiceRunning || shared.Health != driver.RuntimeHealthUnhealthy {
		t.Fatalf("shared = %#v", shared)
	}
	if !reflect.DeepEqual(shared.Metadata, map[string]string{"sdk": "yes", "driver": "yes", "winner": "driver"}) {
		t.Fatalf("shared metadata = %#v", shared.Metadata)
	}
	if got[1].ID != "driver-only" || got[2].ID != "" || got[3].ID != "" || got[4].ID != "sdk-only" {
		t.Fatalf("stable merge order/anonymous preservation = %#v", got)
	}

	got[0].Metadata["sdk"] = "mutated"
	if ensured[0].Metadata["sdk"] != "yes" || observed[0].Metadata["driver"] != "yes" {
		t.Fatal("merge aliased an input metadata map")
	}
}

func TestResultWithSubagentsCopiesSnapshotAndPreservesAccounting(t *testing.T) {
	original := resultFromResponse("run", driver.Response{
		Output: "leader answer", Summary: "leader summary", Model: "model", Provider: "provider",
		Usage: &driver.Usage{InputTokens: 8}, Metadata: map[string]string{"key": "value"},
		RawStreams: &driver.RawStreams{Stdout: "raw"},
		Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "leader answer"}},
	})
	original.messages = []MessageOutcome{{ID: "host", Text: "objective", State: MessageDelivered}}
	original.subagents = []SubagentResult{{Task: "old", State: "running"}}
	snapshot := []SubagentResult{{Task: "task", Member: "worker", State: "cancelled", Usage: &Usage{InputTokens: 7}, Unread: []string{"note"}}}
	result := original.WithSubagents(snapshot)
	if result == original || result.Text != original.Text || result.RunID != "run" || result.Usage.InputTokens != 8 || !reflect.DeepEqual(result.Raw(), original.Raw()) || !reflect.DeepEqual(result.Transcript(), original.Transcript()) || !reflect.DeepEqual(result.Messages(), original.Messages()) || !reflect.DeepEqual(result.Subagents(), snapshot) {
		t.Fatalf("snapshot replacement changed result accounting or audit: %+v", result)
	}
	if got := original.Subagents(); len(got) != 1 || got[0].Task != "old" {
		t.Fatalf("source summaries mutated: %+v", got)
	}
	snapshot[0].Unread[0] = "mutated"
	snapshot[0].Usage.InputTokens = 99
	result.Usage.InputTokens = 99
	result.Metadata["key"] = "mutated"
	if got := result.Subagents()[0]; got.Unread[0] != "note" || got.Usage.InputTokens != 7 {
		t.Fatalf("snapshot retained caller-owned data: %+v", got)
	}
	if original.Usage.InputTokens != 8 || original.Metadata["key"] != "value" {
		t.Fatal("result copy retained source-owned accounting or metadata")
	}
	var empty *Result
	if got := empty.WithSubagents(nil); got == nil || len(got.Subagents()) != 0 || got.Usage != nil {
		t.Fatalf("nil result copy=%+v", got)
	}
}
