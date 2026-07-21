package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

type demoSummary struct {
	AgentCard       map[string]any
	Stream          streamSummary
	Poll            map[string]any
	AssistantOutput string
}

type streamSummary struct {
	TaskID             string   `json:"task_id"`
	ContextID          string   `json:"context_id"`
	States             []string `json:"states"`
	ArtifactChunks     int      `json:"artifact_chunks"`
	ResultArtifactSeen bool     `json:"result_artifact_seen"`
	TerminalState      string   `json:"terminal_state"`
	TerminalMessage    string   `json:"terminal_message,omitempty"`
	RecoveredState     bool     `json:"recovered_state"`
}

func runClientDemo(ctx context.Context, agentCardURL, contextID, prompt, expect string) (demoSummary, error) {
	client := clienta2a.New(clienta2a.Options{
		AgentCardURL:        agentCardURL,
		PreferredTransports: []clienta2a.TransportProtocol{clienta2a.TransportJSONRPC},
	})
	defer client.Close()

	card, err := client.AgentCard(ctx)
	if err != nil {
		return demoSummary{}, err
	}
	if !card.Capabilities.Streaming {
		return demoSummary{}, fmt.Errorf("expected demo agent card to advertise streaming")
	}

	stream, err := client.SendStream(ctx, clienta2a.SendRequest{
		ContextID:           contextID,
		AcceptedOutputModes: card.DefaultOutputModes,
		Message: clienta2a.Message{
			Role: "user",
			Parts: []clienta2a.Part{{
				Kind:      clienta2a.PartText,
				Text:      prompt,
				MediaType: "text/plain",
			}},
		},
		Metadata: map[string]any{
			"example": "a2a-local",
		},
	})
	if err != nil {
		return demoSummary{}, err
	}
	defer stream.Close()

	streamOut, assistantOutput, err := consumeStream(stream)
	if err != nil {
		return demoSummary{}, err
	}
	if streamOut.TaskID == "" {
		return demoSummary{}, fmt.Errorf("A2A stream did not return a task id")
	}
	if streamOut.TerminalState != string(clienta2a.TaskStateCompleted) {
		return demoSummary{}, fmt.Errorf("A2A task ended in %s: %s", streamOut.TerminalState, defaultString(streamOut.TerminalMessage, "no terminal message"))
	}

	historyLength := 4
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{
		TaskID:        streamOut.TaskID,
		HistoryLength: &historyLength,
	})
	if err != nil {
		return demoSummary{}, err
	}
	if task.Status.State != clienta2a.TaskStateCompleted {
		return demoSummary{}, fmt.Errorf("GetTask returned state %s: %s", task.Status.State, statusMessage(task.Status))
	}
	if strings.TrimSpace(assistantOutput) == "" && task.Status.Message != nil {
		assistantOutput = partsText(task.Status.Message.Parts)
	}
	if expected := strings.TrimSpace(expect); expected != "" && !strings.Contains(assistantOutput, expected) {
		return demoSummary{}, fmt.Errorf("A2A task completed but assistant output did not contain %q: %q", expected, preview(assistantOutput, 240))
	}

	return demoSummary{
		AgentCard: map[string]any{
			"name":                 card.Name,
			"url":                  card.URL,
			"fingerprint":          card.Fingerprint,
			"streaming":            card.Capabilities.Streaming,
			"default_output_modes": card.DefaultOutputModes,
			"skills":               len(card.Skills),
		},
		Stream:          streamOut,
		AssistantOutput: assistantOutput,
		Poll: map[string]any{
			"task_id":              task.ID,
			"context_id":           task.ContextID,
			"state":                task.Status.State,
			"history_messages":     len(task.Messages),
			"artifacts":            len(task.Artifacts),
			"result_artifact_seen": taskHasArtifact(task, bridgea2a.ArtifactAgentAdaptorResult),
		},
	}, nil
}

func consumeStream(stream *clienta2a.Stream) (streamSummary, string, error) {
	var summary streamSummary
	var output strings.Builder

	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, "", err
		}

		if event.TaskID != "" {
			summary.TaskID = event.TaskID
		}
		if event.ContextID != "" {
			summary.ContextID = event.ContextID
		}

		switch event.Kind {
		case clienta2a.EventTask:
			if event.Task != nil {
				rememberState(&summary, event.Task.Status.State)
			}
		case clienta2a.EventStatus:
			if event.Status != nil {
				rememberState(&summary, event.Status.State)
			}
		case clienta2a.EventArtifact:
			summary.ArtifactChunks++
			if event.Artifact == nil {
				continue
			}
			switch event.Artifact.Name {
			case bridgea2a.ArtifactAssistantOutput:
				if !event.LastChunk {
					output.WriteString(partsText(event.Artifact.Parts))
				}
			case bridgea2a.ArtifactAgentAdaptorResult:
				summary.ResultArtifactSeen = true
			}
		case clienta2a.EventTerminal:
			applyTerminal(&summary, &output, event)
			return summary, output.String(), nil
		}
	}

	return summary, output.String(), nil
}

func applyTerminal(summary *streamSummary, output *strings.Builder, event clienta2a.Event) {
	summary.RecoveredState = event.RecoveredState
	if event.Task != nil {
		rememberState(summary, event.Task.Status.State)
		summary.TerminalState = string(event.Task.Status.State)
		if output.Len() == 0 && event.Task.Status.Message != nil {
			output.WriteString(partsText(event.Task.Status.Message.Parts))
		}
		summary.TerminalMessage = statusMessage(event.Task.Status)
		return
	}
	if event.Status != nil {
		rememberState(summary, event.Status.State)
		summary.TerminalState = string(event.Status.State)
		if output.Len() == 0 && event.Status.Message != nil {
			output.WriteString(partsText(event.Status.Message.Parts))
		}
		summary.TerminalMessage = statusMessage(*event.Status)
		return
	}
	if event.Message != nil {
		summary.TerminalState = string(clienta2a.TaskStateCompleted)
		if output.Len() == 0 {
			output.WriteString(partsText(event.Message.Parts))
		}
		summary.TerminalMessage = partsText(event.Message.Parts)
	}
}

func rememberState(summary *streamSummary, state clienta2a.TaskState) {
	if state == "" {
		return
	}
	value := string(state)
	for _, existing := range summary.States {
		if existing == value {
			return
		}
	}
	summary.States = append(summary.States, value)
}

func taskHasArtifact(task clienta2a.Task, name string) bool {
	for _, artifact := range task.Artifacts {
		if artifact.Name == name {
			return true
		}
	}
	return false
}

func partsText(parts []clienta2a.Part) string {
	var out strings.Builder
	for _, part := range parts {
		if part.Kind == clienta2a.PartText {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

func statusMessage(status clienta2a.TaskStatus) string {
	if status.Message == nil {
		return ""
	}
	return partsText(status.Message.Parts)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func preview(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
