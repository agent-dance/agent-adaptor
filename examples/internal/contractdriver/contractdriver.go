// Package contractdriver provides a deterministic examples-only adapter for
// demonstrating host contracts that should run without a provider account.
package contractdriver

import (
	"context"
	"errors"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type Config struct {
	Output       string
	DecisionKind agentadaptor.HumanDecisionKind
	Fail         bool
}

func New(cfg Config, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	return agentadaptor.BindTyped(adapter{}, cfg, opts...)
}

type adapter struct{}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        "example-contract",
		DisplayName: "Example Contract Driver",
		Runtime:     agentadaptor.RuntimeCapability{ReportsServices: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Permission: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			Question:   agentadaptor.QuestionSupport{Ask: true, AutoReject: true},
		},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	if _, ok := cfg.(Config); !ok {
		return errors.New("example contract driver requires contractdriver.Config")
	}
	return nil
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := req.Config.(Config)
	if cfg.DecisionKind != "" {
		decisions, ok := sink.(agentadaptor.DecisionCapableSink)
		if !ok {
			return agentadaptor.DriverRunResult{}, errors.New("run sink does not support decisions")
		}
		if _, err := decisions.RequestDecision(ctx, decisionRequest(cfg.DecisionKind)); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}

	output := cfg.Output
	if output == "" {
		output = "contract completed"
	}
	item := agentadaptor.TranscriptItem{Kind: agentadaptor.TranscriptAssistant, Text: output}
	if err := sink.Emit(agentadaptor.RunEvent{Type: agentadaptor.RunEventItem, Item: &item}); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	result := agentadaptor.DriverRunResult{
		Output:          output,
		RawStreams:      &agentadaptor.RawStreams{Stdout: output + "\n"},
		Transcript:      []agentadaptor.TranscriptItem{item},
		Summary:         "contract example",
		Result:          map[string]any{"status": "completed"},
		ExitCode:        0,
		Provider:        "examples",
		RuntimeServices: runtimeReports(req.Runtime.Ensured),
	}
	if cfg.Fail {
		result.Failure = &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureAgentError,
			Message: "requested example failure",
		}
	}
	return result, nil
}

func decisionRequest(kind agentadaptor.HumanDecisionKind) agentadaptor.DecisionRequest {
	payload := map[string]any{}
	switch kind {
	case agentadaptor.HumanDecisionPermission:
		payload["tool"] = "shell"
		payload["args"] = map[string]any{"command": "go test ./..."}
	case agentadaptor.HumanDecisionPlanReview:
		payload["plan"] = "Run the test suite, inspect the diff, then publish the result."
	case agentadaptor.HumanDecisionQuestion:
		payload["schema"] = map[string]any{"type": "object"}
	}
	return agentadaptor.DecisionRequest{
		Kind:    kind,
		Source:  "example-contract",
		Prompt:  "Continue with the example operation?",
		Payload: payload,
	}
}

func runtimeReports(refs []agentadaptor.RuntimeServiceRef) []agentadaptor.RuntimeServiceReport {
	reports := make([]agentadaptor.RuntimeServiceReport, 0, len(refs))
	for _, ref := range refs {
		reports = append(reports, agentadaptor.RuntimeServiceReport{
			ID: ref.ID, Name: ref.Name, URL: ref.URL, Status: ref.Status,
			Lifecycle: ref.Lifecycle, ReuseKey: ref.ReuseKey, Command: ref.Command,
			CWD: ref.CWD, Port: ref.Port, OwnerAgentID: ref.OwnerAgentID,
			Health: ref.Health, Metadata: ref.Metadata,
		})
	}
	return reports
}
