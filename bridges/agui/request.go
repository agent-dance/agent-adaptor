package agui

import (
	"context"
	"errors"
	"net/http"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
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
func PrepareHTTPRequest(r *http.Request, runner adaptor.Runner, opts RequestOptions) (*PreparedRequest, error) {
	input, err := DecodeHTTPRequest(r)
	if err != nil {
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
			return nil, err
		}
	}
	if opts.OnAGUIRequest != nil {
		if err := opts.OnAGUIRequest(r, input); err != nil {
			return nil, err
		}
	}
	request.Target = runnerForKey(runner, request.ThreadKey)
	state := InputState{}
	// A verifier sees every threaded request, because an empty prior history
	// can itself diverge from recorded history. Checkpoint reads and ResumeOnly
	// apply only when the client supplied prior messages: a lone user turn keeps
	// the Thread's continue-or-start semantics.
	hasPrior := len(input.Messages) > 1
	if input.ThreadID != "" && (opts.VerifyHistory != nil || hasPrior) {
		if opts.VerifyHistory != nil {
			request.ThreadKey, state.CheckpointUsable, err = opts.VerifyHistory(
				r.Context(), input, request.Target, request.ThreadKey,
			)
			request.Target = runnerForKey(runner, request.ThreadKey)
		} else if thread, ok := request.Target.(*adaptor.Thread); ok {
			state.CheckpointUsable, err = CheckpointUsable(r.Context(), thread)
		}
		if err != nil {
			return nil, err
		}
		// Compatibility is fully checked inside the sole execution pipeline.
		// ResumeOnly prevents that later check from silently dropping history.
		if hasPrior && state.CheckpointUsable && request.ThreadKey != "" {
			if agent, ok := runner.(*adaptor.Agent); ok {
				request.Target = agent.Thread(request.ThreadKey, adaptor.ResumeOnly())
			}
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
	for _, missing := range []error{
		adaptor.ErrThreadNotFound,
		adaptor.ErrThreadCheckpointMissing,
		adaptor.ErrThreadIncompatible,
		adaptor.ErrThreadStoreRequired,
	} {
		if errors.Is(err, missing) {
			return false, nil
		}
	}
	return false, err
}
