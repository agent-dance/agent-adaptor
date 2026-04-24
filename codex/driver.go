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
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
)

const DriverType = "codex"

type adapter struct{}

func New(cfg agentadaptor.CodexConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.CodexConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        DriverType,
		DisplayName: "Codex",
		Models:      codexModels(),
		ConfigSchema: &agentadaptor.ConfigSchema{
			Fields: []agentadaptor.ConfigField{
				{Name: "command", Label: "Command", Type: "text", Description: "Override the Codex CLI executable.", Hint: "Defaults to `codex` when unset.", Default: "codex", Group: "command"},
				{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
				{Name: "model", Label: "Model", Type: "select", Description: "Codex model identifier, for example gpt-5.4.", Default: "gpt-5.4", Options: modelOptions(codexModels()), Group: "model"},
				{Name: "reasoning_effort", Label: "Reasoning Effort", Type: "select", Description: "Optional reasoning effort passed through model_reasoning_effort.", Hint: "Only set this when you want to override Codex defaults.", Options: []agentadaptor.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "Extra High"}}, Group: "model"},
				{Name: "fast_mode", Label: "Fast Mode", Type: "toggle", Description: "Enable Codex fast service tier defaults.", Default: false, Group: "execution"},
				{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
			},
		},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
		MCP:          agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Isolation: true, WebSearch: true, Browser: false,
			Permission: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: false, Retry: false},
			Question:   agentadaptor.QuestionSupport{Ask: false, AutoReject: false, Retry: false},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
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
	case agentadaptor.CodexConfig, *agentadaptor.CodexConfig:
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

func (adapter) ListSkills(_ context.Context, _ any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, _ *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	return listCodexSkills(payload, selected, resolved), nil
}

// InjectSkills is a no-op for Codex. Injection requires the resolved
// CODEX_HOME which is only known once Run() produces the effective
// bindings for this particular invocation (profile-aware). Run() performs
// the filesystem work itself; implementing the interface here simply
// keeps the adapter spec explicit.
func (adapter) InjectSkills(_ context.Context, _ any, _ agentadaptor.ResolvedSkills, _ *agentadaptor.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(_ context.Context, _ any, payload agentadaptor.ResolvedSkills, selected []string, resolved []agentadaptor.Skill, _ *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	return syncCodexSkills(payload, selected, resolved), nil
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
		HITL:         false, // HITL is audit-only in v1; see workstream doc §15.
	}
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	command := cfg.Command
	if command == "" {
		command = "codex"
	}
	effectiveBindings, err := effectiveCodexBindings(cfg.CommonConfig, req.Profile, req.Agent)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCodexHome := ""
	for _, binding := range effectiveBindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), "CODEX_HOME") {
			effectiveCodexHome = strings.TrimSpace(binding.Value)
			break
		}
	}
	if err := injectCodexSkills(ctx, req.Skills, effectiveCodexHome, sink); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if err := syncCodexMCPProfile(cfg.CommonConfig, req.Profile, req.Agent, effectiveCodexHome, req.MCP); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveBindings, err = adapterutil.RuntimeEnvBindings(effectiveBindings, req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	// Streaming runs switch the transport to `codex app-server`. The
	// exec --json path remains the default for batch / scripted usage; see
	// docs/workstream-streaming-chat.md §§5–8.
	if req.Streaming {
		return runAppServer(ctx, req, sink, cfg, command, effectiveBindings)
	}

	args := []string{"exec", "--json"}
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
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if req.Session != nil && req.Session.State != nil {
		if req.Session.State.Data[agentadaptor.SessionParamCWD] != "" && req.Session.State.Data[agentadaptor.SessionParamCWD] != effectiveCWD {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
		}
		if req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != "" && req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != req.Workspace.ID {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
		}
	}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "resume", req.Session.State.ResumeID, "-")
	} else {
		args = append(args, "-")
	}

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if req.Instructions != nil && req.Instructions.Path != "" {
		prompt = "Instructions bundle: " + req.Instructions.Path + "\n\n" + prompt
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
	checkpoint := parser.checkpoint(result.ExitCode)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			agentadaptor.SessionParamCWD:         effectiveCWD,
			agentadaptor.SessionParamWorkspaceID: req.Workspace.ID,
		}
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

	return agentadaptor.DriverRunResult{
		Output:          parser.buildOutput(),
		RawStreams:      &raw,
		Transcript:      parser.transcript,
		ExitCode:        result.ExitCode,
		Signal:          result.Signal,
		TimedOut:        result.TimedOut,
		Usage:           parser.usage,
		Checkpoint:      checkpoint,
		Provider:        provider,
		Model:           cfg.Model,
		Summary:         parser.finalSummary(),
		Result:          parser.resultFinal,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:         failure,
	}, nil
}

func chooseCWD(cfg agentadaptor.CommonConfig, workspace agentadaptor.WorkspaceLease) string {
	if workspace.CWD != "" {
		return workspace.CWD
	}
	return cfg.CWD
}

func readConfig(cfg any) agentadaptor.CodexConfig {
	switch typed := cfg.(type) {
	case agentadaptor.CodexConfig:
		return typed
	case *agentadaptor.CodexConfig:
		if typed != nil {
			return *typed
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
