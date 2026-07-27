package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
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

// DriverType is the stable descriptor type for the built-in Codex adapter.
const DriverType = "codex"

type adapter struct{}

// New returns a configured Codex AgentBinding. Hosts should pass the result to
// agentadaptor.WithDefaultAgent or agentadaptor.WithAgent; direct adapter use
// is reserved for lower-level tests and custom plumbing.
func New(cfg agentadaptor.CodexConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.CodexConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

// NewAdapter returns the low-level Codex DriverAdapter. Most hosts should use
// New so config and binding defaults travel together.
func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	fields := []agentadaptor.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the Codex CLI executable.", Hint: "Defaults to `codex` when unset.", Default: "codex", Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "Codex model identifier, for example gpt-5.4.", Default: "gpt-5.4", Options: modelOptions(codexModels()), Group: "model"},
		{Name: "reasoning_effort", Label: "Reasoning Effort", Type: "select", Description: "Optional reasoning effort passed through model_reasoning_effort.", Hint: "Only set this when you want to override Codex defaults.", Options: []agentadaptor.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "Extra High"}}, Group: "model"},
		{Name: "fast_mode", Label: "Fast Mode", Type: "toggle", Description: "Enable Codex fast service tier defaults.", Default: false, Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return agentadaptor.DriverDescriptor{
		Type:         DriverType,
		DisplayName:  "Codex",
		Models:       codexModels(),
		ConfigSchema: &agentadaptor.ConfigSchema{Fields: fields},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncPersistent},
		MCP:          agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Isolation: true, WebSearch: true, Browser: false,
			Permission: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			Question:   agentadaptor.QuestionSupport{Ask: false, AutoReject: false, Retry: false},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
		StructuredOutput: agentadaptor.StructuredOutputCapability{
			JSONSchemaNative:         true,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStreaming:       false,
			WorksWithHITL:            false,
			Notes:                    "Native JSON Schema output is supported by codex exec --output-schema; codex app-server streaming support is not advertised.",
		},
	}
}

func codexModels() []agentadaptor.ModelInfo {
	return []agentadaptor.ModelInfo{
		{ID: "gpt-5.4", Label: "gpt-5.4"},
		{ID: "gpt-5.3-codex", Label: "gpt-5.3-codex"},
		{ID: "gpt-5.3-codex-spark", Label: "gpt-5.3-codex-spark"},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case agentadaptor.CodexConfig, *agentadaptor.CodexConfig, Config, *Config:
		return nil
	default:
		return errors.New("codex driver requires agentadaptor.CodexConfig")
	}
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (agentadaptor.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = "codex"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveCodexBindings(config.CommonConfig, nil, agentadaptor.AgentIdentity{})
	if err != nil {
		checks = append(checks, agentadaptor.EnvironmentCheck{Code: "codex_profile_error", Level: "fail", Message: "Codex profile resolution failed.", Detail: err.Error()})
		return adapterutil.SummarizeEnvironment(DriverType, checks), nil
	}
	checks = append(checks, codexAuthChecks(bindings)...)
	checks = append(checks, codexConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return adapter{}.Descriptor().Models, nil
}

func (adapter) DetectModel(_ context.Context, cfg any, profile *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agentadaptor.AgentIdentity{})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Model) == "" {
		for _, candidate := range codexConfigCandidates(bindings) {
			if model, ok, err := readCodexConfiguredModel(candidate); err == nil && ok {
				return &agentadaptor.DetectedModel{
					Model:      model,
					Provider:   "openai",
					Source:     "config_file",
					Candidates: []string{model},
				}, nil
			}
		}
		return nil, nil
	}
	return &agentadaptor.DetectedModel{
		Model:      config.Model,
		Provider:   "openai",
		Source:     "binding_config",
		Candidates: []string{config.Model},
	}, nil
}

func (adapter) GetProfile(_ context.Context, cfg any, agent agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	return codexProfile(readConfig(cfg).CommonConfig, profile, agent), nil
}

func codexConfigCandidates(bindings []agentadaptor.EnvBinding) []string {
	root := resolveSharedCodexHome(bindings)
	return []string{
		filepath.Join(root, "config.toml"),
		filepath.Join(root, "config.json"),
	}
}

func readCodexConfiguredModel(path string) (string, bool, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return configprobe.ReadSimpleTOMLString(path, "model")
	case ".json":
		return configprobe.ReadTopLevelJSONString(path, "model")
	default:
		return "", false, nil
	}
}

func codexConfigChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	checks := make([]agentadaptor.EnvironmentCheck, 0)
	for _, candidate := range codexConfigCandidates(bindings) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "codex_config_present",
			Level:   "info",
			Message: "Codex config file is present.",
			Detail:  candidate,
		})
		if model, ok, err := readCodexConfiguredModel(candidate); err == nil && ok {
			checks = append(checks, agentadaptor.EnvironmentCheck{
				Code:    "codex_config_model",
				Level:   "info",
				Message: "Codex config file declares a default model.",
				Detail:  model,
			})
		}
		if strings.HasSuffix(strings.ToLower(candidate), ".toml") {
			if provider, ok, err := configprobe.ReadSimpleTOMLString(candidate, "model_provider"); err == nil && ok {
				checks = append(checks, agentadaptor.EnvironmentCheck{
					Code:    "codex_config_provider",
					Level:   "info",
					Message: "Codex config file declares a model provider.",
					Detail:  provider,
				})
			}
		}
		break
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

func (adapter) GetQuota(ctx context.Context, cfg any, profile *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agentadaptor.AgentIdentity{})
	if err != nil {
		return agentadaptor.QuotaReport{}, err
	}
	return codexQuotaReport(ctx, bindings)
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agentadaptor.AgentIdentity{})
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return listCodexSkills(payload, selected, resolved, codexHomeFromBindings(bindings))
}

// InjectSkills is a no-op for Codex. Injection requires the resolved
// CODEX_HOME which is only known once Run() produces the effective
// bindings for this particular invocation (profile-aware). Run() performs
// the filesystem work itself; implementing the interface here simply
// keeps the adapter spec explicit.
func (adapter) InjectSkills(_ context.Context, _ any, _ agentadaptor.ResolvedSkills, _ *agentadaptor.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agentadaptor.AgentIdentity{})
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return syncCodexSkills(ctx, payload, selected, resolved, codexHomeFromBindings(bindings), nil)
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, agent agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection, payload agentadaptor.ProfilePayload, selected []string, resolved []agentadaptor.Skill) (agentadaptor.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agent)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	skills, err := listCodexSkills(payload.Skills, selected, resolved, codexHomeFromBindings(bindings))
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := codexProfileAndKind(config.CommonConfig, profile, agent)
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, false)
	mcpSnapshot, err := mcpruntime.SnapshotResource(DriverType, codexHomeFromBindings(bindings), payload.MCP, false)
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

func (adapter) SyncProfileResources(ctx context.Context, cfg any, agent agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection, payload agentadaptor.ProfilePayload, selected []string, resolved []agentadaptor.Skill) (agentadaptor.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveCodexBindings(config.CommonConfig, profile, agent)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	codexHome := codexHomeFromBindings(bindings)
	skills, err := syncCodexSkills(ctx, payload.Skills, selected, resolved, codexHome, nil)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := codexProfileAndKind(config.CommonConfig, profile, agent)
	mcpSnapshot, err := mcpruntime.SyncResource(ctx, DriverType, codexHome, kind, payload.MCP)
	if err != nil {
		return agentadaptor.ProfileSnapshot{}, err
	}
	snapshot := profilesnapshot.Build(DriverType, effectiveProfile, kind, payload, skills, true)
	snapshot = profileconfig.WithSnapshotResource(snapshot, mcpSnapshot)
	if payload.Declared.Config {
		configSnapshot, err := profileconfig.SyncNativePatches(ctx, DriverType, codexHome, payload.Config)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, configSnapshot)
	}
	if payload.Declared.Instructions {
		instructionsSnapshot, _, err := profileinstructions.Sync(ctx, DriverType, codexHome, payload.Instructions)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, instructionsSnapshot)
	}
	if payload.Declared.Agents {
		agentsSnapshot, err := profileagents.Sync(ctx, DriverType, codexHome, payload.Agents)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, agentsSnapshot)
	}
	if payload.Declared.Hooks {
		hooksSnapshot, err := profilehooks.Sync(ctx, DriverType, codexHome, payload.Hooks)
		if err != nil {
			return agentadaptor.ProfileSnapshot{}, err
		}
		snapshot = profileconfig.WithSnapshotResource(snapshot, hooksSnapshot)
	}
	return snapshot, nil
}

// StreamCapability advertises the codex adapter's streaming fidelity. When
// the caller opts into streaming, codex switches from `codex exec --json` to
// `codex app-server`, which natively emits token-level deltas, reasoning
// deltas, and tool-call lifecycle events.
func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         false, // app-server HITL requests are not yet wired into the SDK decision path.
	}
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	// per-run WithModel overrides the binding model for this invocation only.
	if m := strings.TrimSpace(req.ModelOverride); m != "" {
		cfg.Model = m
	}
	command := cfg.Command
	if command == "" {
		command = "codex"
	}
	profileFingerprint := req.ProfilePayload.Fingerprint
	effectiveBindings, err := effectiveCodexBindingsNoInitialize(cfg.CommonConfig, req.Profile, req.Agent)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCodexHome := codexHomeFromBindings(effectiveBindings)
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if err := validateCodexSessionGuard(req, effectiveCWD, profileFingerprint); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	// Streaming runs switch the transport to `codex app-server`. The
	// exec --json path remains the default for batch / scripted usage; see
	// docs/workstream-streaming-chat.md §§5–8.
	effectiveBindings, err = effectiveCodexBindings(cfg.CommonConfig, req.Profile, req.Agent)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCodexHome = codexHomeFromBindings(effectiveBindings)
	_, profileKind := codexProfileAndKind(cfg.CommonConfig, req.Profile, req.Agent)
	if err := injectCodexSkills(ctx, req.Skills, effectiveCodexHome, sink); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if _, err := mcpruntime.SyncResource(ctx, DriverType, effectiveCodexHome, profileKind, req.MCP); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if req.ProfilePayload.Declared.Config {
		if _, err := profileconfig.SyncNativePatches(ctx, DriverType, effectiveCodexHome, req.ProfilePayload.Config); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	var preparedInstructions profileinstructions.Prepared
	if req.ProfilePayload.Declared.Instructions {
		preparedInstructions, err = profileinstructions.PrepareForRun(ctx, DriverType, effectiveCodexHome, effectiveCWD, req.Instructions)
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	if req.ProfilePayload.Declared.Agents {
		if _, err := profileagents.Sync(ctx, DriverType, effectiveCodexHome, req.ProfilePayload.Agents); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	if req.ProfilePayload.Declared.Hooks {
		if _, err := profilehooks.Sync(ctx, DriverType, effectiveCodexHome, req.ProfilePayload.Hooks); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}
	effectiveBindings, err = adapterutil.RuntimeEnvBindings(effectiveBindings, req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if req.Streaming {
		return runAppServer(ctx, req, sink, cfg, command, effectiveBindings, preparedInstructions)
	}

	args := []string{"exec", "--json"}
	var schemaTempDir string
	if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
		if hasAnyArg(cfg.ExtraArgs, "--output-schema") {
			return agentadaptor.DriverRunResult{}, &agentadaptor.InvalidOutputSchemaError{Reason: "Codex ExtraArgs must not include --output-schema when SDK structured output is enabled"}
		}
		schemaTempDir, err = os.MkdirTemp("", "agentadaptor-codex-output-schema-*")
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
		defer os.RemoveAll(schemaTempDir)
		schemaPath := filepath.Join(schemaTempDir, "schema.json")
		if err := os.WriteFile(schemaPath, req.OutputSchema.SchemaJSON, 0o600); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
		args = append(args, "--output-schema", schemaPath)
	}
	if req.Policy.WebSearch == agentadaptor.FeatureAllow {
		args = append([]string{"--search"}, args...)
	}
	if req.Policy.Isolation == agentadaptor.IsolationUnrestricted {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+string(cfg.ReasoningEffort))
	}
	if cfg.FastMode {
		args = append(args, "-c", `service_tier="fast"`, "-c", "features.fast_mode=true")
	}
	args = append(args, cfg.ExtraArgs...)
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "resume", req.Session.State.ResumeID, "-")
	} else {
		args = append(args, "-")
	}

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}

	parser := newCodexParser(sink)
	result, err := clihelper.Run(ctx, clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     effectiveCWD,
		Env:     effectiveBindings,
		Prompt:  prompt,
		Observe: parser.onChunk,
	}, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	parser.finalize()
	raw := agentadaptor.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" &&
		result.ExitCode != 0 && isCodexUnknownSessionError(raw.Stdout, raw.Stderr) {
		reason := parser.errorMessage
		if strings.TrimSpace(reason) == "" {
			reason = fmt.Sprintf("codex resume session %q is unavailable", req.Session.State.ResumeID)
		}
		return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: reason}
	}
	provider := ""
	if cfg.Model != "" {
		provider = "openai"
	}
	var failure *agentadaptor.RunFailure
	if strings.TrimSpace(parser.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureAgentError,
			Message: parser.errorMessage,
		}
	}
	checkpoint := parser.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: profileFingerprint,
		}
	}
	var structuredOutput *agentadaptor.StructuredOutput
	if req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate {
		structuredOutput = parser.structuredOutput
	}

	return agentadaptor.DriverRunResult{
		Output:           parser.buildOutput(),
		RawStreams:       &raw,
		Transcript:       parser.transcript,
		ExitCode:         result.ExitCode,
		Signal:           result.Signal,
		TimedOut:         result.TimedOut,
		Usage:            parser.usage,
		Checkpoint:       checkpoint,
		Provider:         provider,
		Model:            cfg.Model,
		Summary:          parser.finalSummary(),
		Result:           parser.resultFinal,
		StructuredOutput: structuredOutput,
		RuntimeServices:  adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:          failure,
	}, nil
}

func hasAnyArg(args []string, names ...string) bool {
	for _, arg := range args {
		for _, name := range names {
			if arg == name || strings.HasPrefix(arg, name+"=") {
				return true
			}
		}
	}
	return false
}

func validateCodexSessionGuard(req agentadaptor.DriverRunRequest, effectiveCWD, profileFingerprint string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	if req.Session.State.Data[driver.SessionParamCWD] != "" && req.Session.State.Data[driver.SessionParamCWD] != effectiveCWD {
		return &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
	}
	if req.Session.State.Data[driver.SessionParamWorkspaceID] != "" && req.Session.State.Data[driver.SessionParamWorkspaceID] != req.Workspace.ID {
		return &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
	}
	if req.Session.State.Data[driver.SessionParamProfileFingerprint] != "" && req.Session.State.Data[driver.SessionParamProfileFingerprint] != profileFingerprint {
		return &agentadaptor.ResumeRejectedError{Reason: "profile resources changed"}
	}
	return nil
}

func codexHomeFromBindings(bindings []agentadaptor.EnvBinding) string {
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), "CODEX_HOME") {
			return strings.TrimSpace(binding.Value)
		}
	}
	return ""
}

func chooseCWD(cfg agentadaptor.CommonConfig, workspace agentadaptor.WorkspaceLease) string {
	if workspace.CWD != "" {
		return workspace.CWD
	}
	return cfg.CWD
}

// readConfig normalizes every config shape the adapter accepts. The v1
// entry point (Driver) hands over the package-owned Config, so this is the
// single point where it is converted for the shared adapter path.
func readConfig(cfg any) agentadaptor.CodexConfig {
	switch typed := cfg.(type) {
	case agentadaptor.CodexConfig:
		return typed
	case *agentadaptor.CodexConfig:
		if typed != nil {
			return *typed
		}
	case Config:
		return typed.engineConfig()
	case *Config:
		if typed != nil {
			return typed.engineConfig()
		}
	}
	return agentadaptor.CodexConfig{}
}

var codexUnknownSessionErrorRE = regexp.MustCompile(`(?i)unknown (session|thread)|session .* not found|thread .* not found|conversation .* not found|missing rollout path for thread|state db missing rollout path|no rollout found for thread id`)

func isCodexUnknownSessionError(stdout, stderr string) bool {
	haystack := strings.Join(filterNonEmptyLines(stdout, stderr), "\n")
	return codexUnknownSessionErrorRE.MatchString(haystack)
}

func filterNonEmptyLines(parts ...string) []string {
	lines := make([]string, 0)
	for _, part := range parts {
		for _, line := range strings.Split(part, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
	}
	return lines
}
