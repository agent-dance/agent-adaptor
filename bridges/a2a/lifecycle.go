package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func requiredLifecycleKind(kind string) bool {
	switch kind {
	case "message.update", "turn.end", "done":
		return true
	default:
		return false
	}
}

// safeResult keeps the final answer and bounded summary public. Diagnostics
// use the same explicit opt-ins as the terminal artifact.
func safeResult(result *adaptor.Result, exposure ExposurePolicy) map[string]any {
	if result == nil {
		return nil
	}
	out := map[string]any{"Text": result.Text, "Summary": result.Summary, "Turns": result.Turns}
	if exposure.Diagnostics.IncludeUsage {
		out["Usage"] = result.Usage
	}
	if exposure.Diagnostics.IncludeMetadata {
		out["Metadata"] = result.Metadata
		out["RunID"] = result.RunID
		out["Model"] = result.Model
		out["Provider"] = result.Provider
	}
	return out
}

func lifecyclePayload(result *adaptor.Result, err error, exposure ExposurePolicy) map[string]any {
	out := map[string]any{"Result": safeResult(result, exposure)}
	if err != nil {
		out["Error"] = err.Error()
		if errors.Is(err, context.Canceled) {
			out["ErrorKind"] = "cancelled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			out["ErrorKind"] = "timeout"
		}
		var re *adaptor.RunError
		if errors.As(err, &re) {
			out["Reason"] = re.Reason
			out["Partial"] = safeResult(re.Result, exposure)
		}
	}
	return out
}

// terminal attaches Done to the one final protocol status, after artifacts.
func (t *streamTranslator) terminal(status *a2aproto.TaskStatusUpdateEvent, result *adaptor.Result, err error) *a2aproto.TaskStatusUpdateEvent {
	source := t.done
	if source == nil {
		source = adaptor.Done{Result: result, Err: err}
	}
	events := t.emit(source, AdapterStreamEventV1{Kind: "done", Result: lifecyclePayload(result, err, t.exposure)})
	msg := events[0].(*a2aproto.TaskStatusUpdateEvent).Status.Message
	if status.Status.Message == nil {
		status.Status.Message = msg
	} else {
		status.Status.Message.Parts = append(status.Status.Message.Parts, msg.Parts...)
	}
	return status
}

func decodeLifecycle(event AdapterStreamEventV1) (adaptor.Event, error) {
	if event.Result == nil {
		return nil, fmt.Errorf("%s lifecycle payload is required", event.Kind)
	}
	raw, err := json.Marshal(event.Result)
	if err != nil {
		return nil, err
	}
	if event.Kind == "message.update" {
		var update adaptor.MessageUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		if update.ID == "" || (update.State != adaptor.MessageAccepted && update.State != adaptor.MessageDelivered && update.State != adaptor.MessageFailed) {
			return nil, errors.New("invalid message.update identity or state")
		}
		return update, nil
	}
	var wire struct {
		Result     *adaptor.Result
		Partial    *adaptor.Result
		Error      string
		ErrorKind  string
		Reason     adaptor.FailureReason
		Turn       int
		MessageIDs []string
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	var failure error
	if wire.Error != "" {
		failure = errors.New(wire.Error)
		switch wire.ErrorKind {
		case "cancelled":
			failure = fmt.Errorf("%s: %w", wire.Error, context.Canceled)
		case "timeout":
			failure = fmt.Errorf("%s: %w", wire.Error, context.DeadlineExceeded)
		}
		if wire.Reason != "" {
			runErr := &adaptor.RunError{Message: wire.Error, Reason: wire.Reason, Result: wire.Partial}
			if wire.ErrorKind != "" {
				failure = errors.Join(runErr, failure)
			} else {
				failure = runErr
			}
		}
	}
	if event.Kind == "turn.end" {
		if wire.Turn < 1 {
			return nil, errors.New("turn.end turn must be positive")
		}
		return adaptor.TurnEnd{Turn: wire.Turn, Result: wire.Result, Err: failure, MessageIDs: wire.MessageIDs}, nil
	}
	if wire.Result == nil && failure == nil {
		return nil, errors.New("done requires a result or error")
	}
	return adaptor.Done{Result: wire.Result, Err: failure}, nil
}
