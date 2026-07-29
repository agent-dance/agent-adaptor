package codex

import (
	"context"
	"strings"

	"github.com/agent-dance/agent-adaptor/codex/appserver"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
)

// runAppServer handles provider-streaming invocations and SessionFork. Forks
// use the official app-server thread/fork method even when the public caller
// selected Run; the unified event pipeline drains the provider stream.
//
// The function mirrors the existing exec --json path for session resume
// validation (CWD / workspace-id consistency) and checkpoint construction so
// the two transports are interchangeable from the SDK's point of view.
func runAppServer(
	ctx context.Context,
	req driver.Request,
	sink driver.EventSink,
	cfg Config,
	command string,
	effectiveBindings []driver.EnvBinding,
	preparedInstructions profileinstructions.Prepared,
) (driver.Response, error) {
	opts, err := buildAppServerOptions(req, cfg, command, effectiveBindings, preparedInstructions)
	if err != nil {
		return driver.Response{}, err
	}
	result, err := appserver.Run(ctx, opts, sink)
	return finishAppServerResult(req, result, chooseCWD(cfg.CommonConfig, req.Workspace)), err
}

func buildAppServerOptions(
	req driver.Request,
	cfg Config,
	command string,
	effectiveBindings []driver.EnvBinding,
	preparedInstructions profileinstructions.Prepared,
) (appserver.Options, error) {
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)

	if err := validateCodexSessionGuard(req, effectiveCWD, req.ProfilePayload.Fingerprint); err != nil {
		return appserver.Options{}, err
	}

	prompt := req.Prompt
	if runtimePrefix := driverutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}

	approval := mapApprovalPolicy(req.Policy)
	sandbox := mapSandbox(req.Policy)

	resumeID, forkID := codexAppServerThreadIDs(req.Session)
	if err := validateCodexForkRequest(req); err != nil {
		return appserver.Options{}, err
	}
	extraArgs, err := codexAppServerExtraArgs(cfg.ExtraArgs, req.Policy)
	if err != nil {
		return appserver.Options{}, err
	}
	model, effort, serviceTier := codexAppServerConfigProjection(cfg)

	opts := appserver.Options{
		Command:        command,
		ExtraArgs:      extraArgs,
		CWD:            effectiveCWD,
		Env:            effectiveBindings,
		ClientName:     "agent-adaptor",
		ClientVersion:  "v0",
		Prompt:         prompt,
		ResumeThreadID: resumeID,
		ForkThreadID:   forkID,
		// Keep app-server threads persistent so any checkpoint returned to a
		// Thread remains resumable after this subprocess exits. Stateless Agent
		// runs simply ignore the checkpoint.
		Ephemeral:   false,
		Sandbox:     sandbox,
		Approval:    approval,
		Model:       model,
		Effort:      effort,
		ServiceTier: serviceTier,
		RunID:       req.RunID,
	}
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		opts.OutputSchema = req.OutputSchema
	}

	return opts, nil
}

func finishAppServerResult(req driver.Request, result driver.Response, effectiveCWD string) driver.Response {
	// Stamp CWD + workspace id onto the checkpoint so resume validation
	// works the same way as in the exec --json path.
	if result.Checkpoint != nil && result.Checkpoint.State != nil {
		if result.Checkpoint.State.Data == nil {
			result.Checkpoint.State.Data = map[string]string{}
		}
		result.Checkpoint.State.Data[driver.SessionParamCWD] = effectiveCWD
		result.Checkpoint.State.Data[driver.SessionParamWorkspaceID] = req.Workspace.ID
		result.Checkpoint.State.Data[driver.SessionParamProfileFingerprint] = req.ProfilePayload.Fingerprint
	}

	// Attach runtime-service reports: these are produced by the SDK
	// upstream rather than the app-server, but the codex adapter advertises
	// ReportsServices = true and so must surface them here as well.
	result.RuntimeServices = driverutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent)
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["transport"] = "app-server"
	return result
}

func codexAppServerThreadIDs(session *driver.SessionContext) (resumeID, forkID string) {
	if session == nil || session.State == nil {
		return "", ""
	}
	if session.Mode == driver.SessionFork {
		return "", session.State.ResumeID
	}
	return session.State.ResumeID, ""
}

func codexAppServerConfigProjection(cfg Config) (model, effort, serviceTier string) {
	model = strings.TrimSpace(cfg.Model)
	effort = string(cfg.ReasoningEffort)
	if cfg.FastMode {
		serviceTier = "fast"
	}
	return model, effort, serviceTier
}

func mapApprovalPolicy(p driver.RunPolicy) string {
	switch driver.EffectiveHumanDecisionPolicy(p.HumanDecision).Permission {
	case driver.HumanDecisionAutoApprove:
		return "never"
	case driver.HumanDecisionAsk:
		return "on-request"
	case driver.HumanDecisionAutoReject:
		return "on-request"
	default:
		return ""
	}
}

func mapSandbox(p driver.RunPolicy) string {
	switch p.Isolation {
	case driver.IsolationReadOnly:
		return "read-only"
	case driver.IsolationWorkspaceWrite:
		return "workspace-write"
	case driver.IsolationUnrestricted:
		return "danger-full-access"
	default:
		return ""
	}
}
