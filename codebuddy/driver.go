package codebuddy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profileagents"
	"github.com/agent-dance/agent-adaptor/internal/profileconfig"
	"github.com/agent-dance/agent-adaptor/internal/profilehooks"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/profilesnapshot"
)

// DriverType is the stable descriptor type for the built-in CodeBuddy adapter.
const DriverType = "codebuddy"

// defaultCommand is the CodeBuddy CLI executable.
const defaultCommand = "codebuddy"

type adapter struct{}

// New returns a configured CodeBuddy AgentBinding. Hosts should pass the result
// to agentadaptor.WithDefaultAgent or agentadaptor.WithAgent.
func New(cfg agentadaptor.CodeBuddyConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.CodeBuddyConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

// NewAdapter returns the low-level CodeBuddy DriverAdapter.
func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         true,
	}
}

var (
	validPermissionModes = map[agentadaptor.CodeBuddyPermissionMode]struct{}{
		agentadaptor.CodeBuddyPermissionDefault:     {},
		agentadaptor.CodeBuddyPermissionAcceptEdits: {},
		agentadaptor.CodeBuddyPermissionPlan:        {},
		agentadaptor.CodeBuddyPermissionAuto:        {},
		agentadaptor.CodeBuddyPermissionDontAsk:     {},
		agentadaptor.CodeBuddyPermissionBypass:      {},
	}
	validEfforts = map[agentadaptor.ThinkingEffort]struct{}{
		"minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
	}
)

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	fields := []agentadaptor.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the CodeBuddy CLI executable.", Hint: "Defaults to `codebuddy` when unset.", Default: defaultCommand, Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "CodeBuddy model identifier.", Default: defaultModel, Options: modelOptions(models()), Group: "model"},
		{Name: "effort", Label: "Reasoning Effort", Type: "select", Description: "Optional reasoning effort.", Options: []agentadaptor.ConfigOption{{Value: "minimal", Label: "Minimal"}, {Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "X-High"}, {Value: "max", Label: "Max"}}, Group: "model"},
		{Name: "permission_mode", Label: "Permission Mode", Type: "select", Description: "CodeBuddy headless permission mode. Leave empty to derive from run policy.", Options: []agentadaptor.ConfigOption{{Value: "default", Label: "Always Ask"}, {Value: "acceptEdits", Label: "Accept Edits"}, {Value: "plan", Label: "Plan"}, {Value: "auto", Label: "Auto"}, {Value: "dontAsk", Label: "Don't Ask"}, {Value: "bypassPermissions", Label: "Bypass Permissions"}}, Group: "execution"},
		{Name: "max_turns_per_run", Label: "Max Turns", Type: "number", Description: "Optional max-turns guard for one run.", Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return agentadaptor.DriverDescriptor{
		Type:         DriverType,
		DisplayName:  "CodeBuddy Code",
		Models:       models(),
		ConfigSchema: &agentadaptor.ConfigSchema{Fields: fields},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncPersistent},
		MCP:          agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			// CodeBuddy CLI has no controllable flag for web search or
			// browser tooling (browser lives in the agent-browser plugin),
			// so the SDK does not model those dimensions. Their availability
			// is implicit in the user's local CodeBuddy configuration.
			Isolation: false, WebSearch: false, Browser: false,
			// SDK stream-json control requests provide all three blocking
			// decision classes. Retry is not supported because CodeBuddy does
			// not reissue the same control request.
			Permission: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			Question:   agentadaptor.QuestionSupport{Ask: true, AutoReject: true, Retry: false},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
		StructuredOutput: agentadaptor.StructuredOutputCapability{
			JSONSchemaNative:         true,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStart:           true,
			WorksWithStreaming:       false,
			WorksWithHITL:            false,
			Notes:                    "Native JSON Schema output uses CodeBuddy print-mode --output-format json --json-schema; stream-json/HITL combinations are not advertised.",
		},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	config, ok := asConfig(cfg)
	if !ok {
		return errors.New("codebuddy driver requires agentadaptor.CodeBuddyConfig")
	}
	if config.PermissionMode != agentadaptor.CodeBuddyPermissionUnset {
		if _, ok := validPermissionModes[config.PermissionMode]; !ok {
			return fmt.Errorf("codebuddy: unsupported permission_mode %q", config.PermissionMode)
		}
	}
	if config.Effort != "" {
		if _, ok := validEfforts[config.Effort]; !ok {
			return fmt.Errorf("codebuddy: unsupported effort %q", config.Effort)
		}
	}
	return nil
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (agentadaptor.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = defaultCommand
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, agentadaptor.EnvironmentCheck{Code: "codebuddy_profile_error", Level: "fail", Message: "CodeBuddy profile resolution failed.", Detail: err.Error()})
		return adapterutil.SummarizeEnvironment(DriverType, checks), nil
	}
	checks = append(checks, authChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return models(), nil
}

func (adapter) DetectModel(_ context.Context, cfg any, _ *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error) {
	return detectEffectiveModel(readConfig(cfg)), nil
}

func (adapter) GetProfile(_ context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	return resolveProfile(readConfig(cfg).CommonConfig, profile), nil
}

func (adapter) ConfigSchema(_ context.Context, _ any) (*agentadaptor.ConfigSchema, error) {
	return adapter{}.Descriptor().ConfigSchema, nil
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	bindings, err := effectiveBindings(readConfig(cfg).CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return listSkills(payload, selected, resolved, bindings)
}

func (adapter) InjectSkills(_ context.Context, _ any, _ agentadaptor.ResolvedSkills, _ *agentadaptor.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	_, kind := profileAndKind(config.CommonConfig, profile)
	return syncSkills(ctx, payload, selected, resolved, bindings, kind)
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection, payload agentadaptor.ProfilePayload, selected []string, resolved []agentadaptor.Skill) (agentadaptor.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	skills, err := listSkills(payload.Skills, selected, resolved, bindings)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := profileAndKind(config.CommonConfig, profile)
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
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := profileAndKind(config.CommonConfig, profile)
	skills, err := syncSkills(ctx, payload.Skills, selected, resolved, bindings, kind)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
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

func (a adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	if m := strings.TrimSpace(req.ModelOverride); m != "" {
		cfg.Model = m
	}
	command := cfg.Command
	if command == "" {
		command = defaultCommand
	}

	prep, err := a.prepareRun(ctx, cfg, req)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	if wantsControlTransport(req.Policy.HumanDecision) {
		if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
			return agentadaptor.DriverRunResult{}, &agentadaptor.StructuredOutputUnsupportedError{
				Adapter: DriverType,
				Mode:    req.OutputSchema.Mode,
				Reason:  "CodeBuddy native structured output is not supported with control HITL",
			}
		}
		return a.runControl(ctx, cfg, command, req, sink, prep)
	}
	return a.runHeadless(ctx, cfg, command, req, sink, prep)
}

// runPrep carries the resolved per-run inputs shared by both engines.
type runPrep struct {
	bindings      []agentadaptor.EnvBinding
	env           []agentadaptor.EnvBinding
	effectiveCWD  string
	prompt        string
	reportedModel string
}

func (a adapter) prepareRun(ctx context.Context, cfg agentadaptor.CodeBuddyConfig, req agentadaptor.DriverRunRequest) (runPrep, error) {
	profileFingerprint := req.ProfilePayload.Fingerprint
	if _, err := effectiveBindingsNoInitialize(cfg.CommonConfig, req.Profile); err != nil {
		return runPrep{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if err := validateSessionGuard(req, effectiveCWD, profileFingerprint, req.Skills.Fingerprint); err != nil {
		return runPrep{}, err
	}
	bindings, err := effectiveBindings(cfg.CommonConfig, req.Profile)
	if err != nil {
		return runPrep{}, err
	}
	effectiveProfile, kind := profileAndKind(cfg.CommonConfig, req.Profile)
	if _, err := syncSkills(ctx, req.Skills, req.Skills.Keys(), nil, bindings, kind); err != nil {
		return runPrep{}, err
	}
	if _, err := mcpruntime.SyncResource(ctx, DriverType, effectiveProfile.Dir, kind, req.MCP); err != nil {
		return runPrep{}, err
	}
	if req.ProfilePayload.Declared.Config {
		if _, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Config); err != nil {
			return runPrep{}, err
		}
	}
	var preparedInstructions profileinstructions.Prepared
	if req.ProfilePayload.Declared.Instructions {
		preparedInstructions, err = profileinstructions.PrepareForRun(ctx, DriverType, effectiveProfile.Dir, effectiveCWD, req.Instructions)
		if err != nil {
			return runPrep{}, err
		}
	}
	if req.ProfilePayload.Declared.Agents {
		if _, err := profileagents.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Agents); err != nil {
			return runPrep{}, err
		}
	}
	if req.ProfilePayload.Declared.Hooks {
		if _, err := profilehooks.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Hooks); err != nil {
			return runPrep{}, err
		}
	}
	env, err := adapterutil.RuntimeEnvBindings(bindings, req.Runtime)
	if err != nil {
		return runPrep{}, err
	}

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}

	reportedModel := requestedModelFlag(cfg)
	if reportedModel == "" {
		if detected := detectEffectiveModel(cfg); detected != nil {
			reportedModel = detected.Model
		}
	}

	return runPrep{
		bindings:      bindings,
		env:           env,
		effectiveCWD:  effectiveCWD,
		prompt:        prompt,
		reportedModel: reportedModel,
	}, nil
}

func (adapter) runHeadless(ctx context.Context, cfg agentadaptor.CodeBuddyConfig, command string, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink, prep runPrep) (agentadaptor.DriverRunResult, error) {
	if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
		if hasAnyArg(cfg.ExtraArgs, "--json-schema", "--output-format") {
			return agentadaptor.DriverRunResult{}, &agentadaptor.InvalidOutputSchemaError{Reason: "CodeBuddy ExtraArgs must not include --json-schema or --output-format when SDK structured output is enabled"}
		}
	}

	permMode := headlessPermissionMode(cfg, req.Policy)
	args := buildExecArgs(cfg, req, permMode, false)
	args = append(args, prep.prompt)

	p := newParser(sink)
	if req.Streaming {
		p.enableStreaming(req.RunID)
	}

	runReq := clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     prep.effectiveCWD,
		Env:     prep.env,
		Observe: p.onChunk,
	}

	result, err := clihelper.Run(ctx, runReq, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	p.finalize()

	raw := agentadaptor.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr}
	checkpoint := p.checkpoint(result.ExitCode)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                prep.effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: req.ProfilePayload.Fingerprint,
		}
	}
	var failure *agentadaptor.RunFailure
	if strings.TrimSpace(p.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{Code: agentadaptor.FailureAgentError, Message: p.errorMessage}
	}

	var structuredOutput *agentadaptor.StructuredOutput
	if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
		structuredOutput = p.structuredOutput
	}

	return agentadaptor.DriverRunResult{
		Output:           p.buildOutput(),
		RawStreams:       &raw,
		Transcript:       p.transcript,
		ExitCode:         result.ExitCode,
		Signal:           result.Signal,
		TimedOut:         result.TimedOut,
		Usage:            p.usage,
		Checkpoint:       checkpoint,
		Metadata:         p.outputMetadata(),
		Provider:         "codebuddy",
		Model:            prep.reportedModel,
		Summary:          p.finalSummary(),
		Result:           p.resultFinal,
		StructuredOutput: structuredOutput,
		RuntimeServices:  adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:          failure,
	}, nil
}

func validateSessionGuard(req agentadaptor.DriverRunRequest, effectiveCWD, profileFingerprint, legacyBundleKey string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	data := req.Session.State.Data
	if data[driver.SessionParamCWD] != "" && data[driver.SessionParamCWD] != effectiveCWD {
		return &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
	}
	if data[driver.SessionParamWorkspaceID] != "" && data[driver.SessionParamWorkspaceID] != req.Workspace.ID {
		return &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
	}
	if data[driver.SessionParamProfileFingerprint] != "" && data[driver.SessionParamProfileFingerprint] != profileFingerprint {
		return &agentadaptor.ResumeRejectedError{Reason: "profile resources changed"}
	}
	if data[driver.SessionParamProfileFingerprint] == "" &&
		data[driver.SessionParamPromptBundleKey] != "" &&
		data[driver.SessionParamPromptBundleKey] != legacyBundleKey {
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

// asConfig normalizes every config shape the adapter accepts. The v1 entry
// point (Driver) hands over the package-owned Config, so this is the single
// point where it is converted for the shared adapter path.
func asConfig(cfg any) (agentadaptor.CodeBuddyConfig, bool) {
	switch typed := cfg.(type) {
	case agentadaptor.CodeBuddyConfig:
		return typed, true
	case *agentadaptor.CodeBuddyConfig:
		if typed != nil {
			return *typed, true
		}
	case Config:
		return typed.engineConfig(), true
	case *Config:
		if typed != nil {
			return typed.engineConfig(), true
		}
	}
	return agentadaptor.CodeBuddyConfig{}, false
}

func readConfig(cfg any) agentadaptor.CodeBuddyConfig {
	config, _ := asConfig(cfg)
	return config
}
