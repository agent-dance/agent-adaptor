package team

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type activityDriver struct{ *fakeDriver }

func (fake *activityDriver) Descriptor() driver.Descriptor {
	descriptor := fake.fakeDriver.Descriptor()
	descriptor.StructuredOutput = driver.StructuredOutputCapability{
		JSONSchemaNative: true, WorksWithRun: true,
		WorksWithStreaming: true, WorksWithHITL: true,
	}
	return descriptor
}

func TestTeamActivityPreservesLiveTextAndPlanArtifact(t *testing.T) {
	ctx := testContext(t)
	const plan = `{"filename":"PLAN.md","media_type":"text/markdown","summary":"Implement the change","content":"# Plan\n1. Test the change."}`
	const finalText = "The implementation plan is ready."
	var team *Team
	var calls atomic.Int32
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			if _, err := call(team, req, "delegate", map[string]any{"member": "plan", "objective": "Create an implementation plan"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("coordinated", 1), nil
	}})
	worker := adaptor.New(&activityDriver{&fakeDriver{run: func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		for _, payload := range []driver.StreamPayload{
			{Kind: driver.StreamRunStarted},
			{Kind: driver.StreamTextStart, MessageID: "plan-text"},
			{Kind: driver.StreamTextContent, MessageID: "plan-text", Delta: "Inspect the API. "},
			{Kind: driver.StreamTextContent, MessageID: "plan-text", Delta: "Write the plan."},
			{Kind: driver.StreamTextEnd, MessageID: "plan-text"},
			{Kind: driver.StreamRunFinished},
		} {
			if err := sink.EmitStream(payload); err != nil {
				return driver.Response{}, err
			}
		}
		response := answer(finalText, 2)
		response.StructuredOutput = &driver.StructuredOutput{
			Format: driver.OutputFormatJSONSchema, Source: driver.StructuredOutputSourceNative,
			RawJSON: json.RawMessage(plan), Valid: true,
		}
		response.Model, response.Provider = "planner-model", "planner-provider"
		response.Metadata = map[string]string{"secret": "private-runtime-token"}
		response.RawStreams = &driver.RawStreams{Stdout: "private-provider-stdout", Stderr: "private-provider-stderr"}
		return response, nil
	}}})
	team = fixture(t, Config{Leader: leader, Members: []Member{{Key: "plan", Name: "Codex Planner", Runner: worker,
		Output: map[string]any{"type": "object", "required": []string{"filename", "media_type", "summary", "content"},
			"properties": map[string]any{"filename": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}},
	}}})
	events, _, err := collect(t, team.Send(ctx, "plan the change"))
	if err != nil {
		t.Fatal(err)
	}
	translator := agui.NewEventTranslator()
	var activity map[string]any
	live, terminal := false, false
	for _, event := range events {
		if update, ok := event.(adaptor.SubagentUpdate); ok && update.Kind == adaptor.SubagentFinished {
			encoded, err := json.Marshal(update.Data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-") {
				t.Fatalf("worker activity exposed private audit data: %s", encoded)
			}
		}
		for _, wire := range translator.Translate(event) {
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var projection struct {
				Type    string         `json:"type"`
				Content map[string]any `json:"content"`
				Patch   []struct {
					Path  string `json:"path"`
					Value any    `json:"value"`
				} `json:"patch"`
			}
			if err := json.Unmarshal(encoded, &projection); err != nil {
				t.Fatal(err)
			}
			switch projection.Type {
			case "ACTIVITY_SNAPSHOT":
				activity = projection.Content
				if activity["agentName"] != "Codex Planner" || activity["description"] != "Create an implementation plan" || activity["protocol"] != "local" {
					t.Fatalf("worker snapshot lost roster/objective details: %+v", activity)
				}
			case "ACTIVITY_DELTA":
				for _, patch := range projection.Patch {
					activity[strings.TrimPrefix(patch.Path, "/")] = patch.Value
				}
				live = live || activity["text"] == "Inspect the API. Write the plan."
				terminal = terminal || activity["status"] == "completed"
			}
		}
	}
	if !live || !terminal || activity["text"] != finalText {
		t.Fatalf("worker Activity lost text: live=%v terminal=%v activity=%+v", live, terminal, activity)
	}
	result, ok := activity["result"].(map[string]any)
	if !ok || result["model"] != "planner-model" || result["provider"] != "planner-provider" {
		t.Fatalf("worker terminal result=%+v", activity["result"])
	}
	artifact, ok := result["output"].(map[string]any)
	if !ok || artifact["filename"] != "PLAN.md" || artifact["media_type"] != "text/markdown" || artifact["content"] != "# Plan\n1. Test the change." {
		t.Fatalf("worker Activity lost structured plan attachment: %+v", result)
	}
}
