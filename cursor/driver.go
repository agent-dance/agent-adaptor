package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/adapterutil"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/configprobe"
)

const DriverType = "cursor"

type adapter struct{}

func New(cfg agentadaptor.CursorConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.CursorConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        DriverType,
		DisplayName: "Cursor Agent",
		Models:      cursorModels(),
		ConfigSchema: &agentadaptor.ConfigSchema{
			Fields: []agentadaptor.ConfigField{
				{Name: "command", Label: "Command", Type: "text", Description: "Override the Cursor Agent CLI executable.", Hint: "Defaults to `agent` when unset.", Default: "agent", Group: "command"},
				{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
				{Name: "agent_profile_dir", Label: "Agent Profile Dir", Type: "text", Description: "Stable local Cursor profile directory used when CURSOR_HOME is not already set in CommonConfig.Env.", Hint: "Maps to CURSOR_HOME. Explicit CommonConfig.Env CURSOR_HOME still wins.", Group: "profile"},
				{Name: "model", Label: "Model", Type: "select", Description: "Cursor model identifier, for example gpt-5.", Default: "gpt-5", Options: modelOptions(cursorModels()), Group: "model"},
				{Name: "mode", Label: "Mode", Type: "select", Description: "Cursor agent mode passed through --mode.", Options: []agentadaptor.ConfigOption{{Value: "agent", Label: "Agent"}, {Value: "ask", Label: "Ask"}}, Group: "execution"},
				{Name: "auto_trust", Label: "Auto Trust", Type: "toggle", Description: "Enable --yolo / auto trust by default.", Hint: "Use only in trusted local environments.", Default: false, Group: "permissions", Meta: map[string]string{"risk": "high"}},
				{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
			},
		},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncPersistent},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		Permissions:  agentadaptor.InvocationPermissionCapability{Approvals: true, Search: true},
		Runtime:      agentadaptor.RuntimeCapability{ReportsServices: true},
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
	bindings := effectiveCursorBindings(config.CommonConfig)
	command := config.Command
	if command == "" {
		command = "agent"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
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
			Hint:    "Run Cursor Agent once or point AgentProfileDir / CURSOR_HOME at the target operator profile.",
		})
	}
	checks = append(checks, cursorAuthChecks(bindings)...)
	checks = append(checks, cursorConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, _ any) ([]agentadaptor.ModelInfo, error) {
	return adapter{}.Descriptor().Models, nil
}

func (adapter) DetectModel(_ context.Context, cfg any) (*agentadaptor.DetectedModel, error) {
	config := readConfig(cfg)
	bindings := effectiveCursorBindings(config.CommonConfig)
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

func (adapter) GetProfile(_ context.Context, cfg any, _ agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
	return cursorProfile(readConfig(cfg).CommonConfig), nil
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

func (adapter) GetQuota(_ context.Context, _ any) (agentadaptor.QuotaReport, error) {
	return agentadaptor.QuotaReport{
		DriverType: DriverType,
		Provider:   "cursor",
		Available:  false,
		Error:      "Cursor live quota probing is not available in this SDK build",
	}, nil
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.SkillPayload) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	return listCursorSkills(payload, effectiveCursorBindings(config.CommonConfig))
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload agentadaptor.SkillPayload, _ []string) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	return syncCursorSkills(ctx, payload, effectiveCursorBindings(config.CommonConfig), noopCursorSink{})
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg := readConfig(req.Config)
	command := cfg.Command
	if command == "" {
		command = "agent"
	}
	effectiveEnv, err := adapterutil.RuntimeEnvBindings(effectiveCursorBindings(cfg.CommonConfig), req.Runtime)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	if _, err := syncCursorSkills(ctx, req.Skills, effectiveEnv, sink); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)

	args := []string{"-p", "--output-format", "stream-json", "--workspace", effectiveCWD}
	if req.Session != nil && req.Session.State != nil {
		if req.Session.State.Data[agentadaptor.SessionParamCWD] != "" && req.Session.State.Data[agentadaptor.SessionParamCWD] != effectiveCWD {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session working directory changed"}
		}
		if req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != "" && req.Session.State.Data[agentadaptor.SessionParamWorkspaceID] != req.Workspace.ID {
			return agentadaptor.DriverRunResult{}, &agentadaptor.ResumeRejectedError{Reason: "session workspace changed"}
		}
	}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Mode != "" {
		args = append(args, "--mode", string(cfg.Mode))
	}
	if cfg.AutoTrust || req.Permissions.TrustMode == agentadaptor.TrustAuto {
		args = append(args, "--yolo")
	}
	args = append(args, cfg.ExtraArgs...)

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
		Env:     effectiveEnv,
		Prompt:  prompt,
	}, sink)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	checkpoint := parseCheckpoint(result.Stdout, result.ExitCode)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			agentadaptor.SessionParamCWD:         effectiveCWD,
			agentadaptor.SessionParamWorkspaceID: req.Workspace.ID,
		}
	}

	return agentadaptor.DriverRunResult{
		Output:          result.Stdout,
		Transcript:      adapterutil.TranscriptFromOutput(result.Stdout, result.Stderr, "", nil, nil),
		ExitCode:        result.ExitCode,
		Checkpoint:      checkpoint,
		Metadata:        map[string]string{"stderr": result.Stderr},
		Provider:        "cursor",
		Model:           cfg.Model,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
	}, nil
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

func parseCheckpoint(stdout string, exitCode int) *agentadaptor.DriverCheckpoint {
	if exitCode != 0 {
		return nil
	}
	var checkpoint *agentadaptor.DriverCheckpoint
	for _, line := range strings.Split(stdout, "\n") {
		if parsed := parseCheckpointLine(line, cursorCheckpointEvents); parsed != nil {
			checkpoint = parsed
		}
	}
	return checkpoint
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

var cursorCheckpointEvents = map[string]struct{}{
	"result":          {},
	"run.completed":   {},
	"session":         {},
	"session.updated": {},
}

func parseCheckpointLine(line string, allowedEvents map[string]struct{}) *agentadaptor.DriverCheckpoint {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}

	sessionID := topLevelString(payload, "session_id", "sessionId", "sessionID")
	if sessionID == "" {
		return nil
	}

	eventKind := checkpointEventKind(payload)
	if eventKind != "" {
		if _, ok := allowedEvents[eventKind]; !ok {
			return nil
		}
	} else if !isCheckpointPayload(payload) {
		return nil
	}

	displayID := topLevelString(payload, "display_id", "displayId")
	if displayID == "" {
		displayID = sessionID
	}

	return &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{
			ResumeID:  sessionID,
			DisplayID: displayID,
		},
		Valid: true,
	}
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
		case "session_id", "sessionId", "sessionID", "display_id", "displayId", "event", "type", "kind", "timestamp", "ts", "created_at", "createdAt":
		default:
			return false
		}
	}
	return true
}
