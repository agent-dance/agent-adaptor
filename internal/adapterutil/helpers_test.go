package adapterutil

import (
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestTranscriptItemForLineClassifiesPlainTextStreams(t *testing.T) {
	stdout := transcriptItemForLine(agentadaptor.RunEventStdout, "hello")
	if stdout.Type != agentadaptor.TranscriptOutput || stdout.Text != "hello" {
		t.Fatalf("unexpected stdout transcript item: %#v", stdout)
	}

	stderr := transcriptItemForLine(agentadaptor.RunEventStderr, "problem")
	if stderr.Type != agentadaptor.TranscriptDiagnostic || stderr.Text != "problem" {
		t.Fatalf("unexpected stderr transcript item: %#v", stderr)
	}
}

func TestTranscriptItemForLineClassifiesStructuredJSON(t *testing.T) {
	item := transcriptItemForLine(agentadaptor.RunEventStdout, `{"type":"message","text":"hi"}`)
	if item.Type != agentadaptor.TranscriptStructured {
		t.Fatalf("expected structured transcript item, got %#v", item)
	}
	if item.Metadata["stream"] != string(agentadaptor.RunEventStdout) {
		t.Fatalf("unexpected transcript metadata: %#v", item.Metadata)
	}
	if item.Data == nil || item.Data["payload"] == nil {
		t.Fatalf("expected structured payload, got %#v", item.Data)
	}
}

func TestTranscriptFromOutputAppendsSummaryQuestionAndFailure(t *testing.T) {
	items := TranscriptFromOutput(
		"hello\n",
		"warn\n",
		"done",
		&agentadaptor.RunQuestion{
			Prompt: "continue?",
			Choices: []agentadaptor.RunChoice{
				{Key: "y", Label: "Yes", Description: "continue"},
			},
		},
		&agentadaptor.RunFailure{
			Message: "boom",
			Code:    "fatal",
			Metadata: map[string]string{
				"source": "test",
			},
		},
	)
	if len(items) != 5 {
		t.Fatalf("expected 5 transcript items, got %#v", items)
	}
	if items[2].Type != agentadaptor.TranscriptSummary || items[2].Text != "done" {
		t.Fatalf("unexpected summary item: %#v", items[2])
	}
	if items[3].Type != agentadaptor.TranscriptQuestion || items[3].Data["choices"] == nil {
		t.Fatalf("unexpected question item: %#v", items[3])
	}
	if items[4].Type != agentadaptor.TranscriptFailure || items[4].Metadata["code"] != "fatal" {
		t.Fatalf("unexpected failure item: %#v", items[4])
	}
}

func TestResolvedEnvValuePrefersBindingOverProcessEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "process-value")
	value, source := ResolvedEnvValue([]agentadaptor.EnvBinding{{
		Name:  "OPENAI_API_KEY",
		Value: "binding-value",
	}}, "OPENAI_API_KEY")
	if value != "binding-value" || source != "binding_env" {
		t.Fatalf("unexpected resolved env: %q %q", value, source)
	}
}

func TestResolvedTruthyEnvRecognizesTrueValues(t *testing.T) {
	ok, source := ResolvedTruthyEnv([]agentadaptor.EnvBinding{{
		Name:  "CLAUDE_CODE_USE_BEDROCK",
		Value: "true",
	}}, "CLAUDE_CODE_USE_BEDROCK")
	if !ok || source != "binding_env" {
		t.Fatalf("unexpected truthy env result: %v %q", ok, source)
	}
}
