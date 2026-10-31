// Package pi integrates Pi Coding Agent's official RPC and JSON protocols with
// the adaptor Runner. Pi 0.86.1 or later is required. Explicit Threads reuse an
// owned RPC process; WithSpawn forces a fresh process. Native extension dialogs
// and tool approvals enter the single SDK Event sink. MCP connections and
// approval callbacks are rebound each turn through Pi's official extension API.
package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// DriverType is the stable Pi driver identifier.
const DriverType = "pi"

// Config configures Pi. Authentication and custom API endpoints use Pi's native
// models.json/auth.json files or Env. Invocation resources use private extensions;
// SyncProfile explicitly writes requested owned profile resources.
// Prefer adaptor.WithWorkspace for per-call workspace selection.
// Profile agents are file-managed declarations delivered through Pi's official
// extension API. The adaptor_subagent tool executes isolated native Pi child
// sessions, forwarding their events and approvals through the parent's sink.
// Instructions, model, thinking, exact built-in tool policies, skill preloading
// and MCP server selection are supported. Skills name SDK selections or installed
// profile skills by host key (preferred) or unambiguous runtime name. Their full
// SKILL.md contents are captured before prompting and added to the child system
// prompt, even with NoSkills. Files must be nonempty UTF-8 and at most 4 MiB.
// A nonempty MCPServers list selects exact keys from the resolved parent servers;
// empty inherits that resolved set. Child connections never rediscover durable
// servers outside that set. ToolPolicy still applies to those tools. Missing
// references fail before the parent is prompted. SourcePath imports Markdown
// with the official subagent example's name/description/model/tools fields;
// the body uses the same child system prompt mapping as Instructions. Imported
// values are captured before profile initialization and persisted without a
// source-file dependency. Only Key, RuntimeName, SourceFingerprint and Metadata
// may accompany SourcePath. Permission overrides, sandbox, hooks and Native
// agent fields are rejected.
// Child output is audited separately in
// Metadata["pi.child_runs"] and included in complete Raw streams and total Usage.
// Children do not create public Threads or additional run lifecycles.
type Config struct {
	Command      string // Executable; defaults to pi.
	CWD          string // Working directory when the resolved workspace is empty.
	Env          []driver.EnvBinding
	Provider     string   // --provider; empty preserves the native default.
	Model        string   // --model; adaptor.WithModel overrides it per invocation.
	Thinking     string   // off, minimal, low, medium, high, xhigh, or max.
	Tools        []string // Optional native tool allowlist (--tools).
	NoTools      bool     // Disable native tools; cannot be combined with Tools.
	Extensions   []string // Explicit Pi extension paths (--extension).
	NoExtensions bool     // Disable extension discovery; explicit Extensions still load.
	NoSkills     bool     // Disable native discovery; SDK-selected skills still load.
	SessionDir   string   // Optional native session storage directory.
}

// Driver captures an immutable configuration without performing I/O.
func Driver(cfg Config) driver.Driver {
	return configuredDriver{cfg: cloneConfig(cfg), pool: newRPCPool()}
}

type configuredDriver struct {
	cfg  Config
	pool *rpcPool
}

func (d configuredDriver) CloseProcesses(ctx context.Context) error {
	if d.pool == nil {
		return nil
	}
	return d.pool.close(ctx)
}

func cloneConfig(c Config) Config {
	c.Env = append([]driver.EnvBinding(nil), c.Env...)
	c.Tools = append([]string(nil), c.Tools...)
	c.Extensions = append([]string(nil), c.Extensions...)
	return c
}

func (d configuredDriver) config(value any) (Config, error) {
	if value == nil {
		return cloneConfig(d.cfg), nil
	}
	switch c := value.(type) {
	case Config:
		return cloneConfig(c), nil
	case *Config:
		if c != nil {
			return cloneConfig(*c), nil
		}
	}
	return Config{}, errors.New("pi driver requires pi.Config")
}

func (d configuredDriver) ValidateConfig(value any) error {
	c, err := d.config(value)
	if err != nil {
		return err
	}
	switch c.Thinking {
	case "", "off", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("pi: invalid thinking level %q", c.Thinking)
	}
	if c.NoTools && len(c.Tools) > 0 {
		return errors.New("pi: Tools and NoTools are mutually exclusive")
	}
	for _, name := range c.Tools {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, ",\x00\r\n") {
			return fmt.Errorf("pi: invalid tool name %q", name)
		}
	}
	for _, path := range c.Extensions {
		if strings.TrimSpace(path) == "" {
			return errors.New("pi: extension path is empty")
		}
	}
	return nil
}

func (configuredDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type: DriverType, DisplayName: "Pi Coding Agent",
		ConfigSchema: &driver.ConfigSchema{Fields: []driver.ConfigField{
			{Name: "command", Type: "text", Label: "Command", Default: "pi"},
			{Name: "cwd", Type: "text", Label: "Working directory"},
			{Name: "model", Type: "text", Label: "Model"},
			{Name: "provider", Type: "text", Label: "Provider"},
			{Name: "thinking", Type: "text", Label: "Thinking level"},
			{Name: "session_dir", Type: "text", Label: "Session directory"},
		}},
		Sessions:     driver.SessionCapability{SupportsResume: true},
		Process:      driver.ProcessCapability{Persistent: true},
		Workspace:    driver.WorkspaceCapability{Supported: true},
		Instructions: driver.InstructionsCapability{Supported: true},
		Skills:       driver.SkillCapability{Supported: true, Mode: driver.SkillSyncPersistent},
		MCP:          driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true},
		},
		StructuredOutput: driver.StructuredOutputCapability{JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true, Notes: "Pi RPC and JSON modes have no native output-schema constraint; core validates the prompted JSON."},
	}
}

func (configuredDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, TokenLevel: true, Reasoning: true, ToolCallArgs: true, HITL: true}
}
func (configuredDriver) SessionCodec() driver.SessionCodec {
	return driverutil.NativeSessionCodec("pi/json/v1")
}
func (d configuredDriver) SessionConfigFingerprint() (string, error) {
	return driver.CanonicalSessionConfigFingerprint("pi/config/v1", d.cfg)
}

func resolveProfile(ctx context.Context, c Config, selection *driver.ProfileSelection, initialize bool) (skillruntime.ProfileResolution, error) {
	if err := ctx.Err(); err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	native := filepath.Join(skillruntime.ResolveHome(c.Env), ".pi", "agent")
	r, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{
		Bindings: c.Env, Selection: selection, EnvVar: "PI_CODING_AGENT_DIR", DefaultDir: native, NativeSharedDir: native,
		DedicatedSubdirs: []string{"skills", "sessions"}, SettingsFiles: []string{"settings.json", "models.json"}, MCPFiles: []string{mcpProfileFile}, SkillsDirs: []string{"skills"}, AuthFiles: []string{"auth.json"}, SkipInitialize: !initialize,
	})
	r.Profile.DriverType = DriverType
	if err == nil && initialize && selection != nil && selection.Mode == driver.ProfileModeClone && selection.Clone != nil && selection.Clone.IncludeSettings {
		cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = profilejson.Clone(cloneCtx, r.CloneSource, r.Profile.Dir, profilejson.ClonePlan{Files: []string{agentProfileFile}, KeepField: func(string) bool { return true }, Resources: []profilejson.Layout{agentProfileLayout(r.CloneSource)}})
	}
	return r, err
}

func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.execute(ctx, req, sink, nil)
}

func (d configuredDriver) execute(ctx context.Context, req driver.Request, sink driver.EventSink, scope *agentScope) (response driver.Response, runErr error) {
	if wantsInteractions(req.Policy.HumanDecision) {
		if _, ok := sink.(driver.DecisionCapableSink); !ok {
			return driver.Response{}, errors.New("pi: interactive policy requires a decision-capable sink")
		}
	}
	if d.pool != nil && d.pool.isClosed() {
		return driver.Response{}, errRPCPoolClosed
	}
	if err := d.ValidateConfig(req.Config); err != nil {
		return driver.Response{}, err
	}
	preparedInstructions, err := prepareInstructions(req.Instructions)
	if err != nil {
		return driver.Response{}, err
	}
	req.Instructions = preparedInstructions
	c, _ := d.config(req.Config)
	if req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative {
		return driver.Response{}, &driver.StructuredOutputUnsupportedError{Driver: DriverType, Reason: "Pi has no native JSON Schema constraint"}
	}
	if len(req.MCP.Servers) > 0 && c.NoTools {
		return driver.Response{}, errors.New("pi: NoTools conflicts with requested MCP tools")
	}
	if wantsInteractions(req.Policy.HumanDecision) && c.NoTools {
		return driver.Response{}, errors.New("pi: NoTools conflicts with interactive approval tools")
	}
	if req.ProfilePayload.Declared.Hooks || req.ProfilePayload.Declared.Config {
		return driver.Response{}, errors.New("pi: profile hooks and config patches are unsupported; use native profile files and Extensions")
	}
	req.ProfilePayload.Agents, err = prepareAgentPayload(req.ProfilePayload.Agents)
	if err != nil {
		return driver.Response{}, err
	}
	if _, err := agentProfileEntries(req.ProfilePayload.Agents); err != nil {
		return driver.Response{}, err
	}
	if req.ModelOverride != "" {
		c.Model = req.ModelOverride
	}
	cwd, err := driverutil.EffectiveCWD(c.CWD, req.Workspace)
	if err != nil {
		return driver.Response{}, err
	}
	profile, err := resolveProfile(ctx, c, req.Profile, false)
	if err != nil {
		return driver.Response{}, err
	}
	guards := driverutil.SessionGuards(req, cwd, profile.Profile.Dir)
	guards["profile_agents"], err = driverutil.NativeFilesFingerprint("pi/profile-agents/v1", filepath.Join(profile.Profile.Dir, agentProfileFile))
	if err != nil {
		return driver.Response{}, err
	}
	guards["profile_mcp"], err = driverutil.NativeFilesFingerprint("pi/profile-mcp/v1", filepath.Join(profile.Profile.Dir, mcpProfileFile))
	if err != nil {
		return driver.Response{}, err
	}
	guards["native_config"], err = driverutil.NativeFilesFingerprint("pi/native/v1", filepath.Join(profile.Profile.Dir, "models.json"), filepath.Join(profile.Profile.Dir, "settings.json"), filepath.Join(profile.Profile.Dir, "APPEND_SYSTEM.md"), filepath.Join(profile.Profile.Dir, "SYSTEM.md"), filepath.Join(profile.Profile.Dir, "AGENTS.md"), filepath.Join(profile.Profile.Dir, "AGENTS.override.md"))
	if err != nil {
		return driver.Response{}, err
	}
	sessionDir := c.SessionDir
	if sessionDir == "" {
		sessionDir, _ = driverutil.ResolvedEnvValue(profile.Bindings, "PI_CODING_AGENT_SESSION_DIR")
	}
	if sessionDir == "" {
		sessionDir = filepath.Join(profile.Profile.Dir, "sessions")
	}
	if !filepath.IsAbs(sessionDir) {
		sessionDir = filepath.Join(cwd, sessionDir)
	}
	guards["session_dir"] = filepath.Clean(sessionDir)
	guards["native_skills"], err = skillruntime.ProfileSkillsFingerprint(ctx, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	if err := driverutil.CheckSessionGuards(req, guards); err != nil {
		return driver.Response{}, err
	}
	profile, err = resolveProfile(ctx, c, req.Profile, true)
	if err != nil {
		return driver.Response{}, err
	}
	req.MCP, err = resolveProfileMCP(req, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	req.ProfilePayload.Agents, err = resolveProfileAgents(req, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	guards["resolved_agents"] = req.ProfilePayload.Agents.Fingerprint
	if err := driverutil.CheckSessionGuards(req, map[string]string{"resolved_agents": guards["resolved_agents"]}); err != nil {
		return driver.Response{}, err
	}
	agentResources, err := resolveAgentResources(ctx, req, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	guards["agent_resources"], err = driver.CanonicalSessionConfigFingerprint("pi/agent-resources/v1", agentResources)
	if err != nil {
		return driver.Response{}, err
	}
	if err := driverutil.CheckSessionGuards(req, map[string]string{"agent_resources": guards["agent_resources"]}); err != nil {
		return driver.Response{}, err
	}
	if len(req.ProfilePayload.Agents.Agents) > 0 && c.NoTools {
		return driver.Response{}, errors.New("pi: NoTools conflicts with subagents")
	}
	guards["profile_agents"], err = driverutil.NativeFilesFingerprint("pi/profile-agents/v1", filepath.Join(profile.Profile.Dir, agentProfileFile))
	if err != nil {
		return driver.Response{}, err
	}
	if len(req.MCP.Servers) > 0 && c.NoTools {
		return driver.Response{}, errors.New("pi: NoTools conflicts with persistent MCP tools")
	}
	guards["profile_mcp"], err = driverutil.NativeFilesFingerprint("pi/profile-mcp/v1", filepath.Join(profile.Profile.Dir, mcpProfileFile))
	if err != nil {
		return driver.Response{}, err
	}
	// A first profile clone may just have copied settings. Persist the bytes
	// actually delivered, while checking an existing checkpoint before writes.
	guards["native_skills"], err = skillruntime.ProfileSkillsFingerprint(ctx, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	guards["native_config"], err = driverutil.NativeFilesFingerprint("pi/native/v1", filepath.Join(profile.Profile.Dir, "models.json"), filepath.Join(profile.Profile.Dir, "settings.json"), filepath.Join(profile.Profile.Dir, "APPEND_SYSTEM.md"), filepath.Join(profile.Profile.Dir, "SYSTEM.md"), filepath.Join(profile.Profile.Dir, "AGENTS.md"), filepath.Join(profile.Profile.Dir, "AGENTS.override.md"))
	if err != nil {
		return driver.Response{}, err
	}
	env, err := driverutil.RuntimeEnvBindings(profile.Bindings, req.Runtime)
	if err != nil {
		return driver.Response{}, err
	}
	runCtx, abort := context.WithCancelCause(ctx)
	defer abort(nil)
	childCtx, stopChildren := context.WithCancel(runCtx)
	delegation := &delegationRun{ctx: childCtx, cancel: stopChildren, driver: d, config: cloneConfig(c), request: req, resources: agentResources, calls: map[string]bool{}}
	observed := &invocationSink{base: sink, owner: delegation}
	delegation.sink = observed
	sink = observed
	defer func() { delegation.finish(&response) }()
	stateful := req.Session != nil && req.Session.Mode != driver.SessionStateless
	// Native extensions can issue UI dialogs even without an explicit SDK
	// policy. RPC preserves those requests. JSON print is sufficient only for
	// stateless calls with native extension discovery explicitly disabled.
	useRPC := (stateful && req.Session.EngineSessionID != "") || !c.NoExtensions || len(c.Extensions) > 0 || len(req.ProfilePayload.Agents.Agents) > 0 || scope != nil
	var extension string
	var bridge *mcpBridge
	if useRPC {
		bridge, err = prepareBridge(runCtx, req, env, cwd, &c.Tools, sink, abort, scope)
		if err != nil {
			return driver.Response{}, err
		}
		if bridge != nil {
			bridge.delegation = delegation
			defer bridge.close()
		}
	} else {
		var closeMCP func()
		extension, env, closeMCP, err = prepareExtension(runCtx, req, env, cwd, &c.Tools, sink, abort)
		if err != nil {
			return driver.Response{}, err
		}
		defer closeMCP()
	}
	var args []string
	if extension != "" {
		args = append(args, "--extension", extension)
	}
	if c.Provider != "" {
		args = append(args, "--provider", c.Provider)
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Thinking != "" {
		args = append(args, "--thinking", c.Thinking)
	}
	if c.NoTools {
		args = append(args, "--no-tools")
	}
	if len(c.Tools) > 0 {
		args = append(args, "--tools", strings.Join(c.Tools, ","))
	}
	if c.NoExtensions {
		args = append(args, "--no-extensions")
	}
	if c.NoSkills {
		args = append(args, "--no-skills")
	}
	for _, path := range c.Extensions {
		args = append(args, "--extension", path)
	}
	if c.SessionDir != "" {
		args = append(args, "--session-dir", c.SessionDir)
	}
	if !stateful {
		args = append(args, "--no-session")
	} else if !useRPC && req.Session.State != nil && req.Session.State.ResumeID != "" {
		flag := "--session"
		if req.Session.Mode == driver.SessionFork {
			flag = "--fork"
		}
		args = append(args, flag, req.Session.State.ResumeID)
	}
	for _, skill := range req.Skills.Entries {
		path := filepath.Join(skill.SourcePath, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			return driver.Response{}, fmt.Errorf("pi skill %q: %w", skill.Key, err)
		}
		args = append(args, "--skill", path)
	}
	if ref := req.Instructions; ref != nil {
		content := ref.Content
		flag := "--append-system-prompt"
		if ref.Mode == driver.InstructionModeReplace {
			flag = "--system-prompt"
		}
		args = append(args, flag, content)
	}
	prompt := req.Prompt
	if prefix := driverutil.RuntimePromptPrefix(req.Runtime); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}
	if useRPC {
		return d.runRPC(runCtx, c, req, sink, cwd, env, args, prompt, guards, bridge)
	}
	args = append([]string{"--mode", "json", "--print"}, args...)
	command := c.Command
	if command == "" {
		command = "pi"
	}
	p := newParser(sink, req.Streaming)
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
	result, runErr := clihelper.Run(runCtx, clihelper.CommandRequest{Command: command, Args: args, CWD: cwd, Env: env, Prompt: prompt, Observe: driverutil.ObserveStdout(&p.lines, p.line)}, sink)
	runErr = errors.Join(runErr, context.Cause(runCtx))
	p.lines.Flush(p.line)
	if stateful && req.Session.Mode == driver.SessionFork && req.Session.State != nil && p.sessionID == req.Session.State.ResumeID {
		runErr = errors.Join(runErr, errors.New("pi: fork returned the parent session identifier"))
	}
	response = p.finish(result, runErr)
	if response.Provider == "" {
		response.Provider = c.Provider
	}
	if response.Model == "" {
		response.Model = c.Model
	}
	if stateful && response.Checkpoint != nil {
		response.Checkpoint.State.Data = guards
	} else {
		response.Checkpoint = nil
	}
	// Only the exact pre-prompt session lookup diagnostic permits core's one
	// resume fallback. Never replay a prompt after any agent event was observed.
	if stateful && req.Session.State != nil && !p.agentStarted && result.ExitCode != 0 && strings.Contains(result.RawStreams.Stderr, "No session found matching '") {
		return response, &engine.ResumeRejectedError{Reason: "Pi session is unavailable"}
	}
	return response, errors.Join(runErr, p.events.Err)
}
