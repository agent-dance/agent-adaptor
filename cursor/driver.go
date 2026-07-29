package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profileagents"
	"github.com/agent-dance/agent-adaptor/internal/profileconfig"
	"github.com/agent-dance/agent-adaptor/internal/profilehooks"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/internal/profilesnapshot"
)

// DriverType is the stable descriptor type for the built-in Cursor driver.
const DriverType = "cursor"

type adapter struct{}

func (adapter) Descriptor() driver.Descriptor {
	fields := []driver.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the Cursor Agent CLI executable.", Hint: "Defaults to `agent` when unset.", Default: "agent", Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "Cursor model identifier, for example gpt-5.", Default: "gpt-5", Options: modelOptions(cursorModels()), Group: "model"},
		{Name: "mode", Label: "Mode", Type: "select", Description: "Cursor agent mode passed through --mode.", Options: []driver.ConfigOption{{Value: "agent", Label: "Agent"}, {Value: "ask", Label: "Ask"}}, Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return driver.Descriptor{
		Type:         DriverType,
		DisplayName:  "Cursor Agent",
		Models:       cursorModels(),
		ConfigSchema: &driver.ConfigSchema{Fields: fields},
		Sessions:     driver.SessionCapability{SupportsResume: true},
		Skills:       driver.SkillCapability{Supported: true, Mode: driver.SkillSyncPersistent},
		MCP:          driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: driver.InstructionsCapability{Supported: true},
		Workspace:    driver.WorkspaceCapability{Supported: true},
		Process:      driver.ProcessCapability{Persistent: false},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			Isolation: false, WebSearch: false, Browser: false,
			Permission: driver.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			PlanReview: driver.HumanDecisionSupport{},
			Question:   driver.QuestionSupport{Ask: false, AutoReject: false, Retry: false},
		},
		Runtime: driver.RuntimeCapability{ReportsServices: true},
		StructuredOutput: driver.StructuredOutputCapability{
			JSONSchemaNative:         false,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStreaming:       true,
			WorksWithHITL:            false,
			Notes:                    "Cursor CLI exposes JSON/stream-json envelopes but no native JSON Schema output surface; use explicit prompt validation.",
		},
	}
}

func cursorModels() []driver.ModelInfo {
	return []driver.ModelInfo{
		{ID: "gpt-5", Label: "gpt-5"},
		{ID: "claude-sonnet-4", Label: "claude-sonnet-4"},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case Config, *Config:
		return nil
	default:
		return errors.New("cursor driver requires cursor.Config")
	}
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (driver.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = "agent"
	}
	checks := append(driverutil.CommandEnvironmentChecks(command), driverutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveCursorBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, driver.EnvironmentCheck{Code: "cursor_profile_error", Level: "fail", Message: "Cursor profile resolution failed.", Detail: err.Error()})
		return driverutil.SummarizeEnvironment(DriverType, checks), nil
	}
	cursorHome := resolveCursorHome(bindings)
	if _, err := os.Stat(cursorHome); err == nil {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "cursor_home_present",
			Level:   "info",
			Message: "Cursor home directory exists.",
			Detail:  cursorHome,
		})
	} else {
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "cursor_home_missing",
			Level:   "warn",
			Message: "Cursor home directory was not found yet.",
			Detail:  cursorHome,
			Hint:    "Run Cursor Agent once, use a profile option, or point CURSOR_HOME at the target operator profile.",
		})
	}
	checks = append(checks, cursorAuthChecks(bindings)...)
	checks = append(checks, cursorConfigChecks(bindings)...)
	return driverutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]driver.ModelInfo, error) {
	return adapter{}.Descriptor().Models, nil
}

func (adapter) DetectModel(_ context.Context, cfg any, profile *driver.ProfileSelection) (*driver.DetectedModel, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Model) == "" {
		for _, candidate := range cursorConfigCandidates(bindings) {
			if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
				return &driver.DetectedModel{
					Model:      model,
					Provider:   "cursor",
					Source:     "config_file",
					Candidates: []string{model},
				}, nil
			}
		}
		return nil, nil
	}
	return &driver.DetectedModel{
		Model:      config.Model,
		Provider:   "cursor",
		Source:     "binding_config",
		Candidates: []string{config.Model},
	}, nil
}

func (adapter) GetProfile(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection) (driver.AgentProfile, error) {
	return cursorProfile(readConfig(cfg).CommonConfig, profile), nil
}

func cursorConfigCandidates(bindings []driver.EnvBinding) []string {
	home := resolveCursorHome(bindings)
	return []string{
		filepath.Join(home, "config.json"),
		filepath.Join(home, "settings.json"),
		filepath.Join(home, "argv.json"),
	}
}

func cursorConfigChecks(bindings []driver.EnvBinding) []driver.EnvironmentCheck {
	checks := make([]driver.EnvironmentCheck, 0)
	for _, candidate := range cursorConfigCandidates(bindings) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "cursor_config_present",
			Level:   "info",
			Message: "Cursor config/state file is present.",
			Detail:  candidate,
		})
		if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
			checks = append(checks, driver.EnvironmentCheck{
				Code:    "cursor_config_model",
				Level:   "info",
				Message: "Cursor config file declares a default model.",
				Detail:  model,
			})
		}
	}
	return checks
}

func modelOptions(models []driver.ModelInfo) []driver.ConfigOption {
	if len(models) == 0 {
		return nil
	}
	options := make([]driver.ConfigOption, 0, len(models))
	for _, model := range models {
		options = append(options, driver.ConfigOption{
			Value: model.ID,
			Label: model.Label,
		})
	}
	return options
}

func (adapter) ConfigSchema(_ context.Context, _ any) (*driver.ConfigSchema, error) {
	return adapter{}.Descriptor().ConfigSchema, nil
}

func (adapter) GetQuota(_ context.Context, _ any, _ *driver.ProfileSelection) (driver.QuotaReport, error) {
	return driver.QuotaReport{
		DriverType: DriverType,
		Provider:   "cursor",
		Available:  false,
		Error:      "Cursor live quota probing is not available in this SDK build",
	}, nil
}

func (adapter) ListSkills(_ context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listCursorSkills(payload, selected, resolved, bindings)
}

// InjectSkills is the pre-run hook the SDK invokes before calling Run. For
// Cursor we must use the profile-aware CURSOR_HOME that only becomes
// available once Run produces the effective bindings, so the heavy lifting
// is performed in Run itself. Implementing the interface here keeps the
// adapter spec explicit.
func (adapter) InjectSkills(_ context.Context, _ any, _ driver.ResolvedSkills, _ *driver.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return syncCursorSkills(ctx, payload, selected, resolved, bindings, noopCursorSink{})
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	skills, err := listCursorSkills(payload.Skills, selected, resolved, bindings)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := cursorProfileAndKind(config.CommonConfig, profile)
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
	bindings, err := effectiveCursorBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	skills, err := syncCursorSkills(ctx, payload.Skills, selected, resolved, bindings, noopCursorSink{})
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := cursorProfileAndKind(config.CommonConfig, profile)
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

func (adapter) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if req.OutputSchema != nil && req.OutputSchema.Mode == driver.StructuredOutputNativeStrict {
		return driver.Response{}, &driver.StructuredOutputUnsupportedError{Driver: DriverType, Mode: req.OutputSchema.Mode, Reason: "Cursor CLI does not expose native JSON Schema output"}
	}
	cfg := readConfig(req.Config)
	// Per-run WithModel overrides the configured model for this invocation only.
	if m := strings.TrimSpace(req.ModelOverride); m != "" {
		cfg.Model = m
	}
	command := cfg.Command
	if command == "" {
		command = "agent"
	}
	profileFingerprint := req.ProfilePayload.Fingerprint
	bindings, err := effectiveCursorBindingsNoInitialize(cfg.CommonConfig, req.Profile)
	if err != nil {
		return driver.Response{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if err := validateCursorSessionGuard(req, effectiveCWD, profileFingerprint); err != nil {
		return driver.Response{}, err
	}
	if req.Session != nil && req.Session.Mode == driver.SessionFork {
		// Cursor's public CLI currently exposes resume but no conversation-fork
		// primitive. Resuming here would append to the parent's provider chat and
		// violate Thread.Fork's parent-immutability contract, so fail before any
		// profile/resource materialization or subprocess launch.
		return driver.Response{}, &engine.ResumeRejectedError{Reason: "Cursor CLI does not support safe session fork"}
	}
	bindings, err = effectiveCursorBindings(cfg.CommonConfig, req.Profile)
	if err != nil {
		return driver.Response{}, err
	}
	effectiveEnv, err := driverutil.RuntimeEnvBindings(bindings, req.Runtime)
	if err != nil {
		return driver.Response{}, err
	}
	effectiveProfile, profileKind := cursorProfileAndKind(cfg.CommonConfig, req.Profile)
	if _, err := mcpruntime.SyncResource(ctx, DriverType, effectiveProfile.Dir, profileKind, req.MCP); err != nil {
		return driver.Response{}, err
	}
	if req.ProfilePayload.Declared.Config {
		if _, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Config); err != nil {
			return driver.Response{}, err
		}
	}
	var preparedInstructions profileinstructions.Prepared
	if req.ProfilePayload.Declared.Instructions {
		preparedInstructions, err = profileinstructions.PrepareForRun(ctx, DriverType, effectiveProfile.Dir, effectiveCWD, req.Instructions)
		if err != nil {
			return driver.Response{}, err
		}
	}
	if req.ProfilePayload.Declared.Agents {
		if _, err := profileagents.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Agents); err != nil {
			return driver.Response{}, err
		}
	}
	if req.ProfilePayload.Declared.Hooks {
		if _, err := profilehooks.Sync(ctx, DriverType, effectiveProfile.Dir, req.ProfilePayload.Hooks); err != nil {
			return driver.Response{}, err
		}
	}
	if _, err := syncCursorSkills(ctx, req.Skills, req.Skills.Keys(), nil, effectiveEnv, sink); err != nil {
		return driver.Response{}, err
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
	if req.Policy.HumanDecision.Permission == driver.HumanDecisionAutoApprove {
		args = append(args, "--force")
	}
	args = append(args, cursorSafeExtraArgs(cfg.ExtraArgs)...)

	prompt := req.Prompt
	if runtimePrefix := driverutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
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
		return driver.Response{}, err
	}
	parser.finalize()
	raw := driver.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr, Terminal: parser.terminal}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" &&
		result.ExitCode != 0 && isCursorUnknownSessionError(raw.Stdout, raw.Stderr) {
		return driver.Response{}, &engine.ResumeRejectedError{Reason: "Cursor resume session is unavailable"}
	}
	var failure *driver.RunFailure
	if strings.TrimSpace(parser.errorMessage) != "" {
		failure = &driver.RunFailure{
			Code:    driver.FailureAgentError,
			Message: parser.errorMessage,
		}
	} else if result.ExitCode == 0 && (parser.protocolMalformed || !parser.terminalSeen || !parser.terminalSuccess) {
		message := "Cursor stream-json protocol ended without a successful terminal result"
		if parser.protocolMalformed {
			message = "Cursor stream-json protocol was malformed"
		}
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: message}
	}
	checkpoint := parser.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: profileFingerprint,
		}
	}

	return driver.Response{
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
		RuntimeServices: driverutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:         failure,
	}, nil
}

// cursorSafeExtraArgs preserves provider-specific escape hatches while
// preventing construction-time arguments from replacing the invocation's
// resolved transport, workspace, model, mode, or Thread selector.
func cursorSafeExtraArgs(extra []string) []string {
	blockedValues := map[string]struct{}{
		"--output-format": {},
		"--workspace":     {},
		"--resume":        {},
		"--model":         {},
		"-m":              {},
		"--mode":          {},
	}
	blockedBooleans := map[string]struct{}{
		"--print": {}, "-p": {},
		// Permission policy is resolved per call. Constructor ExtraArgs must
		// never enable Cursor's force-approval mode behind that policy.
		"--force": {}, "-f": {}, "--yolo": {},
	}
	out := make([]string, 0, len(extra))
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		base := arg
		if eq := strings.IndexByte(arg, '='); eq >= 0 {
			base = arg[:eq]
		}
		if _, blocked := blockedBooleans[base]; blocked {
			continue
		}
		if _, blocked := blockedValues[base]; blocked {
			if !strings.Contains(arg, "=") && i+1 < len(extra) && !strings.HasPrefix(extra[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out
}

var cursorUnknownSessionErrorRE = regexp.MustCompile(`(?i)(session|chat).*(not found|unknown|does not exist|invalid|expired)|unable to (resume|find).*(session|chat)`)

func isCursorUnknownSessionError(stdout, stderr string) bool {
	return cursorUnknownSessionErrorRE.MatchString(stdout + "\n" + stderr)
}

func validateCursorSessionGuard(req driver.Request, effectiveCWD, profileFingerprint string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	if req.Session.State.Data[driver.SessionParamCWD] != "" && req.Session.State.Data[driver.SessionParamCWD] != effectiveCWD {
		return &engine.ResumeRejectedError{Reason: "session working directory changed"}
	}
	if req.Session.State.Data[driver.SessionParamWorkspaceID] != "" && req.Session.State.Data[driver.SessionParamWorkspaceID] != req.Workspace.ID {
		return &engine.ResumeRejectedError{Reason: "session workspace changed"}
	}
	if req.Session.State.Data[driver.SessionParamProfileFingerprint] != "" && req.Session.State.Data[driver.SessionParamProfileFingerprint] != profileFingerprint {
		return &engine.ResumeRejectedError{Reason: "profile resources changed"}
	}
	return nil
}

func chooseCWD(cfg driver.CommonConfig, workspace driver.WorkspaceLease) string {
	if workspace.CWD != "" {
		return workspace.CWD
	}
	return cfg.CWD
}

// readConfig snapshots the package-owned configuration used by the configured driver.
func readConfig(cfg any) Config {
	switch typed := cfg.(type) {
	case Config:
		return cloneConfig(typed)
	case *Config:
		if typed != nil {
			return cloneConfig(*typed)
		}
	}
	return Config{}
}
