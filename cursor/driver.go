package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profileagents"
	"github.com/agent-dance/agent-adaptor/internal/profileconfig"
	"github.com/agent-dance/agent-adaptor/internal/profilehooks"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/profilesnapshot"
)

// DriverType is the stable descriptor type for the built-in Cursor adapter.
const DriverType = "cursor"

type adapter struct{}

// New returns a configured Cursor AgentBinding. Hosts should pass the result
// to agentadaptor.WithDefaultAgent or agentadaptor.WithAgent; direct adapter
// use is reserved for lower-level tests and custom plumbing.
func New(cfg agentadaptor.CursorConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.CursorConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

// NewAdapter returns the low-level Cursor DriverAdapter. Most hosts should use
// New so config and binding defaults travel together.
func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	fields := []agentadaptor.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the Cursor Agent CLI executable.", Hint: "Defaults to `agent` when unset.", Default: "agent", Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "Cursor model identifier, for example gpt-5.", Default: "gpt-5", Options: modelOptions(cursorModels()), Group: "model"},
		{Name: "mode", Label: "Mode", Type: "select", Description: "Cursor agent mode passed through --mode.", Options: []agentadaptor.ConfigOption{{Value: "agent", Label: "Agent"}, {Value: "ask", Label: "Ask"}}, Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return agentadaptor.DriverDescriptor{
		Type:         DriverType,
		DisplayName:  "Cursor Agent",
		Models:       cursorModels(),
		ConfigSchema: &agentadaptor.ConfigSchema{Fields: fields},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncPersistent},
		MCP:          agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Isolation: false, WebSearch: true, Browser: false,
			Permission: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			Question:   agentadaptor.QuestionSupport{Ask: false, AutoReject: false, Retry: false},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
	}
}

func cursorModels() []agentadaptor.ModelInfo {
	return []agentadaptor.ModelInfo{
		{ID: "gpt-5", Label: "gpt-5"},
		{ID: "claude-sonnet-4", Label: "claude-sonnet-4"},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case agentadaptor.CursorConfig, *agentadaptor.CursorConfig:
		return nil
	default:
		return errors.New("cursor driver requires agentadaptor.CursorConfig")
	}
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (agentadaptor.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = "agent"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveCursorBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, agentadaptor.EnvironmentCheck{Code: "cursor_profile_error", Level: "fail", Message: "Cursor profile resolution failed.", Detail: err.Error()})
		return adapterutil.SummarizeEnvironment(DriverType, checks), nil
	}
	cursorHome := resolveCursorHome(bindings)
	if _, err := os.Stat(cursorHome); err == nil {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_home_present",
			Level:   "info",
			Message: "Cursor home directory exists.",
			Detail:  cursorHome,
		})
	} else {
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_home_missing",
			Level:   "warn",
			Message: "Cursor home directory was not found yet.",
			Detail:  cursorHome,
			Hint:    "Run Cursor Agent once, use a profile option, or point CURSOR_HOME at the target operator profile.",
		})
	}
	checks = append(checks, cursorAuthChecks(bindings)...)
	checks = append(checks, cursorConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return adapter{}.Descriptor().Models, nil
}

func (adapter) DetectModel(_ context.Context, cfg any, profile *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Model) == "" {
		for _, candidate := range cursorConfigCandidates(bindings) {
			if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
				return &agentadaptor.DetectedModel{
					Model:      model,
					Provider:   "cursor",
					Source:     "config_file",
					Candidates: []string{model},
				}, nil
			}
		}
		return nil, nil
	}
	return &agentadaptor.DetectedModel{
		Model:      config.Model,
		Provider:   "cursor",
		Source:     "binding_config",
		Candidates: []string{config.Model},
	}, nil
}

func (adapter) GetProfile(_ context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	return cursorProfile(readConfig(cfg).CommonConfig, profile), nil
}

func cursorConfigCandidates(bindings []agentadaptor.EnvBinding) []string {
	home := resolveCursorHome(bindings)
	return []string{
		filepath.Join(home, "config.json"),
		filepath.Join(home, "settings.json"),
		filepath.Join(home, "argv.json"),
	}
}

func cursorConfigChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	checks := make([]agentadaptor.EnvironmentCheck, 0)
	for _, candidate := range cursorConfigCandidates(bindings) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "cursor_config_present",
			Level:   "info",
			Message: "Cursor config/state file is present.",
			Detail:  candidate,
		})
		if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
			checks = append(checks, agentadaptor.EnvironmentCheck{
				Code:    "cursor_config_model",
				Level:   "info",
				Message: "Cursor config file declares a default model.",
				Detail:  model,
			})
		}
	}
	return checks
}

func modelOptions(models []agentadaptor.ModelInfo) []agentadaptor.ConfigOption {
	if len(models) == 0 {
		return nil
	}
	options := make([]agentadaptor.ConfigOption, 0, len(models))
	for _, model := range models {
		options = append(options, agentadaptor.ConfigOption{
			Value: model.ID,
			Label: model.Label,
		})
	}
	return options
}

func (adapter) ConfigSchema(_ context.Context, _ any) (*agentadaptor.ConfigSchema, error) {
	return adapter{}.Descriptor().ConfigSchema, nil
}

func (adapter) GetQuota(_ context.Context, _ any, _ *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error) {
	return agentadaptor.QuotaReport{
		DriverType: DriverType,
		Provider:   "cursor",
		Available:  false,
		Error:      "Cursor live quota probing is not available in this SDK build",
	}, nil
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return listCursorSkills(payload, selected, resolved, bindings)
}

// InjectSkills is the pre-run hook the SDK invokes before calling Run. For
// Cursor we must use the profile-aware CURSOR_HOME that only becomes
// available once Run produces the effective bindings, so the heavy lifting
// is performed in Run itself. Implementing the interface here keeps the
// adapter spec explicit.
func (adapter) InjectSkills(_ context.Context, _ any, _ agentadaptor.ResolvedSkills, _ *agentadaptor.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return syncCursorSkills(ctx, payload, selected, resolved, bindings, noopCursorSink{})
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection, payload agentadaptor.ProfilePayload, selected []string, resolved []agentadaptor.Skill) (agentadaptor.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	skills, err := listCursorSkills(payload.Skills, selected, resolved, bindings)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := cursorProfileAndKind(config.CommonConfig, profile)
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, false)
	mcpSnapshot, err := mcpruntime.SnapshotResource(DriverType, effectiveProfile.Dir, payload.MCP, false)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	snapshot = profileconfig.WithSnapshotResource(snapshot, mcpSnapshot)
	if payload.Declared.Config {
		snapshot = profileconfig.WithSnapshotResource(snapshot, profileconfig.Snapshot(DriverType, effectiveProfile.Dir, payload.Config, false))
	}
	if payload.Declared.Instructions {
		snapshot = profileconfig.WithSnapshotResource(snapshot, profileinstructions.Snapshot(DriverType, effectiveProfile.Dir, payload.Instructions, false))
	}
	if payload.Declared.Agents {
		snapshot = profileconfig.WithSnapshotResource(snapshot, profileagents.Snapshot(DriverType, effectiveProfile.Dir, payload.Agents, false))
	}
	if payload.Declared.Hooks {
		snapshot = profileconfig.WithSnapshotResource(snapshot, profilehooks.Snapshot(DriverType, effectiveProfile.Dir, payload.Hooks, false))
	}
	return snapshot, nil
}

func (adapter) SyncProfileResources(ctx context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection, payload agentadaptor.ProfilePayload, selected []string, resolved []agentadaptor.Skill) (agentadaptor.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	skills, err := syncCursorSkills(ctx, payload.Skills, selected, resolved, bindings, noopCursorSink{})
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := cursorProfileAndKind(config.CommonConfig, profile)
	mcpSnapshot, err := mcpruntime.SyncResource(ctx, DriverType, effectiveProfile.Dir, kind, payload.MCP)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, true)
	snapshot = profileconfig.WithSnapshotResource(snapshot, mcpSnapshot)
	if payload.Declared.Config {
		configSnapshot, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveProfile.Dir, payload.Config)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, configSnapshot)
	}
	if payload.Declared.Instructions {
		instructionsSnapshot, _, err := profileinstructions.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Instructions)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, instructionsSnapshot)
	}
	if payload.Declared.Agents {
		agentsSnapshot, err := profileagents.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Agents)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, agentsSnapshot)
	}
	if payload.Declared.Hooks {
		hooksSnapshot, err := profilehooks.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Hooks)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, hooksSnapshot)
	}
	return snapshot, nil
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	command := cfg.Command
	if command == "" {
		command = "agent"
	}
	profileFingerprint := req.ProfilePayload.Fingerprint
	bindings, err := effectiveCursorBindingsNoInitialize(cfg.CommonConfig, req.Profile)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if err := validateCursorSessionGuard(req, effectiveCWD, profileFingerprint); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	bindings, err = effectiveCursorBindings(cfg.CommonConfig, req.Profile)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveEnv, err := adapterutil.RuntimeEnvBindings(bindings, req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveProfile, profileKind := cursorProfileAndKind(cfg.CommonConfig, req.Profile)
	if _, err := mcpruntime.SyncResource(ctx, DriverType, effectiveProfile.Dir, profileKind, req.MCP); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if req.ProfilePayload.Declared.Config {
		if _, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Config); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	var preparedInstructions profileinstructions.Prepared
	if req.ProfilePayload.Declared.Instructions {
		preparedInstructions, err = profileinstructions.PrepareForRun(ctx, DriverType, effectiveProfile.Dir, effectiveCWD, req.Instructions)
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	if req.ProfilePayload.Declared.Agents {
		if _, err := profileagents.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Agents); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	if req.ProfilePayload.Declared.Hooks {
		if _, err := profilehooks.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Hooks); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	if _, err := syncCursorSkills(ctx, req.Skills, req.Skills.Keys(), nil, effectiveEnv, sink); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	args := []string{"-p", "--output-format", "stream-json", "--workspace", effectiveCWD}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Mode != "" {
		args = append(args, "--mode", string(cfg.Mode))
	}
	if req.Policy.HumanDecision.Permission == agentadaptor.HumanDecisionAutoApprove {
		args = append(args, "--force")
	}
	args = append(args, cfg.ExtraArgs...)

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}

	parser := newCursorParser(sink)
	result, err := clihelper.Run(ctx, clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     effectiveCWD,
		Env:     effectiveEnv,
		Prompt:  prompt,
		Observe: parser.onChunk,
	}, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	parser.finalize()
	raw := agentadaptor.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr}
	checkpoint := parser.checkpoint(result.ExitCode)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			agentadaptor.SessionParamCWD:                effectiveCWD,
			agentadaptor.SessionParamWorkspaceID:        req.Workspace.ID,
			agentadaptor.SessionParamProfileFingerprint: profileFingerprint,
		}
	}
	var failure *agentadaptor.RunFailure
	if strings.TrimSpace(parser.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureAgentError,
			Message: parser.errorMessage,
		}
	}

	return agentadaptor.DriverRunResult{
		Output:          parser.buildOutput(),
		RawStreams:      &raw,
		Transcript:      parser.transcript,
		ExitCode:        result.ExitCode,
		Signal:          result.Signal,
		TimedOut:        result.TimedOut,
		Usage:           parser.usage,
		Checkpoint:      checkpoint,
		Provider:        "cursor",
		Model:           cfg.Model,
		Summary:         parser.finalSummary(),
		Result:          parser.resultFinal,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:         failure,
	}, nil
}

func validateCursorSessionGuard(req agentadaptor.DriverRunRequest, effectiveCWD, profileFingerprint string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	if req.Session.State.Data[agentadaptor.SessionParamCWD] != "" && req.Session.State.Data[agentadaptor.SessionParamCWD] != effectiveCWD {
		return &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
	}
	if req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != "" && req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != req.Workspace.ID {
		return &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
	}
	if req.Session.State.Data[agentadaptor.SessionParamProfileFingerprint] != "" && req.Session.State.Data[agentadaptor.SessionParamProfileFingerprint] != profileFingerprint {
		return &agentadaptor.ResumeRejectedError{Reason: "profile resources changed"}
	}
	return nil
}

func chooseCWD(cfg agentadaptor.CommonConfig, workspace agentadaptor.WorkspaceLease) string {
	if workspace.CWD != "" {
		return workspace.CWD
	}
	return cfg.CWD
}

func readConfig(cfg any) agentadaptor.CursorConfig {
	switch typed := cfg.(type) {
	case agentadaptor.CursorConfig:
		return typed
	case *agentadaptor.CursorConfig:
		if typed != nil {
			return *typed
		}
	}
	return agentadaptor.CursorConfig{}
}
