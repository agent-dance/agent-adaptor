package adaptor

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestResultFromResponseDeepCopiesAuditLayers(t *testing.T) {
	terminalJSON := json.RawMessage(`{"status":"completed","nested":{"value":1}}`)
	resp := driver.Response{
		Output:   "assistant text",
		Summary:  "bounded summary",
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

	// Accessors return fresh deep copies too.
	raw.Terminal.JSON[0] = '['
	transcript[0].Metadata["kind"] = "changed"
	services := result.Services()
	services[0].Metadata["observed"] = "changed"
	if string(result.Raw().Terminal.JSON) != `{"status":"completed","nested":{"value":1}}` || result.Transcript()[0].Metadata["kind"] != "final" || result.Services()[0].Metadata["observed"] != "yes" {
		t.Fatal("audit accessor mutation escaped into Result")
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
