package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// jsonMarshalInteractive is a pin-point JSON encoder used only for the few
// NDJSON frames the driver injects into the CLI's stdin. A plain
// encoding/json.Marshal produces the right shape; this thin wrapper exists
// so tests can mock out the encoding path if needed later.
func jsonMarshalInteractive(v any) ([]byte, error) {
	return json.Marshal(v)
}

// DriverType is the stable descriptor type for the built-in Claude driver.
const DriverType = "claude"

type adapter struct{}

// StreamCapability declares Claude Code stream-json capabilities when
// --include-partial-messages is enabled.
func (adapter) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         true,
	}
}

func (adapter) Descriptor() driver.Descriptor {
	fields := []driver.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Description: "Override the Claude CLI executable.", Hint: "Defaults to `claude` when unset.", Default: "claude", Group: "command"},
		{Name: "cwd", Label: "Working Directory", Type: "text", Description: "Default working directory when the workspace manager does not override it.", Hint: "Leave empty to let the workspace manager resolve the cwd.", Group: "command"},
		{Name: "model", Label: "Model", Type: "select", Description: "Claude model identifier, for example claude-sonnet-4 or a Bedrock-native us.anthropic.* id.", Default: defaultClaudeModel, Options: modelOptions(claudeModels()), Group: "model"},
		{Name: "effort", Label: "Thinking Effort", Type: "select", Description: "Optional Claude thinking effort.", Options: []driver.ConfigOption{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}}, Group: "model"},
		{Name: "max_turns_per_run", Label: "Max Turns", Type: "number", Description: "Optional max-turns guard for one run.", Group: "execution"},
		{Name: "extra_args", Label: "Extra Args", Type: "textarea", Description: "Additional CLI args appended after SDK-managed flags.", Group: "command"},
	}
	fields = append(fields, profileconfig.CapabilityFields(DriverType)...)
	return driver.Descriptor{
		Type:         DriverType,
		DisplayName:  "Claude Code",
		Models:       claudeModels(),
		ConfigSchema: &driver.ConfigSchema{Fields: fields},
		Sessions:     driver.SessionCapability{SupportsResume: true},
		Skills:       driver.SkillCapability{Supported: true, Mode: driver.SkillSyncPersistent},
		MCP:          driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions: driver.InstructionsCapability{Supported: true},
		Workspace:    driver.WorkspaceCapability{Supported: true},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			Isolation: false, WebSearch: false, Browser: true,
			// Interactive mode uses stdio permission prompting
			// (`--permission-prompt-tool stdio`) so every native can_use_tool
			// control_request, including ordinary tools, is resolved through
			// the same DecisionCapableSink as plan review and questions.
			Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			// PlanReview + Question are fully supported in interactive mode via
			// can_use_tool control_request / control_response over stdio.
			// Retry stays false: CLI cannot
			// "re-ask the same tool_use_id"; retry would need to push the
			// model to emit a new tool_use, which is not automatic.
			PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: false},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true, Retry: false},
		},
		Runtime: driver.RuntimeCapability{ReportsServices: true},
		StructuredOutput: driver.StructuredOutputCapability{
			JSONSchemaNative:         true,
			JSONSchemaPromptValidate: true,
			WorksWithRun:             true,
			WorksWithStreaming:       true,
			WorksWithHITL:            false,
			Notes:                    "Native JSON Schema output supports print-mode streaming via --output-format stream-json --json-schema; interactive HITL combinations are still not advertised.",
		},
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case Config, *Config:
		return nil
	default:
		return errors.New("claude driver requires claude.Config")
	}
}

func (adapter) CheckEnvironment(_ context.Context, cfg any) (driver.EnvironmentReport, error) {
	config := readConfig(cfg)
	command := config.Command
	if command == "" {
		command = "claude"
	}
	checks := append(driverutil.CommandEnvironmentChecks(command), driverutil.CWDEnvironmentChecks(config.CommonConfig.CWD)...)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, nil)
	if err != nil {
		checks = append(checks, driver.EnvironmentCheck{Code: "claude_profile_error", Level: "fail", Message: "Claude profile resolution failed.", Detail: err.Error()})
		return driverutil.SummarizeEnvironment(DriverType, checks), nil
	}
	checks = append(checks, claudeAuthChecks(bindings)...)
	checks = append(checks, claudeModelCompatibilityChecks(config)...)
	checks = append(checks, claudeConfigChecks(bindings)...)
	return driverutil.SummarizeEnvironment(DriverType, checks), nil
}

func (adapter) ListModels(_ context.Context, cfg any) ([]driver.ModelInfo, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, nil)
	if err != nil {
		return nil, err
	}
	return claudeModelsForBindings(bindings), nil
}

func (adapter) DetectModel(_ context.Context, cfg any, profile *driver.ProfileSelection) (*driver.DetectedModel, error) {
	return detectClaudeEffectiveModel(readConfig(cfg), profile)
}

func (adapter) GetProfile(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection) (driver.AgentProfile, error) {
	return claudeProfile(readConfig(cfg).CommonConfig, profile), nil
}

func claudeConfigCandidates(bindings []driver.EnvBinding) []string {
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

func claudeConfigChecks(bindings []driver.EnvBinding) []driver.EnvironmentCheck {
	checks := make([]driver.EnvironmentCheck, 0)
	for _, candidate := range claudeConfigCandidates(bindings) {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		checks = append(checks, driver.EnvironmentCheck{
			Code:    "claude_config_present",
			Level:   "info",
			Message: "Claude config file is present.",
			Detail:  candidate,
		})
		if model, ok, err := configprobe.ReadTopLevelJSONString(candidate, "model"); err == nil && ok {
			checks = append(checks, driver.EnvironmentCheck{
				Code:    "claude_config_model",
				Level:   "info",
				Message: "Claude config file declares a default model.",
				Detail:  model,
			})
		}
		if baseURL, ok, err := configprobe.ReadNestedJSONString(candidate, "env", "ANTHROPIC_BASE_URL"); err == nil && ok {
			checks = append(checks, driver.EnvironmentCheck{
				Code:    "claude_config_base_url",
				Level:   "info",
				Message: "Claude config file overrides the Anthropic base URL.",
				Detail:  baseURL,
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

func (adapter) ConfigSchema(_ context.Context, cfg any) (*driver.ConfigSchema, error) {
	return hydrateClaudeConfigSchema(readConfig(cfg)), nil
}

func (adapter) GetQuota(ctx context.Context, cfg any, profile *driver.ProfileSelection) (driver.QuotaReport, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.QuotaReport{}, err
	}
	return claudeQuotaReport(ctx, bindings)
}

func (adapter) ListSkills(_ context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return listClaudeSkills(payload, selected, resolved, bindings)
}

// InjectSkills is the pre-run hook the SDK invokes before calling Run. Claude
// needs the Run-scoped effective profile bindings before it can reconcile the
// profile-local skills home, so the built-in adapter keeps this hook as a
// no-op and performs materialization after the resume guard inside Run.
func (adapter) InjectSkills(_ context.Context, _ any, _ driver.ResolvedSkills, _ *driver.ProfileSelection) error {
	return nil
}

func (adapter) SyncSkills(ctx context.Context, cfg any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, profile *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	_, kind := claudeProfileAndKind(config.CommonConfig, profile)
	return syncClaudeSkills(ctx, payload, selected, resolved, bindings, kind)
}

func (adapter) SnapshotProfileResources(_ context.Context, cfg any, _ driver.AgentIdentity, profile *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (engine.ProfileSnapshot, error) {
	config := readConfig(cfg)
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	skills, err := listClaudeSkills(payload.Skills, selected, resolved, bindings)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := claudeProfileAndKind(config.CommonConfig, profile)
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
	bindings, err := effectiveClaudeBindings(config.CommonConfig, profile)
	if err != nil {
		return engine.ProfileSnapshot{}, err
	}
	effectiveProfile, kind := claudeProfileAndKind(config.CommonConfig, profile)
	skills, err := syncClaudeSkills(ctx, payload.Skills, selected, resolved, bindings, kind)
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

func (adapter) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	cfg := readConfig(req.Config)
	if err := validateClaudeSessionRequest(req); err != nil {
		return driver.Response{}, err
	}
	// Per-run WithModel overrides the configured model for this invocation only.
	if m := strings.TrimSpace(req.ModelOverride); m != "" {
		cfg.Model = m
	}
	command := cfg.Command
	if command == "" {
		command = "claude"
	}
	profileFingerprint := req.ProfilePayload.Fingerprint
	bindings, err := effectiveClaudeBindingsNoInitialize(cfg.CommonConfig, req.Profile)
	if err != nil {
		return driver.Response{}, err
	}
	effectiveCWD := chooseCWD(cfg.CommonConfig, req.Workspace)
	if err := validateClaudeSessionGuard(req, effectiveCWD, profileFingerprint); err != nil {
		return driver.Response{}, err
	}
	bindings, err = effectiveClaudeBindings(cfg.CommonConfig, req.Profile)
	if err != nil {
		return driver.Response{}, err
	}
	_, profileKind := claudeProfileAndKind(cfg.CommonConfig, req.Profile)
	if _, err := syncClaudeSkills(ctx, req.Skills, req.Skills.Keys(), nil, bindings, profileKind); err != nil {
		return driver.Response{}, err
	}
	effectiveProfile, _ := claudeProfileAndKind(cfg.CommonConfig, req.Profile)
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
	effectiveEnv, err := driverutil.RuntimeEnvBindings(bindings, req.Runtime)
	if err != nil {
		return driver.Response{}, err
	}

	modelFlag := claudeRequestedModelFlag(cfg)
	reportedModel := modelFlag
	if reportedModel == "" {
		if detected, err := detectClaudeEffectiveModel(cfg, req.Profile); err == nil && detected != nil {
			reportedModel = detected.Model
		}
	}

	interactive := wantsInteractiveClaude(req.Policy.HumanDecision)
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		if interactive {
			return driver.Response{}, &engine.StructuredOutputUnsupportedError{Driver: DriverType, Mode: req.OutputSchema.Mode, Reason: "Claude native structured output is not supported with interactive HITL"}
		}
	}

	args, err := buildClaudeExecArgs(cfg, req, interactive)
	if err != nil {
		return driver.Response{}, err
	}

	rawPrompt := req.Prompt
	if runtimePrefix := driverutil.RuntimePromptPrefix(req.Runtime); runtimePrefix != "" {
		rawPrompt = runtimePrefix + "\n\n" + rawPrompt
	}
	if prefix := profileinstructions.PromptPrefix(preparedInstructions, profileinstructions.Mode(req.Instructions)); prefix != "" {
		rawPrompt = prefix + "\n\n" + rawPrompt
	}

	parser := newClaudeParser(sink)
	parser.setHITLContext(req.RunID, req.Policy.HumanDecision)
	if req.Streaming || interactive {
		// Interactive mode always streams (we need the partial_json deltas
		// to reconstruct tool_use inputs), even when the resolved invocation
		// did not otherwise select the rich provider transport. HITL requests
		// and resolutions still travel through the same decision-capable Event
		// sink used by every Agent.Run and Agent.Stream invocation.
		parser.enableStreaming(req.RunID)
	}

	runReq := clihelper.CommandRequest{
		Command: command,
		Args:    args,
		CWD:     effectiveCWD,
		Env:     ensureRootSandboxEnv(args, effectiveEnv),
		Observe: parser.onChunk,
	}

	if interactive {
		// Encode the initial user message as an NDJSON user frame rather
		// than a raw text prompt: --input-format stream-json expects a
		// protocol envelope.
		initial, err := encodeInteractiveUserFrame(rawPrompt)
		if err != nil {
			return driver.Response{}, err
		}
		runReq.Prompt = initial
		runReq.Stdin = clihelper.NewStdinController()

		// Bind the parser to the interactive sink and stdin so tool_use
		// frames route through sink.RequestDecision and the response is
		// injected back via stdin.Write.
		ic, ok := sink.(driver.DecisionCapableSink)
		if !ok {
			return driver.Response{}, errClaudeInteractiveSinkRequired
		}
		parser.enableInteractive(ctx, ic, runReq.Stdin)
	} else {
		runReq.Prompt = rawPrompt
	}

	result, err := clihelper.Run(ctx, runReq, sink)
	if err != nil {
		return driver.Response{}, err
	}
	parser.finalize()
	raw := driver.RawStreams{Stdout: result.RawStreams.Stdout, Stderr: result.RawStreams.Stderr, Terminal: parser.terminal}
	if parser.interactiveErr != nil {
		failure := &driver.RunFailure{Code: driver.FailureAgentError, Message: parser.interactiveErr.Error()}
		parser.completeStream(failure, result.ExitCode, result.Signal, result.TimedOut)
		return driver.Response{}, parser.interactiveErr
	}
	if err := validateClaudeForkOutcome(req, parser); err != nil {
		parser.completeStream(&driver.RunFailure{Code: driver.FailureAgentError, Message: err.Error()}, result.ExitCode, result.Signal, result.TimedOut)
		return driver.Response{}, err
	}
	if req.Session != nil && req.Session.State != nil && strings.TrimSpace(req.Session.State.ResumeID) != "" &&
		!parser.terminalSuccess && isClaudeResumeRejected(raw.Stdout, raw.Stderr, parser.errorMessage) {
		reason := strings.TrimSpace(parser.errorMessage)
		if reason == "" {
			reason = "claude resume session " + strconv.Quote(req.Session.State.ResumeID) + " is unavailable"
		}
		err := &engine.ResumeRejectedError{Reason: reason}
		parser.completeStream(&driver.RunFailure{Code: driver.FailureAgentError, Message: err.Error()}, result.ExitCode, result.Signal, result.TimedOut)
		return driver.Response{}, err
	}
	failure := parser.failureForOutcome(result.ExitCode, result.Signal, result.TimedOut)
	var structuredOutput *driver.StructuredOutput
	if req.OutputSchema != nil {
		source := driver.StructuredOutputSourceNative
		if req.OutputSchema.Mode == driver.StructuredOutputPromptValidate {
			source = driver.StructuredOutputSourcePromptValidate
		} else {
			structuredOutput = parser.structuredOutput
		}
		structuredOutput, failure = engine.FinalizeStructuredOutput(
			req.OutputSchema,
			source,
			parser.buildOutput(),
			structuredOutput,
			failure,
		)
	}
	checkpoint := parser.checkpointForOutcome(result.ExitCode, result.Signal, result.TimedOut, failure)
	if checkpoint != nil && checkpoint.State != nil {
		checkpoint.State.Data = map[string]string{
			driver.SessionParamCWD:                effectiveCWD,
			driver.SessionParamWorkspaceID:        req.Workspace.ID,
			driver.SessionParamProfileFingerprint: profileFingerprint,
		}
	}
	parser.completeStream(failure, result.ExitCode, result.Signal, result.TimedOut)

	return driver.Response{
		Output:           parser.buildOutput(),
		RawStreams:       &raw,
		Transcript:       parser.transcript,
		ExitCode:         result.ExitCode,
		Signal:           result.Signal,
		TimedOut:         result.TimedOut,
		Usage:            parser.usage,
		Checkpoint:       checkpoint,
		Provider:         "anthropic",
		Model:            reportedModel,
		Summary:          parser.finalSummary(),
		StructuredOutput: structuredOutput,
		RuntimeServices:  driverutil.RuntimeReportsFromRefs(req.Runtime.Ensured, req.Agent),
		Failure:          failure,
	}, nil
}

func validateClaudeSessionRequest(req driver.Request) error {
	if req.Session == nil || req.Session.Mode != driver.SessionFork {
		return nil
	}
	if req.Session.State == nil || strings.TrimSpace(req.Session.State.ResumeID) == "" {
		return &engine.ResumeRejectedError{Reason: "Claude session fork requires a parent resume ID"}
	}
	return nil
}

func validateClaudeForkOutcome(req driver.Request, parser *claudeParser) error {
	if req.Session == nil || req.Session.Mode != driver.SessionFork || !parser.terminalSuccess {
		return nil
	}
	parentID := ""
	if req.Session.State != nil {
		parentID = strings.TrimSpace(req.Session.State.ResumeID)
	}
	childID := strings.TrimSpace(parser.terminalSessionID)
	switch {
	case childID == "":
		return &engine.ResumeRejectedError{Reason: "Claude session fork succeeded without a child session ID"}
	case childID == parentID:
		return &engine.ResumeRejectedError{Reason: "Claude session fork reused the parent session ID"}
	default:
		return nil
	}
}

func validateClaudeSessionGuard(req driver.Request, effectiveCWD, profileFingerprint string) error {
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

func buildClaudeExecArgs(cfg Config, req driver.Request, interactive bool) ([]string, error) {
	// Common core. Native structured output supports two modes:
	//   - non-streaming: final JSON result via --output-format json
	//   - streaming: stream-json events + final structured_output/result event
	//
	// Prompt-validated structured output and ordinary runs always stay on the
	// existing stream-json path.
	nativeStructured := req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate
	args := []string{"--print"}
	if nativeStructured {
		schemaJSON, err := prepareClaudeJSONSchema(req.OutputSchema.SchemaJSON)
		if err != nil {
			return nil, err
		}
		if req.Streaming {
			args = append(args, "--output-format", "stream-json", "--verbose", "--json-schema", string(schemaJSON))
		} else {
			args = append(args, "--output-format", "json", "--json-schema", string(schemaJSON))
		}
	} else {
		args = append(args, "--output-format", "stream-json", "--verbose")
	}

	if interactive {
		// Bidirectional interactive mode:
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
		// One-shot observational mode reads the prompt as plain text from stdin.
		args = append(args, "-")
		if req.Streaming {
			args = append(args, "--include-partial-messages")
		}
	}

	modelFlag := claudeRequestedModelFlag(cfg)
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--resume", req.Session.State.ResumeID)
		if req.Session.Mode == driver.SessionFork {
			// Claude's --resume mutates the selected conversation unless the
			// explicit fork flag is present. A Thread fork must therefore use
			// both flags so the provider returns an independent child session.
			args = append(args, "--fork-session")
		}
	}
	if !interactive && req.Policy.HumanDecision.Permission == driver.HumanDecisionAutoApprove {
		// --dangerously-skip-permissions is only meaningful in one-shot
		// mode where the CLI itself enforces permissions. In interactive
		// mode the CLI routes permission prompts back through stdio
		// control_request frames, so the flag would bypass the host's
		// decision path and muddy the audit trail.
		args = append(args, "--dangerously-skip-permissions")
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
	// Call-scoped SDK semantics are authoritative over constructor ExtraArgs.
	// Remove every provider flag owned by this builder (including its detached
	// value) before appending unrelated escape-hatch arguments.
	extraArgs := withoutManagedClaudeArgs(cfg.ExtraArgs)
	if req.Policy.Browser != driver.FeatureInherit {
		// Browser policy is a nearer per-call setting than constructor-level
		// ExtraArgs. Remove either opaque browser toggle before appending the
		// one canonical flag below so provider flag ordering cannot invert it.
		extraArgs = withoutBooleanArgs(extraArgs, "--chrome", "--no-chrome")
	}
	args = append(args, extraArgs...)
	switch req.Policy.Browser {
	case driver.FeatureAllow:
		args = append(args, "--chrome")
	case driver.FeatureDeny:
		args = append(args, "--no-chrome")
	}
	return args, nil
}

func withoutBooleanArgs(args []string, names ...string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		managed := false
		for _, name := range names {
			if arg == name || strings.HasPrefix(arg, name+"=") {
				managed = true
				break
			}
		}
		if !managed {
			out = append(out, arg)
		}
	}
	return out
}

// claudeManagedExtraArgs lists CLI flags whose semantics are owned by the SDK.
// The bool reports whether the separated form consumes the following argv.
// Both long --flag=value and detached-value forms are removed.
var claudeManagedExtraArgs = map[string]bool{
	"--print":                              false,
	"-p":                                   false,
	"-":                                    false,
	"--output-format":                      true,
	"--input-format":                       true,
	"--json-schema":                        true,
	"--verbose":                            false,
	"--include-partial-messages":           false,
	"--replay-user-messages":               false,
	"--permission-prompt-tool":             true,
	"--permission-mode":                    true,
	"--dangerously-skip-permissions":       false,
	"--allow-dangerously-skip-permissions": false,
	"--settings":                           true,
	"--setting-sources":                    true,
	"--tools":                              true,
	"--allowedTools":                       true,
	"--allowed-tools":                      true,
	"--disallowedTools":                    true,
	"--disallowed-tools":                   true,
	"--resume":                             true,
	"-r":                                   true,
	"--continue":                           false,
	"-c":                                   false,
	"--fork-session":                       false,
	"--session-id":                         true,
	"--no-session-persistence":             false,
	"--model":                              true,
	"--effort":                             true,
	"--max-turns":                          true,
}

var claudeManagedVariadicExtraArgs = map[string]struct{}{
	"--tools":            {},
	"--allowedTools":     {},
	"--allowed-tools":    {},
	"--disallowedTools":  {},
	"--disallowed-tools": {},
}

func withoutManagedClaudeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := arg
		hasInlineValue := false
		if idx := strings.IndexByte(arg, '='); idx >= 0 {
			name = arg[:idx]
			hasInlineValue = true
		}
		consumesValue, managed := claudeManagedExtraArgs[name]
		if !managed {
			out = append(out, arg)
			continue
		}
		if consumesValue && !hasInlineValue && i+1 < len(args) {
			if _, variadic := claudeManagedVariadicExtraArgs[name]; variadic {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
				continue
			}
			// A following flag is not a detached value. Leave it for the next
			// iteration so malformed `--model --custom value` input cannot
			// swallow an otherwise valid unrelated provider argument.
			if !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		}
	}
	return out
}

// ensureRootSandboxEnv protects one-shot runs launched under a UID-0 process
// (systemd User=root, container root, CI runners, …) from the upstream
// Claude CLI's built-in guard:
//
//	--dangerously-skip-permissions cannot be used with root/sudo privileges
//	for security reasons
//
// The CLI's guard (`claude-code@2.1.x`) skips the abort when either
// IS_SANDBOX=1 or CLAUDE_CODE_BUBBLEWRAP is truthy in the subprocess
// environment. Because the driver itself appends --dangerously-skip-permissions
// for HumanDecisionAutoApprove (see buildClaudeExecArgs), root callers would
// otherwise get a 1-second subprocess failure with no session_id, surfacing
// all the way up as ErrSessionCheckpointMissing. We short-circuit that by
// injecting IS_SANDBOX=1 into the spawned CLI env — never into the parent
// process env — so codex / cursor drivers stay untouched and hosts that set
// the variable explicitly (truthy or falsy) keep full control.
//
// Skip conditions:
//   - non-root process (Geteuid() != 0), including Windows where Geteuid
//     returns -1;
//   - args do not contain --dangerously-skip-permissions (interactive mode
//     or HumanDecisionAsk path);
//   - host already set IS_SANDBOX or CLAUDE_CODE_BUBBLEWRAP (intent wins).
//
// geteuid is overridable in tests; production always delegates to os.Geteuid.
var geteuid = os.Geteuid

func ensureRootSandboxEnv(args []string, env []driver.EnvBinding) []driver.EnvBinding {
	if geteuid() != 0 {
		return env
	}
	needsGuard := false
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			needsGuard = true
			break
		}
	}
	if !needsGuard {
		return env
	}
	for _, b := range env {
		if b.Name == "IS_SANDBOX" || b.Name == "CLAUDE_CODE_BUBBLEWRAP" {
			return env
		}
	}
	out := make([]driver.EnvBinding, len(env), len(env)+1)
	copy(out, env)
	return append(out, driver.EnvBinding{Name: "IS_SANDBOX", Value: "1"})
}

// wantsInteractiveClaude reports whether the policy explicitly asks for
// stream-json bidirectional mode. We deliberately look at the raw
// policy fields (not EffectiveHumanDecisionPolicy) so that a zero-value
// policy stays in observational mode — otherwise the default
// (PlanReview=Ask + Permission=Ask) would silently promote every Claude run
// into interactive mode.
//
// Interactive mode engages for every explicit plan/question decision and for
// permission Ask/AutoReject. Those values require an actual control_request /
// control_response exchange; observational print mode cannot prove that the
// requested per-kind decision was honoured. Permission auto-approve alone can
// use Claude's native one-shot bypass flag.
func wantsInteractiveClaude(p driver.HumanDecisionPolicy) bool {
	return p.Permission == driver.HumanDecisionAsk ||
		p.Permission == driver.HumanDecisionAutoReject ||
		p.PlanReview == driver.HumanDecisionAsk ||
		p.PlanReview == driver.HumanDecisionAutoApprove ||
		p.PlanReview == driver.HumanDecisionAutoReject ||
		p.Question == driver.QuestionAsk ||
		p.Question == driver.QuestionAutoReject
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
// adaptor.Agent, which always passes a decision-capable unified event sink).
var errClaudeInteractiveSinkRequired = errors.New("claude interactive mode requires a DecisionCapableSink; adaptor.Agent provides one automatically — this error usually means the driver was invoked directly")

// Keep resume-rejection detection deliberately narrow: a false positive can
// make ContinueOrStart abandon a healthy provider conversation and retry as a
// fresh session. These phrases cover Claude CLI's unavailable-conversation
// diagnostics while auth/network/model failures remain ordinary failures.
var claudeResumeRejectedRE = regexp.MustCompile(`(?i)^(?:error:\s*)?(?:no conversation found with session id:\s*\S+|conversation with id \S+ not found|failed to resume session)\.?$`)

func isClaudeResumeRejected(parts ...string) bool {
	for _, part := range parts {
		for _, line := range strings.Split(part, "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
			if claudeResumeRejectedRE.MatchString(line) {
				return true
			}
		}
	}
	return false
}

func chooseCWD(cfg CommonConfig, workspace driver.WorkspaceLease) string {
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
