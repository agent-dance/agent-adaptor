package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Generic extension dialogs are Questions. Permission and PlanReview require
// the explicit tool signals in interaction.go, never guesses from UI text.
func (p *rpcProcess) extensionUI(ctx context.Context, invocation driver.Request, sink driver.EventSink, raw []byte) error {
	var ui struct {
		ID, Method, Title, Message, Placeholder, Prefill string
		Options                                          []string
	}
	if err := json.Unmarshal(raw, &ui); err != nil || ui.ID == "" || ui.Method == "" {
		return errors.New("pi: invalid extension UI request")
	}
	switch ui.Method {
	case "notify", "setStatus", "setWidget", "setTitle", "set_editor_text":
		return nil // The complete provider record is already in the transcript.
	case "select", "confirm", "input", "editor":
	default:
		return fmt.Errorf("pi: unsupported extension UI method %q", ui.Method)
	}
	decisions, ok := sink.(driver.DecisionCapableSink)
	if !ok {
		return errors.New("pi: extension dialog requires a decision-capable sink")
	}
	if p.uiRequests[ui.ID] {
		return errors.New("pi: duplicate extension dialog identifier")
	}
	if p.uiRequests == nil {
		p.uiRequests = map[string]bool{}
	}
	p.uiRequests[ui.ID] = true
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)
	request := driver.DecisionRequest{RunID: invocation.RunID, Kind: driver.HumanDecisionQuestion, Source: "pi.extension_ui." + ui.Method, Prompt: ui.Title, Payload: payload}
	if ui.Message != "" {
		request.Prompt += "\n\n" + ui.Message
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return errors.New("pi: extension dialog has no prompt")
	}
	if ui.Method == "confirm" {
		ui.Options = []string{"yes", "no"}
	}
	for _, option := range ui.Options {
		request.Choices = append(request.Choices, driver.DecisionChoice{Key: option, Label: option})
	}
	answer, err := decisions.RequestDecision(ctx, request)
	if err != nil {
		return err
	}
	reply := map[string]any{"type": "extension_ui_response", "id": ui.ID}
	if answer.Result != driver.DecisionAnswered {
		reply["cancelled"] = true
	} else {
		value := answer.Text
		if answer.Choice != "" {
			value = answer.Choice
		}
		if ui.Method == "select" || ui.Method == "confirm" {
			valid := false
			for _, option := range ui.Options {
				if option == value {
					valid = true
					break
				}
			}
			if !valid {
				return errors.New("pi: extension dialog answer is not an offered choice")
			}
		}
		if ui.Method == "confirm" {
			reply["confirmed"] = value == "yes"
		} else {
			reply["value"] = value
		}
	}
	return p.write(ctx, reply)
}
