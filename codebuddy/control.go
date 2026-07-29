package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

var errControlSinkRequired = errors.New("codebuddy control transport requires a decision-capable sink")

func (adapter) runControl(ctx context.Context, cfg Config, command string, req driver.Request, sink driver.EventSink, prep runPrep) (driver.Response, error) {
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		return driver.Response{}, &driver.StructuredOutputUnsupportedError{
			Driver: DriverType,
			Mode:   req.OutputSchema.Mode,
			Reason: "CodeBuddy native structured output is not supported with control HITL",
		}
	}
	decisionSink, ok := sink.(driver.DecisionCapableSink)
	if !ok {
		return driver.Response{}, errControlSinkRequired
	}

	stdin := clihelper.NewStdinController()
	p := newParser(sink)
	p.enableControl(ctx, decisionSink, stdin, req.RunID, req.Policy.HumanDecision, prep.prompt)
	p.control.configDir = resolveConfigDir(cfg.Env)
	if req.Streaming {
		p.enableStreaming(req.RunID)
	} else {
		p.enableOutputReconstruction(req.RunID)
	}

	go func() {
		_ = stdin.Write(mustEncodeControlInitialize())
	}()

	result, err := clihelper.Run(ctx, clihelper.CommandRequest{
		Command: command,
		Args:    buildExecArgs(cfg, req, PermissionUnset, true),
		CWD:     prep.effectiveCWD,
		Env:     append(prep.env, driver.EnvBinding{Name: "CODEBUDDY_CODE_ENTRYPOINT", Value: "sdk-py"}),
		Observe: p.onChunk,
		Stdin:   stdin,
	}, sink)
	if err != nil {
		return driver.Response{}, err
	}
	p.finalize()

	raw := driver.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr, Terminal: p.terminal}
	if resumedCodeBuddySession(req) && isCodeBuddyResumeRejected(result.ExitCode, p.errorMessage, raw.Stdout, raw.Stderr) {
		reason := strings.TrimSpace(p.errorMessage)
		if reason == "" {
			reason = "codebuddy resume session is unavailable"
		}
		p.completeStream(&driver.RunFailure{Code: driver.FailureAgentError, Message: reason}, result.ExitCode, result.Signal, result.TimedOut)
		return driver.Response{}, &engine.ResumeRejectedError{Reason: reason}
	}
	failure := p.failureForOutcome(result.ExitCode)
	p.completeStream(failure, result.ExitCode, result.Signal, result.TimedOut)
	checkpoint := p.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                prep.effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: req.ProfilePayload.Fingerprint,
		}
	}
	return driver.Response{
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
		RuntimeServices: driverutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
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
