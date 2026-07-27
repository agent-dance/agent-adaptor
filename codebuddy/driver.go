package codebuddy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/engine"
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

func (adapter) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         true,
	}
}

var (
	validPermissionModes = map[PermissionMode]struct{}{
		PermissionDefault:     {},
		PermissionAcceptEdits: {},
		PermissionPlan:        {},
		PermissionAuto:        {},
		PermissionDontAsk:     {},
		PermissionBypass:      {},
	}
	validEfforts = map[ThinkingEffort]struct{}{
		"minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
	}
)

func (adapter) Descriptor() driver.Descriptor {
	fields := []driver.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the CodeBuddy CLI executable.", Hint: "Defaults to `codebuddy` when unset.", Default: defaultCommand, Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "CodeBuddy model identifier.", Default: defaultModel, Options: modelOptions(models()), Group: "model"},
		{Name: "effort", Label: "Reasoning Effort", Type: "select", Description: "Optional reasoning effort.", Options: []driver.ConfigOption{{Value: "minimal", Label: "Minimal"}, {Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "X-High"}, {Value: "max", Label: "Max"}}, Group: "model"},
		{Name: "permission_mode", Label: "Permission Mode", Type: "select", Description: "CodeBuddy headless permission mode. Leave empty to derive from run policy.", Options: []driver.ConfigOption{{Value: "default", Label: "Always Ask"}, {Value: "acceptEdits", Label: "Accept Edits"}, {Value: "plan", Label: "Plan"}, {Value: "auto", Label: "Auto"}, {Value: "dontAsk", Label: "Don't Ask"}, {Value: "bypassPermissions", Label: "Bypass Permissions"}}, Group: "execution"},
		{Name: "max_turns_per_run", Label: "Max Turns", Type: "number", Description: "Optional max-turns guard for one run.", Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return driver.Descriptor{
		Type:         DriverType,
		DisplayName:  "CodeBuddy Code",
		Models:       models(),
		ConfigSchema: &driver.ConfigSchema{Fields: fields},
		Sessions:     driver.SessionCapability{SupportsResume: true},
		Skills:       driver.SkillCapability{Supported: true, Mode: driver.SkillSyncPersistent},
		MCP:          driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: driver.InstructionsCapability{Supported: true},
		Workspace:    driver.WorkspaceCapability{Supported: true},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			// CodeBuddy CLI has no controllable flag for web search or
			// browser tooling (browser lives in the agent-browser plugin),
			// so the SDK does not model those dimensions. Their availability
			// is implicit in the user's local CodeBuddy configuration.
			Isolation: false, WebSearch: false, Browser: false,
			// SDK stream-json control requests provide all three blocking
			// decision classes. Retry is not supported because CodeBuddy does
			// not reissue the same control request.
			Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true, Retry: false},
		},
		Runtime: driver.RuntimeCapability{ReportsServices: true},
		StructuredOutput: driver.StructuredOutputCapability{
			JSONSchemaNative:         true,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStreaming:       false,
			WorksWithHITL:            false,
			Notes:                    "Native JSON Schema output uses CodeBuddy print-mode --output-format json --json-schema; stream-json/HITL combinations are not advertised.",
		},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	config, ok := asConfig(cfg)
	if !ok {
		return errors.New("codebuddy driver requires codebuddy.Config")
	}
	if config.PermissionMode != PermissionUnset {
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

func (adapter) CheckEnvironment(_ context.Context, cfg any) (driver.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = defaultCommand
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, driver.EnvironmentCheck{Code: "codebuddy_profile_error", Level: "fail", Message: "CodeBuddy profile resolution failed.", Detail: err.Error()})
		return adapterutil.SummarizeEnvironment(DriverType, checks), nil
	}
	checks = append(checks, authChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]driver.ModelInfo, error) {
	return models(), nil
}

func (adapter) DetectModel(_ context.Context, cfg any, _ *driver.ProfileSelection) (*driver.DetectedModel, error) {
	return detectEffectiveModel(readConfig(cfg)), nil
}

func (adapter) GetProfile(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection) (driver.AgentProfile, error) {
	return resolveProfile(readConfig(cfg).CommonConfig, profile), nil
}

func (adapter) ConfigSchema(_ context.Context, _ any) (*driver.ConfigSchema, error) {
	return adapter{}.Descriptor().ConfigSchema, nil
}

func (adapter) ListSkills(_ context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	bindings, err := effectiveBindings(readConfig(cfg).CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listSkills(payload, selected, resolved, bindings)
}

func (adapter) InjectSkills(_ context.Context, _ any, _ driver.ResolvedSkills, _ *driver.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	_, kind := profileAndKind(config.CommonConfig, profile)
	return syncSkills(ctx, payload, selected, resolved, bindings, kind)
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	skills, err := listSkills(payload.Skills, selected, resolved, bindings)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := profileAndKind(config.CommonConfig, profile)
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, false)
	mcpSnapshot, err := mcpruntime.SnapshotResource(DriverType, effectiveProfile.Dir, payload.MCP, false)
	if err != nil {
		return engine.ProfileSnapshot{}, err
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

func (adapter) SyncProfileResources(ctx context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := profileAndKind(config.CommonConfig, profile)
	skills, err := syncSkills(ctx, payload.Skills, selected, resolved, bindings, kind)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	mcpSnapshot, err := mcpruntime.SyncResource(ctx, DriverType, effectiveProfile.Dir, kind, payload.MCP)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, true)
	snapshot = profileconfig.WithSnapshotResource(snapshot, mcpSnapshot)
	if payload.Declared.Config {
		configSnapshot, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveProfile.Dir, payload.Config)
		if err != nil {
			return engine.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, configSnapshot)
	}
	if payload.Declared.Instructions {
		instructionsSnapshot, _, err := profileinstructions.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Instructions)
		if err != nil {
			return engine.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, instructionsSnapshot)
	}
	if payload.Declared.Agents {
		agentsSnapshot, err := profileagents.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Agents)
		if err != nil {
			return engine.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, agentsSnapshot)
	}
	if payload.Declared.Hooks {
		hooksSnapshot, err := profilehooks.Sync(ctx, DriverType, effectiveProfile.Dir, payload.Hooks)
		if err != nil {
			return engine.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, hooksSnapshot)
	}
	return snapshot, nil
}

func (a adapter) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
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
		return driver.Response{}, err
	}

	if wantsControlTransport(req.Policy.HumanDecision) {
		if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
			return driver.Response{}, &driver.StructuredOutputUnsupportedError{
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
	bindings      []driver.EnvBinding
	env           []driver.EnvBinding
	effectiveCWD  string
	prompt        string
	reportedModel string
}

func (a adapter) prepareRun(ctx context.Context, cfg Config, req driver.Request) (runPrep, error) {
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

func (adapter) runHeadless(ctx context.Context, cfg Config, command string, req driver.Request, sink driver.EventSink, prep runPrep) (driver.Response, error) {
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		if hasAnyArg(cfg.ExtraArgs, "--json-schema", "--output-format") {
			return driver.Response{}, &driver.InvalidOutputSchemaError{Reason: "CodeBuddy ExtraArgs must not include --json-schema or --output-format when SDK structured output is enabled"}
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
		return driver.Response{}, err
	}
	p.finalize()

	raw := driver.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr, Terminal: p.terminal}
	var failure *driver.RunFailure
	if p.pendingFailure != nil {
		failure = p.pendingFailure
	} else if strings.TrimSpace(p.errorMessage) != "" {
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: p.errorMessage}
	}
	checkpoint := p.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                prep.effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: req.ProfilePayload.Fingerprint,
		}
	}

	var structuredOutput *driver.StructuredOutput
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		structuredOutput = p.structuredOutput
	}

	return driver.Response{
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
		StructuredOutput: structuredOutput,
		RuntimeServices:  adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:          failure,
	}, nil
}

func validateSessionGuard(req driver.Request, effectiveCWD, profileFingerprint, skillFingerprintFallback string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	data := req.Session.State.Data
	if data[driver.SessionParamCWD] != "" && data[driver.SessionParamCWD] != effectiveCWD {
		return &engine.ResumeRejectedError{Reason: "session working directory changed"}
	}
	if data[driver.SessionParamWorkspaceID] != "" && data[driver.SessionParamWorkspaceID] != req.Workspace.ID {
		return &engine.ResumeRejectedError{Reason: "session workspace changed"}
	}
	if data[driver.SessionParamProfileFingerprint] != "" && data[driver.SessionParamProfileFingerprint] != profileFingerprint {
		return &engine.ResumeRejectedError{Reason: "profile resources changed"}
	}
	if data[driver.SessionParamProfileFingerprint] == "" &&
		data[driver.SessionParamPromptBundleKey] != "" &&
		data[driver.SessionParamPromptBundleKey] != skillFingerprintFallback {
		return &engine.ResumeRejectedError{Reason: "profile resources changed"}
	}
	return nil
}

func chooseCWD(cfg CommonConfig, workspace driver.WorkspaceLease) string {
	if workspace.CWD != "" {
		return workspace.CWD
	}
	return cfg.CWD
}

// asConfig accepts only the package-owned provider configuration.
func asConfig(cfg any) (Config, bool) {
	switch typed := cfg.(type) {
	case Config:
		return typed, true
	case *Config:
		if typed != nil {
			return *typed, true
		}
	}
	return Config{}, false
}

func readConfig(cfg any) Config {
	config, _ := asConfig(cfg)
	return config
}
