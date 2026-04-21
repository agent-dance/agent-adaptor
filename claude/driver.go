package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

const DriverType = "claude"

type adapter struct{}

func New(cfg agentadaptor.ClaudeConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.ClaudeConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        DriverType,
		DisplayName: "Claude Code",
		Models:      claudeModels(),
		ConfigSchema: &agentadaptor.ConfigSchema{
			Fields: []agentadaptor.ConfigField{
				{Name: "command", Label: "Command", Type: "text", Description: "Override the Claude CLI executable.", Hint: "Defaults to `claude` when unset.", Default: "claude", Group: "command"},
				{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
				{Name: "agent_profile_dir", Label: "Agent Profile Dir", Type: "text", Description: "Stable local Claude profile directory used when CLAUDE_CONFIG_DIR is not already set in CommonConfig.Env.", Hint: "Maps to CLAUDE_CONFIG_DIR. Explicit CommonConfig.Env CLAUDE_CONFIG_DIR still wins.", Group: "profile"},
				{Name: "model", Label: "Model", Type: "select", Description: "Claude model identifier, for example claude-sonnet-4 or a Bedrock-native us.anthropic.* id.", Default: defaultClaudeModel, Options: modelOptions(claudeModels()), Group: "model"},
				{Name: "effort", Label: "Thinking Effort", Type: "select", Description: "Optional Claude thinking effort.", Options: []agentadaptor.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}}, Group: "model"},
				{Name: "chrome", Label: "Chrome Tools", Type: "toggle", Description: "Enable Chrome/browser tools by default.", Default: false, Group: "permissions"},
				{Name: "skip_permissions", Label: "Skip Permissions", Type: "toggle", Description: "Skip Claude permission prompts.", Hint: "Use only in trusted local environments.", Default: false, Group: "permissions", Meta: map[string]string{"risk": "high"}},
				{Name: "max_turns_per_run", Label: "Max Turns", Type: "number", Description: "Optional max-turns guard for one run.", Group: "execution"},
				{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
			},
		},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		Permissions:  agentadaptor.InvocationPermissionCapability{Approvals: true, Browser: true},
		Runtime:      agentadaptor.RuntimeCapability{ReportsServices: true},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case agentadaptor.ClaudeConfig, *agentadaptor.ClaudeConfig:
		return nil
	default:
		return errors.New("claude driver requires agentadaptor.ClaudeConfig")
	}
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (agentadaptor.EnvironmentReport, error) {
	config := readConfig(cfg)
	bindings := effectiveClaudeBindings(config.CommonConfig)
	command := config.Command
	if command == "" {
		command = "claude"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	checks = append(checks, claudeAuthChecks(bindings)...)
	checks = append(checks, claudeModelCompatibilityChecks(config)...)
	checks = append(checks, claudeConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, cfg any) ([]agentadaptor.ModelInfo, error) {
	config := readConfig(cfg)
	return claudeModelsForBindings(effectiveClaudeBindings(config.CommonConfig)), nil
}

func (adapter) DetectModel(_ context.Context, cfg any) (*agentadaptor.DetectedModel, error) {
	return detectClaudeEffectiveModel(readConfig(cfg))
}

func (adapter) GetProfile(_ context.Context, cfg any, _ agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
	return claudeProfile(readConfig(cfg).CommonConfig), nil
}

func claudeConfigCandidates(bindings []agentadaptor.EnvBinding) []string {
	home := skillruntime.ResolveHome(bindings)
	configDir := resolveClaudeConfigDir(bindings)
	candidates := []string{
		filepath.Join(configDir, "settings.json"),
		filepath.Join(configDir, "config.json"),
	}
	if skillruntime.ResolveBinding(bindings, "CLAUDE_CONFIG_DIR") == "" && strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")) == "" {
		candidates = append(candidates, filepath.Join(home, ".claude.json"))
	}
	return candidates
}

func claudeConfigChecks(bindings []agentadaptor.EnvBinding) []agentadaptor.EnvironmentCheck {
	checks := make([]agentadaptor.EnvironmentCheck, 0)
	for _, candidate := range claudeConfigCandidates(bindings) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		checks = append(checks, agentadaptor.EnvironmentCheck{
			Code:    "claude_config_present",
			Level:   "info",
			Message: "Claude config file is present.",
			Detail:  candidate,
		})
		if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
			checks = append(checks, agentadaptor.EnvironmentCheck{
				Code:    "claude_config_model",
				Level:   "info",
				Message: "Claude config file declares a default model.",
				Detail:  model,
			})
		}
		if baseURL, ok, err := configprobe.ReadNestedJSONString(candidate, "env", "ANTHROPIC_BASE_URL"); err == nil && ok {
			checks = append(checks, agentadaptor.EnvironmentCheck{
				Code:    "claude_config_base_url",
				Level:   "info",
				Message: "Claude config file overrides the Anthropic base URL.",
				Detail:  baseURL,
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

func (adapter) ConfigSchema(_ context.Context, cfg any) (*agentadaptor.ConfigSchema, error) {
	return hydrateClaudeConfigSchema(readConfig(cfg)), nil
}

func (adapter) GetQuota(ctx context.Context, cfg any) (agentadaptor.QuotaReport, error) {
	config := readConfig(cfg)
	return claudeQuotaReport(ctx, effectiveClaudeBindings(config.CommonConfig))
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.SkillPayload) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	return listClaudeSkills(payload, effectiveClaudeBindings(config.CommonConfig))
}

func (adapter) SyncSkills(_ context.Context, cfg any, payload agentadaptor.SkillPayload, _ []string) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	return syncClaudeSkills(payload, effectiveClaudeBindings(config.CommonConfig))
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	command := cfg.Command
	if command == "" {
		command = "claude"
	}
	bundleRoot, bundleKey, err := prepareClaudePromptBundle(req.Agent, req.Skills)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveEnv, err := adapterutil.RuntimeEnvBindings(effectiveClaudeBindings(cfg.CommonConfig), req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if req.Session != nil && req.Session.State != nil {
		if req.Session.State.Data[agentadaptor.SessionParamCWD] != "" && req.Session.State.Data[agentadaptor.SessionParamCWD] != effectiveCWD {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
		}
		if req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != "" && req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != req.Workspace.ID {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
		}
		if req.Session.State.Data[agentadaptor.SessionParamPromptBundleKey] != "" && req.Session.State.Data[agentadaptor.SessionParamPromptBundleKey] != bundleKey {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "prompt bundle changed"}
		}
	}

	args := []string{"--print", "-", "--output-format", "stream-json", "--verbose"}
	modelFlag := claudeRequestedModelFlag(cfg)
	reportedModel := modelFlag
	if reportedModel == "" {
		if detected, err := detectClaudeEffectiveModel(cfg); err == nil && detected != nil {
			reportedModel = detected.Model
		}
	}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if cfg.SkipPermissions || req.Permissions.ApprovalMode == agentadaptor.ApprovalNever {
		args = append(args, "--dangerously-skip-permissions")
	}
	if cfg.Chrome || req.Permissions.BrowserMode == agentadaptor.FeatureAllow {
		args = append(args, "--chrome")
	}
	if modelFlag != "" {
		args = append(args, "--model", modelFlag)
	}
	if cfg.Effort != "" {
		args = append(args, "--effort", string(cfg.Effort))
	}
	if cfg.MaxTurnsPerRun > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxTurnsPerRun))
	}
	if bundleRoot != "" {
		args = append(args, "--add-dir", bundleRoot)
	}
	args = append(args, cfg.ExtraArgs...)

	prompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		prompt = runtimePrefix + "\n\n" + prompt
	}
	if req.Instructions != nil && req.Instructions.Path != "" {
		prompt = "Instructions bundle: " + req.Instructions.Path + "\n\n" + prompt
	}

	parser := newClaudeParser(sink)
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
			agentadaptor.SessionParamCWD:             effectiveCWD,
			agentadaptor.SessionParamWorkspaceID:     req.Workspace.ID,
			agentadaptor.SessionParamPromptBundleKey: bundleKey,
		}
	}
	var failure *agentadaptor.RunFailure
	if strings.TrimSpace(parser.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{Message: parser.errorMessage}
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
		Provider:        "anthropic",
		Model:           reportedModel,
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

func readConfig(cfg any) agentadaptor.ClaudeConfig {
	switch typed := cfg.(type) {
	case agentadaptor.ClaudeConfig:
		return typed
	case *agentadaptor.ClaudeConfig:
		if typed != nil {
			return *typed
		}
	}
	return agentadaptor.ClaudeConfig{}
}

