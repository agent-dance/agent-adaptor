package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
)

var errControlSinkRequired = errors.New("codebuddy control transport requires a decision-capable sink")

func (adapter) runControl(ctx context.Context, cfg agentadaptor.CodeBuddyConfig, command string, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink, prep runPrep) (agentadaptor.DriverRunResult, error) {
	if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
		return agentadaptor.DriverRunResult{}, &agentadaptor.StructuredOutputUnsupportedError{
			Adapter: DriverType,
			Mode:    req.OutputSchema.Mode,
			Reason:  "CodeBuddy native structured output is not supported with control HITL",
		}
	}
	decisionSink, ok := sink.(agentadaptor.DecisionCapableSink)
	if !ok {
		return agentadaptor.DriverRunResult{}, errControlSinkRequired
	}

	stdin := clihelper.NewStdinController()
	p := newParser(sink)
	p.enableControl(ctx, decisionSink, stdin, req.RunID, req.Policy.HumanDecision, prep.prompt)
	p.control.configDir = resolveConfigDir(cfg.Env)
	if req.Streaming {
		p.enableStreaming(req.RunID)
	}

	go func() {
		_ = stdin.Write(mustEncodeControlInitialize())
	}()

	result, err := clihelper.Run(ctx, clihelper.CommandRequest{
		Command: command,
		Args:    buildExecArgs(cfg, req, agentadaptor.CodeBuddyPermissionUnset, true),
		CWD:     prep.effectiveCWD,
		Env:     append(prep.env, agentadaptor.EnvBinding{Name: "CODEBUDDY_CODE_ENTRYPOINT", Value: "sdk-py"}),
		Observe: p.onChunk,
		Stdin:   stdin,
	}, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	p.finalize()

	raw := agentadaptor.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr}
	failure := p.pendingFailure
	if failure == nil && strings.TrimSpace(p.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{Code: agentadaptor.FailureAgentError, Message: p.errorMessage}
	}
	checkpoint := p.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                prep.effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: req.ProfilePayload.Fingerprint,
		}
	}
	return agentadaptor.DriverRunResult{
		Output:          p.buildOutput(),
		RawStreams:      &raw,
		Transcript:      p.transcript,
		ExitCode:        result.ExitCode,
		Signal:          result.Signal,
		TimedOut:        result.TimedOut,
		Usage:           p.usage,
		Checkpoint:      checkpoint,
		Metadata:        p.outputMetadata(),
		Provider:        "codebuddy",
		Model:           prep.reportedModel,
		Summary:         p.finalSummary(),
		Result:          p.resultFinal,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:         failure,
	}, nil
}

func mustEncodeControlInitialize() []byte {
	frame, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": "agent-adaptor-initialize",
		"request": map[string]any{
			"subtype":      "initialize",
			"hasPrompt":    true,
			"capabilities": map[string]any{"askUserQuestion": true},
		},
	})
	if err != nil {
		panic(err)
	}
	return append(frame, '\n')
}
