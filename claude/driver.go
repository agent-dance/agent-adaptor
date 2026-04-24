package claude

import (
	"context"
	"encoding/json"
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

// jsonMarshalInteractive is a pin-point JSON encoder used only for the few
// NDJSON frames the driver injects into the CLI's stdin. A plain
// encoding/json.Marshal produces the right shape; this thin wrapper exists
// so tests can mock out the encoding path if needed later.
func jsonMarshalInteractive(v any) ([]byte, error) {
	return json.Marshal(v)
}

const DriverType = "claude"

type adapter struct{}

func New(cfg agentadaptor.ClaudeConfig, opts ...agentadaptor.AgentOption) agentadaptor.TypedAgentBinding[agentadaptor.ClaudeConfig] {
	return agentadaptor.BindTyped(NewAdapter(), cfg, opts...)
}

func NewAdapter() agentadaptor.DriverAdapter {
	return adapter{}
}

// StreamCapability declares Claude Code stream-json capabilities when
// --include-partial-messages is enabled.
func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         false,
	}
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
				{Name: "model", Label: "Model", Type: "select", Description: "Claude model identifier, for example claude-sonnet-4 or a Bedrock-native us.anthropic.* id.", Default: defaultClaudeModel, Options: modelOptions(claudeModels()), Group: "model"},
				{Name: "effort", Label: "Thinking Effort", Type: "select", Description: "Optional Claude thinking effort.", Options: []agentadaptor.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}}, Group: "model"},
				{Name: "max_turns_per_run", Label: "Max Turns", Type: "number", Description: "Optional max-turns guard for one run.", Group: "execution"},
				{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
			},
		},
		Sessions:     agentadaptor.SessionCapability{SupportsResume: true},
		Skills:       agentadaptor.SkillCapability{Supported: true, Mode: agentadaptor.SkillSyncEphemeral},
		MCP:          agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: agentadaptor.InstructionsCapability{Supported: true},
		Workspace:    agentadaptor.WorkspaceCapability{Supported: true},
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Isolation: false, WebSearch: false, Browser: true,
			// Phase 3 interactive mode uses stdio permission prompting
			// (`--permission-prompt-tool stdio`) so Claude emits native
			// can_use_tool control_request frames. We still keep
			// Permission.Ask disabled until the dedicated host UX and
			// end-to-end coverage are expanded beyond PlanReview/Question.
			Permission: agentadaptor.HumanDecisionSupport{Ask: false, AutoApprove: true, AutoReject: true, Retry: false},
			// PlanReview + Question are fully supported in Phase 3 via
			// can_use_tool control_request / control_response over stdio.
			// Retry stays false: CLI cannot
			// "re-ask the same tool_use_id"; retry would need to push the
			// model to emit a new tool_use, which is not automatic.
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			Question:   agentadaptor.QuestionSupport{Ask: true, AutoReject: true, Retry: false},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
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
	command := config.Command
	if command == "" {
		command = "claude"
	}
	checks := append(adapterutil.CommandEnvironmentChecks(command), adapterutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, agentadaptor.EnvironmentCheck{Code: "claude_profile_error", Level: "fail", Message: "Claude profile resolution failed.", Detail: err.Error()})
		return adapterutil.SummarizeEnvironment(DriverType, checks), nil
	}
	checks = append(checks, claudeAuthChecks(bindings)...)
	checks = append(checks, claudeModelCompatibilityChecks(config)...)
	checks = append(checks, claudeConfigChecks(bindings)...)
	return adapterutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, cfg any) ([]agentadaptor.ModelInfo, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, nil)
	if err != nil {
		return nil, err
	}
	return claudeModelsForBindings(bindings), nil
}

func (adapter) DetectModel(_ context.Context, cfg any, profile *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error) {
	return detectClaudeEffectiveModel(readConfig(cfg), profile)
}

func (adapter) GetProfile(_ context.Context, cfg any, _ agentadaptor.AgentIdentity, profile *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
	return claudeProfile(readConfig(cfg).CommonConfig, profile), nil
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

func (adapter) GetQuota(ctx context.Context, cfg any, profile *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.QuotaReport{}, err
	}
	return claudeQuotaReport(ctx, bindings)
}

func (adapter) ListSkills(_ context.Context, cfg any, payload agentadaptor.SkillPayload, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return listClaudeSkills(payload, bindings)
}

func (adapter) SyncSkills(_ context.Context, cfg any, payload agentadaptor.SkillPayload, _ []string, profile *agentadaptor.ProfileSelection) (agentadaptor.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return agentadaptor.SkillSnapshot{}, err
	}
	return syncClaudeSkills(payload, bindings)
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
	if err := syncClaudeMCPProfile(cfg.CommonConfig, req.Profile, req.MCP); err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	bindings, err := effectiveClaudeBindings(cfg.CommonConfig, req.Profile)
	if err != nil {
		return agentadaptor.DriverRunResult{}, err
	}
	effectiveEnv, err := adapterutil.RuntimeEnvBindings(bindings, req.Runtime)
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

	modelFlag := claudeRequestedModelFlag(cfg)
	reportedModel := modelFlag
	if reportedModel == "" {
		if detected, err := detectClaudeEffectiveModel(cfg, req.Profile); err == nil && detected != nil {
			reportedModel = detected.Model
		}
	}

	interactive := wantsInteractiveClaude(req.Policy.HumanDecision)
	if interactive {
		if err := validateInteractivePolicy(req.Policy.HumanDecision); err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
	}

	args := buildClaudeExecArgs(cfg, req, bundleRoot, interactive)

	rawPrompt := req.Prompt
	if runtimePrefix := adapterutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		rawPrompt = runtimePrefix + "\n\n" + rawPrompt
	}
	if req.Instructions != nil && req.Instructions.Path != "" {
		rawPrompt = "Instructions bundle: " + req.Instructions.Path + "\n\n" + rawPrompt
	}

	parser := newClaudeParser(sink)
	parser.setHITLContext(req.RunID, req.Policy.HumanDecision)
	if req.Streaming || interactive {
		// Interactive mode always streams (we need the partial_json deltas
		// to reconstruct tool_use inputs), regardless of the caller's
		// Streaming flag. Hosts that invoke Start without streaming but
		// want HITL nevertheless get the StreamHITLRequested/Resolved
		// pair for audit — the stream channel is closed on the outer
		// handle so those events are dropped by the dualSink, but the
		// parser's RequestDecision flow still works through the runner's
		// typed-handler dispatch.
		parser.enableStreaming(req.RunID)
	}

	runReq := clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     effectiveCWD,
		Env:     effectiveEnv,
		Observe: parser.onChunk,
	}

	if interactive {
		// Encode the initial user message as an NDJSON user frame rather
		// than a raw text prompt: --input-format stream-json expects a
		// protocol envelope.
		initial, err := encodeInteractiveUserFrame(rawPrompt)
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
		runReq.Prompt = initial
		runReq.Stdin = clihelper.NewStdinController()

		// Bind the parser to the interactive sink and stdin so tool_use
		// frames route through sink.RequestDecision and the response is
		// injected back via stdin.Write.
		ic, ok := sink.(agentadaptor.DecisionCapableSink)
		if !ok {
			return agentadaptor.DriverRunResult{}, errClaudeInteractiveSinkRequired
		}
		parser.enableInteractive(ctx, ic, runReq.Stdin)
	} else {
		runReq.Prompt = rawPrompt
	}

	result, err := clihelper.Run(ctx, runReq, sink)
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
	if parser.pendingFailure != nil {
		failure = parser.pendingFailure
	} else if strings.TrimSpace(parser.errorMessage) != "" {
		failure = &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureAgentError,
			Message: parser.errorMessage,
		}
	}

	meta := parser.outputMetadata()

	return agentadaptor.DriverRunResult{
		Output:          parser.buildOutput(),
		RawStreams:      &raw,
		Transcript:      parser.transcript,
		ExitCode:        result.ExitCode,
		Signal:          result.Signal,
		TimedOut:        result.TimedOut,
		Usage:           parser.usage,
		Checkpoint:      checkpoint,
		Metadata:        meta,
		Provider:        "anthropic",
		Model:           reportedModel,
		Summary:         parser.finalSummary(),
		Result:          parser.resultFinal,
		RuntimeServices: adapterutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:         failure,
	}, nil
}

func buildClaudeExecArgs(cfg agentadaptor.ClaudeConfig, req agentadaptor.DriverRunRequest, bundleRoot string, interactive bool) []string {
	// Common core. `--print` + `stream-json` output are always needed by
	// the parser.
	args := []string{"--print", "--output-format", "stream-json", "--verbose"}

	if interactive {
		// Phase 3 bidirectional:
		//   - --input-format stream-json: read NDJSON user frames from stdin
		//     at any time during the turn
		//   - --include-partial-messages: ensure input_json_delta frames
		//     arrive so the parser can reconstruct tool_use args
		//   - --replay-user-messages: CLI echoes every user frame it
		//     consumed (isReplay:true) as an ack. The parser uses these
		//     purely for ack; see handlePayload.
		//   - --permission-prompt-tool stdio: route permission prompts
		//     (including AskUserQuestion / ExitPlanMode) back to the host
		//     as control_request frames.
		args = append(args,
			"--input-format", "stream-json",
			"--include-partial-messages",
			"--replay-user-messages",
			"--permission-prompt-tool", "stdio",
		)
	} else {
		// Phase 1 one-shot: read the prompt as plain text from stdin.
		args = append(args, "-")
		if req.Streaming {
			args = append(args, "--include-partial-messages")
		}
	}

	modelFlag := claudeRequestedModelFlag(cfg)
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
	}
	if !interactive && req.Policy.HumanDecision.Permission == agentadaptor.HumanDecisionAutoApprove {
		// --dangerously-skip-permissions is only meaningful in Phase 1
		// mode where the CLI itself enforces permissions. In interactive
		// mode the CLI routes permission prompts back through stdio
		// control_request frames, so the flag would bypass the host's
		// decision path and muddy the audit trail.
		args = append(args, "--dangerously-skip-permissions")
	}
	if req.Policy.Browser == agentadaptor.FeatureAllow {
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
	return args
}

// wantsInteractiveClaude reports whether the policy explicitly asks for
// Phase 3 stream-json bidirectional mode. We deliberately look at the raw
// policy fields (not EffectiveHumanDecisionPolicy) so that a zero-value
// policy stays in Phase 1 observational mode — otherwise the default
// (PlanReview=Ask + Permission=Ask) would silently promote every claude
// run into interactive mode AND then fail validateInteractivePolicy.
//
// Interactive mode engages when the host explicitly sets PlanReview=Ask or
// Question=Ask. AutoReject stays in Phase 1 because it's deterministic and
// the observational flow is sufficient.
func wantsInteractiveClaude(p agentadaptor.HumanDecisionPolicy) bool {
	return p.PlanReview == agentadaptor.HumanDecisionAsk ||
		p.Question == agentadaptor.QuestionAsk
}

// validateInteractivePolicy rejects policy shapes Phase 3 cannot honour.
// The main one: Permission=Ask has no Phase 3 implementation (Phase 3.5
// is needed for host-side tool execution). Callers get a clear policy
// error before the CLI starts.
//
// This inspects raw fields — identical to wantsInteractiveClaude — rather
// than effective defaults, so users who never touched Permission are not
// penalised for the SDK default (Ask). Phase 3 only rejects when the host
// *explicitly* requested Permission=Ask.
func validateInteractivePolicy(p agentadaptor.HumanDecisionPolicy) error {
	if p.Permission == agentadaptor.HumanDecisionAsk {
		return errors.New("claude Phase 3: HumanDecision.Permission=Ask is not yet supported (needs host-side tool executor in Phase 3.5). " +
			"Use Permission=AutoApprove to run Bash/Write/Edit, or leave Permission unset and only set PlanReview/Question.")
	}
	return nil
}

// encodeInteractiveUserFrame wraps the raw prompt in the NDJSON envelope the
// CLI expects when --input-format stream-json is set. Trailing newline lets
// the CLI's line-oriented parser commit the frame immediately.
func encodeInteractiveUserFrame(prompt string) (string, error) {
	frame := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	}
	raw, err := jsonMarshalInteractive(frame)
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

// errClaudeInteractiveSinkRequired is returned when the policy demands
// interactive HITL but the supplied EventSink does not implement
// DecisionCapableSink (this should never happen for runs started through
// the runner, which always passes a dualSink).
var errClaudeInteractiveSinkRequired = errors.New("claude Phase 3 interactive mode requires a DecisionCapableSink; the runner's dualSink provides this automatically — this error usually means the driver was invoked outside the SDK")

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
