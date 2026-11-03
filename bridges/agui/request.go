package agui

import (
	"context"
	"errors"
	"net/http"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/internal/bridgekey"
)

// RequestOptions controls shared HTTP request preparation.
type RequestOptions struct {
	// Input controls prompt and history policy.
	Input InputOptions
	// ResolveThreadKey authorizes and maps the decoded request.
	ResolveThreadKey func(*http.Request, *RunAgentInput) (string, error)
	// OnAGUIRequest observes input before execution preparation.
	OnAGUIRequest func(*http.Request, *RunAgentInput) error
	// VerifyHistory attests history compatibility and checkpoint usability.
	// It receives the actual key for a fixed Thread, which cannot be rebound.
	VerifyHistory func(context.Context, *RunAgentInput, adaptor.Runner, string) (string, bool, error)
}

// PreparedRequest contains validated execution inputs and public identity.
type PreparedRequest struct {
	// Input is the validated request with public wire identity.
	Input *RunAgentInput
	// ThreadKey is independent of public wire identity.
	ThreadKey string
	// Target is the selected Agent or Thread.
	Target adaptor.Runner
	// Prompt is the resolved executable input.
	Prompt string
	// Diagnostics contains input warnings for projection.
	Diagnostics []Diagnostic
}

// PrepareHTTPRequest decodes, resolves and prepares one AG-UI invocation.
// A public thread identity selects the Agent's Thread (v2 Agents always own a
// store); accepting prior history without a usable checkpoint still requires
// HistoryFallback or a host verifier. Fixed Thread runners remain host-bound. Execution-policy errors are rejected before callbacks or store I/O.
// Hooks can return InputError to choose an HTTP status; other host/storage
// failures return a wrapping InputError with status 500 and code agui.request_failed.
func PrepareHTTPRequest(r *http.Request, runner adaptor.Runner, opts RequestOptions) (*PreparedRequest, error) {
	input, err := DecodeHTTPRequest(r)
	if err != nil {
		return nil, err
	}
	if _, err := executableText(input); err != nil {
		return nil, err
	}
	if input.RunID == "" {
		input.RunID = aguievents.GenerateRunID()
	}
	request := &PreparedRequest{Input: input}
	if input.ThreadID != "" {
		request.ThreadKey = bridgekey.Encode("agui", input.ThreadID)
	}
	if opts.ResolveThreadKey != nil {
		request.ThreadKey, err = opts.ResolveThreadKey(r, input)
		if err != nil {
			return nil, requestPreparationError(err)
		}
	}
	if opts.OnAGUIRequest != nil {
		if err := opts.OnAGUIRequest(r, input); err != nil {
			return nil, requestPreparationError(err)
		}
	}
	request.Target = runnerForKey(runner, request.ThreadKey)
	// A fixed Thread's key comes from the host, regardless of wire identity.
	if thread, ok := runner.(*adaptor.Thread); ok {
		request.ThreadKey = thread.Key()
	}
	state := InputState{}
	hasPrior := len(input.Messages) > 1
	var checkpointErr error
	if thread, ok := request.Target.(*adaptor.Thread); ok {
		_, checkpointErr = thread.Checkpoint(r.Context())
		state.CheckpointUsable = checkpointErr == nil
	}
	// The verifier sees every threaded request, including an empty history
	// that could diverge from recorded history. A resolver may bind a legacy
	// request even when its public threadId was absent.
	if request.ThreadKey != "" && opts.VerifyHistory != nil {
		key, usable, err := opts.VerifyHistory(r.Context(), input, request.Target, request.ThreadKey)
		if err != nil {
			return nil, requestPreparationError(err)
		}
		if fixed, ok := runner.(*adaptor.Thread); ok && key != fixed.Key() {
			return nil, &InputError{Code: "agui.history_diverged", Status: http.StatusConflict,
				Message: "a fixed Thread cannot switch keys; supply an Agent to resolve history branches"}
		}
		request.ThreadKey, state.CheckpointUsable = key, usable
		request.Target = runnerForKey(runner, key)
	} else if hasPrior && checkpointErr != nil && !checkpointUnavailable(checkpointErr) {
		return nil, requestPreparationError(checkpointErr)
	}
	// Compatibility is fully checked inside the sole execution pipeline.
	// ResumeOnly prevents that later check from silently dropping history.
	if hasPrior && state.CheckpointUsable && request.ThreadKey != "" {
		if agent, ok := runner.(*adaptor.Agent); ok {
			request.Target = agent.Thread(request.ThreadKey, adaptor.ResumeOnly())
		}
	}
	request.Prompt, request.Diagnostics, err = PrepareInput(input, opts.Input, state)
	if err != nil {
		return nil, err
	}
	// Mint the public wire identity after resolving the execution target. An
	// absent client threadId must never turn into a generated checkpoint key.
	if input.ThreadID == "" {
		input.ThreadID = aguievents.GenerateThreadID()
	}
	return request, nil
}

func runnerForKey(runner adaptor.Runner, key string) adaptor.Runner {
	if key != "" {
		if agent, ok := runner.(*adaptor.Agent); ok {
			return agent.Thread(key)
		}
	}
	return runner
}

// CheckpointUsable classifies public Thread checkpoint availability.
func CheckpointUsable(ctx context.Context, runner adaptor.Runner) (bool, error) {
	thread, ok := runner.(*adaptor.Thread)
	if !ok {
		return false, nil
	}
	_, err := thread.Checkpoint(ctx)
	if err == nil {
		return true, nil
	}
	if checkpointUnavailable(err) {
		return false, nil
	}
	return false, err
}

func checkpointUnavailable(err error) bool {
	for _, missing := range []error{
		adaptor.ErrThreadNotFound,
		adaptor.ErrThreadCheckpointMissing,
		adaptor.ErrThreadIncompatible,
	} {
		if errors.Is(err, missing) {
			return true
		}
	}
	return false
}

// Hooks can return InputError to choose a transport status. Other preparation
// failures are host/storage failures, not malformed client input.
func requestPreparationError(err error) error {
	var input *InputError
	if errors.As(err, &input) {
		return err
	}
	return errors.Join(&InputError{
		Code: "agui.request_failed", Message: err.Error(), Status: http.StatusInternalServerError,
	}, err)
}
