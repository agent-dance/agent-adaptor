// Package opencode integrates OpenCode's official local server and run JSON
// protocols. Resolved streaming, approvals, MCP and native schemas use a private
// authenticated loopback server. Other batch invocations use run --format json.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// DriverType is the stable OpenCode driver identifier.
const DriverType = "opencode"

// Config configures the local OpenCode CLI (tested with 1.14.30). Custom API
// endpoints and credentials use native opencode.json configuration and Env.
// Explicit Threads reuse an owned private server; WithSpawn forces a fresh
// process. Stateless calls own a single invocation process. Pure disables
// external plugins through native flags. Replacement instructions use native
// agent.prompt in a private server: a discovery process is stopped before the
// configured execution process starts, and native delivery is checked before
// any prompt. Subsequent compatible Thread turns reuse the execution process.
// MCP connections are installed through the native server API with complete
// values, avoiding inherited headers and environments from config deep merging.
// Profile agents use native subagent configuration for instructions, model,
// reasoning variant and exact tool permissions. SyncProfile persists owned
// JSONC entries; Run/Stream use process-local overlays and verify resolved
// settings before prompting. SourcePath imports a native Markdown subagent's
// body, model/variant, ordered permissions and supported native options. Only
// Key, RuntimeName, SourceFingerprint and Metadata may accompany SourcePath.
// Captured values survive removal of the original source and settings cloning;
// templates remain literal and permission ordering is checked before prompting.
// Skill preloading, MCP references, sandbox and hooks remain unsupported. Native config
// merges that retain removed fields fail before prompt delivery.
// Explicit Ask/auto-deny also gates native child agents, preserving deny rules.
// The native session permission API appends rules. Each turn replays its resolved
// permissions over the prior SDK segment and verifies native delivery, retaining
// the original checkpoint baseline. Saved project permissions are checked with
// the native read-only db command before the first gated prompt on a process.
// Nonempty saved grants and unavailable/malformed probes fail before prompting;
// the Driver never deletes or rewrites those grants. Native-default policy keeps
// the provider's saved-grant behavior. Safely mediating saved grants remains a
// limitation of explicit Ask/auto-deny support.
// Task metadata and the session API must agree on child ownership before child
// events and approvals enter the same run. Child message/tool IDs are namespaced
// by native session; Transcript retains child ownership and Usage includes child
// steps. Result.Text remains the main assistant's answer. Resuming a child with
// session-specific allow rules under Ask/auto-deny is explicitly unsupported.
type Config struct {
	Command string // Executable; defaults to opencode.
	CWD     string // Working directory when the resolved workspace is empty.
	Env     []driver.EnvBinding
	Model   string // provider/model; adaptor.WithModel overrides it per invocation.
	Agent   string // Native primary agent name; empty preserves the native default.
	Variant string // Provider-specific reasoning variant.
	Pure    bool   // Disable external plugins (--pure).
}

// Driver captures an immutable configuration without performing I/O.
func Driver(cfg Config) driver.Driver {
	cfg.Env = append([]driver.EnvBinding(nil), cfg.Env...)
	return configuredDriver{cfg: cfg, pool: newServerPool()}
}

type configuredDriver struct {
	cfg  Config
	pool *serverPool
}

func (d configuredDriver) CloseProcesses(ctx context.Context) error {
	if d.pool == nil {
		return nil
	}
	return d.pool.close(ctx)
}

func (d configuredDriver) config(value any) (Config, error) {
	var c Config
	switch v := value.(type) {
	case nil:
		c = d.cfg
	case Config:
		c = v
	case *Config:
		if v == nil {
			return c, errors.New("opencode driver requires non-nil opencode.Config")
		}
		c = *v
	default:
		return c, errors.New("opencode driver requires opencode.Config")
	}
	c.Env = append([]driver.EnvBinding(nil), c.Env...)
	return c, nil
}
func (d configuredDriver) ValidateConfig(value any) error {
	c, err := d.config(value)
	if err != nil {
		return err
	}
	if c.Model != "" {
		provider, model, ok := strings.Cut(c.Model, "/")
		if !ok || provider == "" || model == "" {
			return errors.New("opencode: Model must have provider/model form")
		}
	}
	return nil
}
func (configuredDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: DriverType, DisplayName: "OpenCode", ConfigSchema: &driver.ConfigSchema{Fields: []driver.ConfigField{
		{Name: "command", Label: "Command", Type: "text", Default: "opencode"},
		{Name: "cwd", Label: "Working directory", Type: "text"},
		{Name: "model", Label: "Model", Type: "text", Hint: "provider/model"},
		{Name: "agent", Label: "Native agent", Type: "text"},
		{Name: "variant", Label: "Reasoning variant", Type: "text"},
		{Name: "pure", Label: "Disable external plugins", Type: "toggle"},
	}}, Sessions: driver.SessionCapability{SupportsResume: true}, Workspace: driver.WorkspaceCapability{Supported: true},
		Process:          driver.ProcessCapability{Persistent: true},
		Skills:           driver.SkillCapability{Supported: true, Mode: driver.SkillSyncPersistent},
		MCP:              driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
		Instructions:     driver.InstructionsCapability{Supported: true},
		RunPolicyCaps:    driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}, PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}, Question: driver.QuestionSupport{Ask: true, AutoReject: true}},
		StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true, Notes: "Native server format.json_schema; validated prompt fallback is resolved by core."},
	}
}
func (configuredDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, TokenLevel: true, Reasoning: true, HITL: true}
}
func (configuredDriver) SessionCodec() driver.SessionCodec {
	return driverutil.NativeSessionCodec("opencode/run-json/v1")
}
func (d configuredDriver) SessionConfigFingerprint() (string, error) {
	return driver.CanonicalSessionConfigFingerprint("opencode/config/v1", d.cfg)
}

func (d configuredDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if d.pool != nil && d.pool.isClosed() {
		return driver.Response{}, errServerPoolClosed
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
	if req.ModelOverride != "" {
		c.Model = req.ModelOverride
		if err := d.ValidateConfig(c); err != nil {
			return driver.Response{}, err
		}
	}
	if req.ProfilePayload.Declared.Hooks || req.ProfilePayload.Declared.Config {
		return driver.Response{}, errors.New("opencode: portable profile hooks and config patches are unsupported; use native profile configuration")
	}
	_, preparedAgents, err := agentProfilePlan("validate", req.ProfilePayload.Agents)
	if err != nil {
		return driver.Response{}, err
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
	agents, err := resolveProfileAgents(req, profile.Profile.Dir, preparedAgents)
	if err != nil {
		return driver.Response{}, err
	}
	guards["profile_agents"], err = driver.CanonicalSessionConfigFingerprint("opencode/profile-agents/v1", agents)
	if err != nil {
		return driver.Response{}, err
	}
	_, _, guards["profile_mcp"], err = resolveProfileMCP(req, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	files := []string{filepath.Join(profile.Profile.Dir, "opencode.json"), filepath.Join(profile.Profile.Dir, "opencode.jsonc"), filepath.Join(profile.Profile.Dir, "AGENTS.md")}
	if configPath, _ := driverutil.ResolvedEnvValue(profile.Bindings, "OPENCODE_CONFIG"); configPath != "" {
		files = append(files, configPath)
	}
	guards["native_config"], err = driverutil.NativeFilesFingerprint("opencode/native/v1", files...)
	if err != nil {
		return driver.Response{}, err
	}
	content, _ := driverutil.ResolvedEnvValue(profile.Bindings, "OPENCODE_CONFIG_CONTENT")
	guards["native_overlay"], err = driver.CanonicalSessionConfigFingerprint("opencode/overlay/v1", content)
	if err != nil {
		return driver.Response{}, err
	}
	dataHome, _ := driverutil.ResolvedEnvValue(profile.Bindings, "XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(skillruntime.ResolveHome(profile.Bindings), ".local", "share")
	}
	guards["data_home"], err = filepath.Abs(dataHome)
	if err != nil {
		return driver.Response{}, err
	}
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
	var disabledMCP []string
	agents, err = resolveProfileAgents(req, profile.Profile.Dir, preparedAgents)
	if err != nil {
		return driver.Response{}, err
	}
	guards["profile_agents"], err = driver.CanonicalSessionConfigFingerprint("opencode/profile-agents/v1", agents)
	if err != nil {
		return driver.Response{}, err
	}
	req.MCP, disabledMCP, guards["profile_mcp"], err = resolveProfileMCP(req, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	guards["native_skills"], err = skillruntime.ProfileSkillsFingerprint(ctx, profile.Profile.Dir)
	if err != nil {
		return driver.Response{}, err
	}
	guards["native_config"], err = driverutil.NativeFilesFingerprint("opencode/native/v1", files...)
	if err != nil {
		return driver.Response{}, err
	}
	server := wantsServer(req) || agents.active()
	env, cleanup, err := runEnvironment(req, profile.Bindings, server, disabledMCP...)
	if err != nil {
		return driver.Response{}, err
	}
	defer cleanup()
	env, err = agents.environment(env)
	if err != nil {
		return driver.Response{}, err
	}
	if server {
		return d.runServer(ctx, c, req, sink, cwd, env, guards, disabledMCP, agents)
	}
	args := []string{"run", "--format", "json", "--thinking"}
	if c.Pure {
		args = append(args, "--pure")
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Agent != "" {
		args = append(args, "--agent", c.Agent)
	}
	if c.Variant != "" {
		args = append(args, "--variant", c.Variant)
	}
	stateful := req.Session != nil && req.Session.Mode != driver.SessionStateless
	if stateful && req.Session.State != nil && req.Session.State.ResumeID != "" {
		args = append(args, "--session", req.Session.State.ResumeID)
		if req.Session.Mode == driver.SessionFork {
			args = append(args, "--fork")
		}
	}
	if req.Policy.HumanDecision.Permission == driver.HumanDecisionAutoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}
	command := c.Command
	if command == "" {
		command = "opencode"
	}
	prompt := req.Prompt
	if prefix := driverutil.RuntimePromptPrefix(req.Runtime); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}
	p := newParser(sink, req.Streaming)
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
	result, runErr := clihelper.Run(ctx, clihelper.CommandRequest{Command: command, Args: args, CWD: cwd, Env: env, Prompt: prompt, Observe: driverutil.ObserveStdout(&p.lines, p.line)}, sink)
	p.lines.Flush(p.line)
	if stateful && req.Session.Mode == driver.SessionFork && req.Session.State != nil && p.sessionID == req.Session.State.ResumeID {
		runErr = errors.Join(runErr, fmt.Errorf("opencode: fork returned the parent session identifier"))
	}
	response := p.finish(result, runErr)
	response.Provider, response.Model, _ = strings.Cut(c.Model, "/")
	if stateful && response.Checkpoint != nil {
		response.Checkpoint.State.Data = guards
	} else {
		response.Checkpoint = nil
	}
	if stateful && req.Session.State != nil && !p.started && result.ExitCode != 0 && strings.Contains(result.RawStreams.Stderr, "Session not found") {
		return response, &engine.ResumeRejectedError{Reason: "OpenCode session is unavailable"}
	}
	return response, errors.Join(runErr, p.events.Err)
}
