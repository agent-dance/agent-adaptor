package codex

import (
	"context"
	"fmt"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex/appserver"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
)

// runAppServer handles the req.Streaming=true code path. It spawns the
// codex app-server JSON-RPC subprocess, performs the handshake, and drives
// one turn while translating server notifications into StreamPayloads on
// the supplied sink.
//
// The function mirrors the existing exec --json path for session resume
// validation (CWD / workspace-id consistency) and checkpoint construction so
// the two transports are interchangeable from the SDK's point of view.
func runAppServer(
	ctx context.Context,
	req agentadaptor.DriverRunRequest,
	sink agentadaptor.EventSink,
	cfg agentadaptor.CodexConfig,
	command string,
	effectiveBindings []agentadaptor.EnvBinding,
	preparedInstructions profileinstructions.Prepared,
) (agentadaptor.DriverRunResult, error) {
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)

	if err := validateCodexSessionGuard(req, effectiveCWD, req.ProfilePayload.Fingerprint); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}

	approval := mapApprovalPolicy(req.Policy)
	sandbox := mapSandbox(req.Policy)

	resumeID := ""
	if req.Session != nil && req.Session.State != nil {
		resumeID = req.Session.State.ResumeID
	}

	opts := appserver.Options{
		Command:        command,
		CWD:            effectiveCWD,
		Env:            effectiveBindings,
		ClientName:     "agent-adaptor",
		ClientVersion:  "v0",
		Prompt:         prompt,
		ResumeThreadID: resumeID,
		// ephemeral=true tells codex app-server "do not persist this
		// thread to disk". Because we rely on SessionStore to resume
		// across process restarts, every run must leave a persistent
		// thread behind. Otherwise the threadId we stash in the
		// checkpoint is invalid the moment this subprocess exits, and
		// the next call with the same sessionKey silently starts a
		// brand new conversation (the 2026-04-21 regression).
		//
		// If a host ever wants truly stateless runs they can omit
		// WithSessionStore on the SDK, which takes us through the
		// non-streaming code path without a session binding.
		Ephemeral: false,
		Sandbox:   sandbox,
		Approval:  approval,
		Model:     strings.TrimSpace(cfg.Model),
		RunID:     req.RunID,
	}

	result, err := appserver.Run(ctx, opts, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	// Stamp CWD + workspace id onto the checkpoint so resume validation
	// works the same way as in the exec --json path.
	if result.Checkpoint != nil && result.Checkpoint.State != nil {
		if result.Checkpoint.State.Data == nil {
			result.Checkpoint.State.Data = map[string]string{}
		}
		result.Checkpoint.State.Data[agentadaptor.SessionParamCWD] = effectiveCWD
		result.Checkpoint.State.Data[agentadaptor.SessionParamWorkspaceID] = req.Workspace.ID
		result.Checkpoint.State.Data[agentadaptor.SessionParamProfileFingerprint] = req.ProfilePayload.Fingerprint
	}

	// Attach runtime-service reports: these are produced by the SDK
	// upstream rather than the app-server, but the codex adapter advertises
	// ReportsServices = true and so must surface them here as well.
	result.RuntimeServices = adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent)
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["transport"] = "app-server"
	return result, nil
}

func mapApprovalPolicy(p agentadaptor.RunPolicy) string {
	if p.Isolation == agentadaptor.IsolationUnrestricted {
		return "never"
	}
	switch p.HumanDecision.Permission {
	case agentadaptor.HumanDecisionAutoApprove:
		return "never"
	case agentadaptor.HumanDecisionAsk:
		return "on-request"
	case agentadaptor.HumanDecisionAutoReject:
		return "on-request"
	case agentadaptor.HumanDecisionUnset:
		return ""
	default:
		return ""
	}
}

func mapSandbox(p agentadaptor.RunPolicy) string {
	if p.Isolation == agentadaptor.IsolationUnrestricted {
		return "danger-full-access"
	}
	switch p.Isolation {
	case agentadaptor.IsolationReadOnly:
		return "read-only"
	case agentadaptor.IsolationWorkspaceWrite, agentadaptor.IsolationInherit:
		return "workspace-write"
	default:
		return "workspace-write"
	}
}

// Ensure fmt import stays used so future adjustments can reference it.
var _ = fmt.Sprintf
