package codex

import (
	"context"
	"encoding/json"
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
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
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
				{Name: "agent_profile_dir", Label: "Agent Profile Dir", Type: "text", Description: "Stable local Codex profile directory used when CODEX_HOME is not already set in CommonConfig.Env.", Hint: "Maps to CODEX_HOME. Explicit CommonConfig.Env CODEX_HOME still wins.", Group: "profile"},
				{Name: "model", Label: "Model", Type: "select", Description: "Codex model identifier, for example gpt-5.4.", Default: "gpt-5.4", Options: modelOptions(codexModels()), Group: "model"},
				{Name: "reasoning_effort", Label: "Reasoning Effort", Type: "select", Description: "Optional reasoning effort passed through model_reasoning_effort.", Hint: "Only set this when you want to override Codex defaults.", Options: []agentadaptor.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}, {Value: "xhigh", Label: "Extra High"}}, Group: "model"},
				{Name: "search", Label: "Search", Type: "toggle", Description: "Enable Codex search support by default.", Default: false, Group: "permissions"},
				{Name: "fast_mode", Label: "Fast Mode", Type: "toggle", Description: "Enable Codex fast service tier defaults.", Default: false, Group: "execution"},
				{Name: "bypass_approvals_and_sandbox", Label: "Bypass Approvals", Type: "toggle", Description: "Disable approvals and sandboxing.", Hint: "Use only in trusted local environments.", Default: false, Group: "permissions", Meta: map[string]string{"risk": "high"}},
				{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
			},
		},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		Permissions:  agentadaptor.InvocationPermissionCapability{Approvals: true, Sandbox: true, Search: true},
		Runtime:      agentadaptor.RuntimeCapability{ReportsServices: true},
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
	bindings := effectiveCodexBindings(config.CommonConfig)
	command := config.Command
	if command == "" {
		command = "codex"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	checks = append(checks, codexAuthChecks(bindings)...)
	checks = append(checks, codexConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return adapter{}.Descriptor().Models, nil
}

func (adapter) DetectModel(_ context.Context, cfg any) (*agentadaptor.DetectedModel, error) {
	config := readConfig(cfg)
	bindings := effectiveCodexBindings(config.CommonConfig)
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

func (adapter) GetProfile(_ context.Context, cfg any, agent agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
	return codexProfile(readConfig(cfg).CommonConfig, agent), nil
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

func (adapter) GetQuota(ctx context.Context, cfg any) (agentadaptor.QuotaReport, error) {
	config := readConfig(cfg)
	return codexQuotaReport(ctx, effectiveCodexBindings(config.CommonConfig))
}

func (adapter) ListSkills(_ context.Context, _ any, payload agentadaptor.SkillPayload) (agentadaptor.SkillSnapshot, error) {
	return listCodexSkills(payload), nil
}

func (adapter) SyncSkills(_ context.Context, _ any, payload agentadaptor.SkillPayload, _ []string) (agentadaptor.SkillSnapshot, error) {
	return syncCodexSkills(payload), nil
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	command := cfg.Command
	if command == "" {
		command = "codex"
	}
	effectiveBindings := effectiveCodexBindings(cfg.CommonConfig)
	effectiveCodexHome := skillruntime.ResolveBinding(effectiveBindings, "CODEX_HOME")
	if strings.TrimSpace(effectiveCodexHome) == "" {
		managedHome, err := prepareManagedCodexHome(effectiveBindings, req.Agent)
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
		effectiveCodexHome = managedHome
		effectiveBindings = skillruntime.WithBinding(effectiveBindings, "CODEX_HOME", effectiveCodexHome)
	}
	if err := injectCodexSkills(ctx, req.Skills, effectiveCodexHome, sink); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveBindings, err := adapterutil.RuntimeEnvBindings(effectiveBindings, req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}

	args := []string{"exec", "--json"}
	if cfg.Search || req.Permissions.SearchMode == agentadaptor.FeatureAllow {
		args = append([]string{"--search"}, args...)
	}
	if cfg.BypassApprovalsAndSandbox || req.Permissions.SandboxMode == agentadaptor.SandboxDisabled {
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

	result, err := clihelper.Run(ctx, clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     effectiveCWD,
		Env:     effectiveBindings,
		Prompt:  prompt,
	}, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	parsed := parseCodexJSONL(result.Stdout)
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" &&
		result.ExitCode != 0 && isCodexUnknownSessionError(result.Stdout, result.Stderr) {
		reason := parsed.errorMessage
		if strings.TrimSpace(reason) == "" {
			reason = fmt.Sprintf("codex resume session %q is unavailable", req.Session.State.ResumeID)
		}
		return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: reason}
	}
	checkpoint := checkpointFromParsedCodexRun(parsed, result.ExitCode)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			agentadaptor.SessionParamCWD:         effectiveCWD,
			agentadaptor.SessionParamWorkspaceID: req.Workspace.ID,
		}
	}
	var usage *agentadaptor.Usage
	if parsed.hasUsage {
		usage = &agentadaptor.Usage{
			InputTokens:       parsed.usage.InputTokens,
			OutputTokens:      parsed.usage.OutputTokens,
			CachedInputTokens: parsed.usage.CachedInputTokens,
		}
	}
	provider := ""
	if cfg.Model != "" {
		provider = "openai"
	}

	return agentadaptor.DriverRunResult{
		Output:          result.Stdout,
		Transcript:      adapterutil.TranscriptFromOutput(result.Stdout, result.Stderr, "", nil, nil),
		ExitCode:        result.ExitCode,
		Usage:           usage,
		Checkpoint:      checkpoint,
		Metadata:        map[string]string{"stderr": result.Stderr},
		Provider:        provider,
		Model:           cfg.Model,
		Summary:         parsed.summary,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
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

func topLevelString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type codexParsedRun struct {
	sessionID    string
	displayID    string
	summary      string
	usage        agentadaptor.Usage
	hasUsage     bool
	errorMessage string
}

func parseCheckpoint(stdout string, exitCode int) *agentadaptor.DriverCheckpoint {
	return checkpointFromParsedCodexRun(parseCodexJSONL(stdout), exitCode)
}

func checkpointFromParsedCodexRun(parsed codexParsedRun, exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 || parsed.sessionID == "" {
		return nil
	}

	displayID := parsed.displayID
	if displayID == "" {
		displayID = parsed.sessionID
	}
	return &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  parsed.sessionID,
			DisplayID: displayID,
		},
		Valid: true,
	}
}

func parseCodexJSONL(stdout string) codexParsedRun {
	var parsed codexParsedRun
	for _, rawLine := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		payload, ok := parseCodexJSONLine(line)
		if !ok {
			continue
		}
		switch checkpointEventKind(payload) {
		case "thread.started":
			if threadID := topLevelString(payload, "thread_id", "threadId"); threadID != "" {
				parsed.sessionID = threadID
				parsed.displayID = threadID
			}
		case "item.completed":
			item := topLevelObject(payload, "item")
			if checkpointEventKind(item) != "agent_message" {
				continue
			}
			if text := topLevelString(item, "text"); text != "" {
				parsed.summary = text
			}
		case "turn.completed":
			usage := topLevelObject(payload, "usage")
			input, okInput := topLevelInt(usage, "input_tokens")
			cached, okCached := topLevelInt(usage, "cached_input_tokens")
			output, okOutput := topLevelInt(usage, "output_tokens")
			if okInput || okCached || okOutput {
				parsed.hasUsage = true
			}
			if okInput {
				parsed.usage.InputTokens = input
			}
			if okCached {
				parsed.usage.CachedInputTokens = cached
			}
			if okOutput {
				parsed.usage.OutputTokens = output
			}
		case "turn.failed":
			errPayload := topLevelObject(payload, "error")
			if message := topLevelString(errPayload, "message"); message != "" {
				parsed.errorMessage = message
			}
		case "error":
			if message := topLevelString(payload, "message"); message != "" {
				parsed.errorMessage = message
			}
		case "session", "session.updated":
			if sessionID := topLevelString(payload, "session_id", "sessionId", "sessionID"); sessionID != "" {
				parsed.sessionID = sessionID
				if displayID := topLevelString(payload, "display_id", "displayId"); displayID != "" {
					parsed.displayID = displayID
				} else {
					parsed.displayID = sessionID
				}
			}
		default:
			if !isCheckpointPayload(payload) {
				continue
			}
			sessionID := topLevelString(payload, "session_id", "sessionId", "sessionID", "thread_id", "threadId")
			if sessionID == "" {
				continue
			}
			parsed.sessionID = sessionID
			if displayID := topLevelString(payload, "display_id", "displayId"); displayID != "" {
				parsed.displayID = displayID
			} else {
				parsed.displayID = sessionID
			}
		}
	}
	return parsed
}

func checkpointEventKind(payload map[string]any) string {
	return strings.ToLower(topLevelString(payload, "event", "type", "kind"))
}

func isCheckpointPayload(payload map[string]any) bool {
	for key, value := range payload {
		switch value.(type) {
		case nil, string, bool, float64:
		default:
			return false
		}

		switch key {
		case "session_id", "sessionId", "sessionID", "thread_id", "threadId", "display_id", "displayId", "event", "type", "kind", "timestamp", "ts", "created_at", "createdAt":
		default:
			return false
		}
	}
	return true
}

func parseCodexJSONLine(line string) (map[string]any, bool) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func topLevelObject(payload map[string]any, key string) map[string]any {
	raw, ok := payload[key]
	if !ok {
		return nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return value
}

func topLevelInt(payload map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case float64:
			return int(value), true
		case int:
			return value, true
		case int64:
			return int(value), true
		}
	}
	return 0, false
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
