# Profile Resource Provider Matrix

> Status: maximum-capability research baseline. This file records the evidence used to design the public profile-resource surface for Codex, Claude Code, and Cursor. The goal is not the smallest common denominator. The goal is a portable capability envelope: common host intent where possible, typed extended capabilities when at least one provider has a stable native surface, explicit fallback/unsupported states when a provider cannot honor a field, and provider-native escape hatches for the long tail.

## Research Scope

Reviewed local tools:

| Provider | Local evidence |
|---|---|
| Codex | `codex-cli 0.125.0`; `codex features list` reports `codex_hooks` and `multi_agent` as stable/enabled; `codex --help` exposes config overrides, MCP, plugins, profile selection, sandbox, approval policy. |
| Claude Code | `claude 2.1.119`; `claude --help` exposes `--agent`, `--agents`, `--settings`, `--mcp-config`, `--include-hook-events`, `--system-prompt`, `--append-system-prompt`, `--permission-mode`, `--model`, and `--effort`; `claude agents` lists configured agents. |
| Cursor | `cursor agent 2026.01.28-fd13201`; `cursor agent --help` exposes model selection, MCP management, rule generation, sandbox, modes, resume, and print/stream output; bundled guides under `~/.cursor/skills-cursor/` document Cursor hooks and subagents. |

Primary documentation used:

| Provider | Evidence summary |
|---|---|
| Codex | Custom agents live under `~/.codex/agents/` or `.codex/agents/` and require `name`, `description`, and `developer_instructions`; optional fields include model, reasoning effort, sandbox, MCP servers, and skills. Hooks are configured in `hooks.json` or inline `[hooks]`, support command handlers, event groups, regex matchers, timeout, and lifecycle events such as `SessionStart`, `PreToolUse`, `PermissionRequest`, `PostToolUse`, `UserPromptSubmit`, and `Stop`. `config.toml` exposes model, profile, sandbox, approval, permission, shell environment, skills, MCP, tools, and instruction-file keys. |
| Claude Code | Subagents are Markdown files with YAML frontmatter or `--agents` JSON; required fields are `name` and `description`, with body/`prompt` as system prompt and optional fields for tools, model, permission mode, MCP servers, hooks, skills, effort, isolation, etc. Hooks are JSON settings with event -> matcher group -> handler nesting; handler types include command, http, mcp_tool, prompt, and agent. Settings are hierarchical JSON files with user/project/local/managed scopes and include permissions, env, sandbox, hook policy, MCP policy, and more. `CLAUDE.md` and `.claude/rules/` are persistent instruction surfaces. |
| Cursor | Public docs cover Rules (`.cursor/rules`, user rules, `AGENTS.md`, legacy `.cursorrules`) and the Agent CLI. Local bundled Cursor guides document `.cursor/agents/*.md` / `~/.cursor/agents/*.md` subagents with YAML frontmatter and `.cursor/hooks.json` / `~/.cursor/hooks.json` hooks with command/prompt handlers, event names, matchers, timeout, `failClosed`, and event-specific outputs. The January 8, 2026 changelog confirms CLI rules/MCP management and hook performance/behavior updates. |

## Support Levels

The public API should model support with these levels. Capability reporting must be per provider and per field, not just per resource kind.

| Level | Meaning |
|---|---|
| `portable_core` | Public intent every built-in provider can honor natively or through a first-class documented profile/rules surface. This is safe for examples and default docs. |
| `portable_extended` | Public typed intent with stable support in one or more providers and a defined fallback/error behavior in the rest. This should stay in core when the host intent is provider-neutral. |
| `native_escape` | Provider-native blob/source/config passed through without cross-provider translation. This is public as an escape hatch but never reported as portable. |
| `fallback` | Adapter can approximate the host intent, usually by prompt injection, script-side filtering, or per-run CLI flags. Snapshot must say fallback. |
| `unsupported` | Provider lacks a stable surface for this field. Adapter must report error/warning according to the caller's unsupported policy; it must not silently succeed. |

## Capability Matrix

| Resource | Public capability envelope | Codex | Claude Code | Cursor |
|---|---|---|---|---|
| `agents` | `portable_core`: key/runtime name, description, instruction/system-prompt body, source file passthrough, metadata. `portable_extended`: model, reasoning/effort, tool policy, permission/sandbox/isolation hints, MCP server scope, skills, hooks-on-agent when provider supports it. | Native custom agents with TOML files. Strong support for model, reasoning effort, sandbox, MCP servers, skills config. Tool policy should map through sandbox/permissions only where documented; otherwise unsupported. | Native subagents with Markdown/YAML or `--agents` JSON. Strong support for tools, disallowed tools, model, permission mode, MCP servers, hooks, max turns, skills, effort, background, isolation, memory, color. | Native subagents per bundled guide with Markdown/YAML and required `name`/`description`; body is system prompt. Additional fields beyond that are unverified and should be either native escape or unsupported until smoke-tested. |
| `hooks` | `portable_core`: command hook, event taxonomy, optional matcher, timeout, disabled flag, env keys, fail policy, status label. `portable_extended`: prompt hooks, HTTP hooks, MCP-tool hooks, agent hooks, richer output actions, shell/MCP/file/subagent shortcut events. | Native command hooks only in current docs; events include session start, pre/post tool, permission request, prompt submit, stop. Regex matcher, timeout, status message. No native prompt/http/mcp_tool/agent handlers found. | Native hooks with five handler types: command, http, mcp_tool, prompt, agent. Very broad event set, matcher semantics, `if` filtering, output decision control. | Bundled guide documents command and prompt hooks; broad event set including shell, MCP, file, subagent, prompt, compaction, stop, thought/response events; JS regex matcher, timeout, failClosed, loop limit. |
| `instructions` | `portable_core`: instruction material enters effective context; source can be path or inline content; fingerprint participates in resume guard; snapshot states distinguish native/file managed, prompt injected, and unsupported. `portable_extended`: scope hints, path-scoped rules, append vs replace, provider-specific rules format. | Implemented profile-native materialization to `$CODEX_HOME/AGENTS.md`; `replace` mode maps to `$CODEX_HOME/AGENTS.override.md`. Prompt injection remains fallback for run-scoped bundles. | Implemented profile-native materialization to `$CLAUDE_CONFIG_DIR/CLAUDE.md`. Prompt injection remains fallback for run-scoped bundles. | Profile sync remains fallback because public CLI docs expose project rules and user settings, but no stable profile-level rules file. `Run` materializes project/local scoped bundles to `<workspace>/.cursor/rules/<id>.mdc`; other scopes remain prompt fallback. |
| `config` | Public config must be adapter-declared capability patches, not arbitrary file/section writes. `portable_core`: model hint, reasoning/effort, sandbox/isolation, approval/permission policy, env policy, MCP policy where adapter declares support. `native_escape`: provider-native structured patch with explicit provider target. | Native `config.toml` has broad stable keys for model, reasoning, approval, sandbox, permissions, shell env, tools, skills, MCP, profiles, instructions. Current allowlist materializes `model`, `reasoning_effort`, `sandbox`, and `approval`. | Native hierarchical JSON settings expose agent selection, permissions, env, sandbox, hook policy, MCP policy, attribution, worktree settings, and managed settings. CLI flags provide per-session model/effort/system prompt overrides. Current allowlist materializes `model`, `effort`, `permission`, and `env`. | Cursor Agent CLI exposes sandbox, modes, MCP commands, rules generation, and local bundled `update-cli-config` documents `cli-config.json`. Current allowlist materializes `sandbox`, `approval`, `permissions`, and `display`; model remains unsupported because the bundled guide marks model fields as model-picker managed. |

## Target Public Spec Envelope

These target shapes are design guidance for the next API revision. The existing structs can be migrated incrementally, but examples and implementation plans should use these semantics.

### AgentSpec

Admit as a maximum-capability public resource after adding first-class fields:

```go
type AgentSpec struct {
	Key          string
	RuntimeName  string
	Description  string
	Instructions string
	SourcePath   string

	Model           string
	ReasoningEffort string
	ToolPolicy      *AgentToolPolicy
	PermissionMode  string
	SandboxMode     string
	MCPServers      []string
	Skills          []string
	Hooks           []HookSpec

	Native   map[string]any
	Metadata map[string]string
}
```

Rules:

- `Description` and `Instructions` are `portable_core`.
- `SourcePath` is `native_escape`: adapter may copy/reference a provider-native file but must not translate it to another provider.
- `Content` in the current API should be deprecated or documented as an alias for `Instructions`; it must not mean both common instruction body and provider-native blob.
- Extended fields are allowed in public API, but adapter capability checks decide whether to materialize, fallback, or reject.

### HookSpec

Admit as a maximum-capability public resource after replacing bare strings with typed event/matcher/handler fields:

```go
type HookSpec struct {
	Key           string
	Event         HookEvent
	MatcherSpec   HookMatcher
	Handler       HookHandler
	Timeout       time.Duration
	FailPolicy    HookFailPolicy
	StatusMessage  string
	Disabled      bool

	// Legacy v1 command-hook compatibility:
	Matcher string
	Command string
	Args    []string
	Env     map[string]string

	Native   map[string]any
	Metadata map[string]string
}
```

Core/extended split:

- `command` handler is `portable_core`; all three providers have a command/script hook surface.
- `prompt` handler is `portable_extended`; Claude and Cursor support it, Codex currently does not.
- `http`, `mcp_tool`, and `agent` handlers are `portable_extended` only where adapter reports support; currently Claude-native, not portable across all three.
- `Timeout` is public because all known hook systems either support it or need a deterministic adapter default.
- `FailPolicy` is public because Cursor exposes `failClosed`, and Claude/Codex have event-specific blocking behavior that must not be hidden.
- `Matcher` / `Command` / `Args` / `Env` remain as v1 command-hook compatibility fields; v2 callers should use `MatcherSpec` + `Handler`.
- `Args` can remain public for command hooks, but adapter must either safely quote to provider shell strings or reject unrepresentable argv.

### Hook Event Mapping

Canonical events should be host intent names, not provider event names:

| Canonical event | Codex | Claude Code | Cursor |
|---|---|---|---|
| `session_start` | `SessionStart` | `SessionStart` | `sessionStart` |
| `session_end` | unsupported | `SessionEnd` | `sessionEnd` |
| `prompt_submit` | `UserPromptSubmit` | `UserPromptSubmit` | `beforeSubmitPrompt` |
| `prompt_expand` | unsupported | `UserPromptExpansion` | unsupported/unverified |
| `pre_tool` | `PreToolUse` | `PreToolUse` | `preToolUse` |
| `post_tool` | `PostToolUse` | `PostToolUse` | `postToolUse` |
| `tool_failure` | fallback via `PostToolUse` plus script-side failure inspection | `PostToolUseFailure` | `postToolUseFailure` |
| `permission_request` | `PermissionRequest` | `PermissionRequest` | fallback via specific before-* events where supported |
| `pre_shell` | `PreToolUse` + `Bash` matcher | `PreToolUse` + `Bash` matcher | `beforeShellExecution` |
| `post_shell` | `PostToolUse` + `Bash` matcher | `PostToolUse` + `Bash` matcher | `afterShellExecution` |
| `pre_mcp` | `PreToolUse` + MCP tool matcher | `PreToolUse` + MCP tool matcher | `beforeMCPExecution` |
| `post_mcp` | `PostToolUse` + MCP tool matcher | `PostToolUse` + MCP tool matcher | `afterMCPExecution` |
| `pre_file_read` | `PreToolUse` + read matcher where supported | `PreToolUse` + `Read` matcher | `beforeReadFile` |
| `post_file_edit` | `PostToolUse` + edit/write matcher | `PostToolUse` + `Edit|Write` matcher | `afterFileEdit` |
| `subagent_start` | unsupported | `SubagentStart` | `subagentStart` |
| `subagent_stop` | unsupported | `SubagentStop` | `subagentStop` |
| `pre_compact` | unsupported | `PreCompact` | `preCompact` |
| `post_compact` | unsupported | `PostCompact` | unsupported/unverified |
| `stop` | `Stop` | `Stop` | `stop` |
| `stop_failure` | unsupported | `StopFailure` | unsupported/unverified |

### InstructionsBundleRef

Admit as public intent, but expand sources:

```go
type InstructionsBundleRef struct {
	ID          string
	Path        string
	Content     string
	Fingerprint string
	Scope       InstructionScope
	Mode        InstructionMode
	Native      map[string]any
}
```

Rules:

- `Path` and `Content` are mutually exclusive.
- `Scope` can express user/project/local/run when adapters can map it.
- `Mode` separates additive guidance from provider-specific replacement of built-in instructions.
- Snapshot states must include `native_managed`, `file_managed`, `prompt_injected`, and `unsupported`.

### Config

Do not admit arbitrary `Path` / `Section` as the main common API. Replace the default path with adapter-declared config capabilities:

```go
type ProfileConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
}
```

Rules:

- `Capability` is validated against `AgentAdmin.ConfigSchema()`.
- Stable public capabilities should include model, reasoning/effort, sandbox/isolation, approval/permission rules, env policy, MCP policy, hook policy, and instruction-file linkage when the adapter declares support.
- `NativeConfigPatch` may expose provider/file/section for advanced hosts, but must be explicit, provider-scoped, and excluded from portable examples.
- Secret-bearing config values must be environment references or secret handles; manifest must never persist raw secret values.

## Implementation Gates

Before implementing or marking a field as managed:

1. Update this matrix with provider-specific file paths, config keys, event names, and local CLI versions.
2. Add a temp-profile smoke test that writes the minimum resource and verifies the provider accepts or loads it.
3. Add adapter conformance covering `portable_core`, `portable_extended`, `fallback`, `native_escape`, and `unsupported`.
4. Update `docs/api-reference.md` with field-level support states, not just resource-level support.
5. Ensure `ProfileSnapshot` reports the exact materialization state and never presents fallback or unsupported as native managed.

## Sources

- Codex configuration reference: https://developers.openai.com/codex/config-reference
- Codex hooks: https://developers.openai.com/codex/hooks
- Codex subagents: https://developers.openai.com/codex/subagents
- Codex MCP: https://developers.openai.com/codex/mcp
- Codex rules: https://developers.openai.com/codex/rules
- Codex AGENTS.md: https://developers.openai.com/codex/guides/agents-md
- Claude Code settings: https://code.claude.com/docs/en/settings
- Claude Code hooks: https://code.claude.com/docs/en/hooks
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- Claude Code instructions/memory: https://code.claude.com/docs/en/memory
- Cursor CLI changelog: https://cursor.com/changelog/cli-jan-08-2026
- Cursor rules: https://docs.cursor.com/en/context
- Cursor CLI rules: https://docs.cursor.com/en/cli/using
- Local Cursor bundled hook guide: `~/.cursor/skills-cursor/create-hook/SKILL.md`
- Local Cursor bundled subagent guide: `~/.cursor/skills-cursor/create-subagent/SKILL.md`
