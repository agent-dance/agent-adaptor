# Profile Resource Provider Matrix

> Status: v1 contract and provider-capability reference. This file records the research evidence behind the final public profile-resource surface for Codex, Claude Code, and Cursor. The contract uses portable host intent where possible, typed extended capabilities where providers expose a stable native surface, explicit fallback or unsupported states where they do not, and provider-native escape hatches for the long tail.

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
| Cursor | Public docs cover Rules (`.cursor/rules`, user rules, `AGENTS.md`, and the older `.cursorrules` format) and the Agent CLI. Local bundled Cursor guides document `.cursor/agents/*.md` / `~/.cursor/agents/*.md` subagents with YAML frontmatter and `.cursor/hooks.json` / `~/.cursor/hooks.json` hooks with command/prompt handlers, event names, matchers, timeout, `failClosed`, and event-specific outputs. The January 8, 2026 changelog confirms CLI rules/MCP management and hook performance/behavior updates. |

## Support Levels

The public API models support with these levels. Capability reporting is per provider and per field, not just per resource kind.

| Level | Meaning |
|---|---|
| `portable_core` | Public intent every built-in provider can honor natively or through a first-class documented profile/rules surface. This is safe for examples and default docs. |
| `portable_extended` | Public typed intent with stable support in one or more providers and a defined fallback/error behavior in the rest. This should stay in core when the host intent is provider-neutral. |
| `native_escape` | Provider-native blob/source/config passed through without cross-provider translation. This is public as an escape hatch but never reported as portable. |
| `fallback` | Adapter can approximate the host intent, usually by prompt injection, script-side filtering, or per-run CLI flags. Snapshot must say fallback. |
| `unsupported` | Provider lacks a stable surface for this field. Adapter must report error/warning according to the caller's unsupported policy; it must not silently succeed. |

## Historical Local Smoke Evidence

The following results were recorded on the research machine with the CLI versions listed above. They are historical environment evidence, not the current release gate. The example now lives at `examples/profiles/resources`; reruns must use the current path and record the current CLI/auth state independently.

| Current equivalent command | Historically recorded result | Meaning |
|---|---|---|
| `go test ./...` | pass | repository-wide verification, including example compilation |
| `go run ./examples/profiles/resources -agent=codex -timeout=2m` | pass | local profile-resource smoke completed |
| `go run ./examples/profiles/resources -agent=claude -timeout=1m` | fail: `Not logged in` | historical environment/auth gap, not a profile-resource regression |
| `go run ./examples/profiles/resources -agent=cursor -timeout=1m` | fail: `no healthy local cursor CLI command found` | historical environment/CLI gap, not a profile-resource regression |

A successful smoke prints `before_sync` and `after_sync` snapshots plus two successful `Run` results. A provider that cannot start because of local auth or CLI availability must be recorded as an environment gap rather than as proof of either profile-resource success or regression.

## Capability Matrix

| Resource | Public capability envelope | Codex | Claude Code | Cursor |
|---|---|---|---|---|
| `agents` | `portable_core`: key/runtime name, description, and instruction/system-prompt body. `native_escape`: source file plus stable source fingerprint. `portable_extended`: model, reasoning/effort, tool policy, permission/sandbox/isolation hints, MCP server scope, skills, hooks-on-agent, native data, and metadata where the Driver can materialize them. | Native custom agents with TOML files. The built-in materializer maps model, reasoning effort, and sandbox; tool policy, permission mode, MCP servers, skills, and agent-local hooks are reported as unmapped. | Native subagents with Markdown/YAML or `--agents` JSON. The built-in materializer maps tools, disallowed tools, model, permission mode, MCP servers, skills, and effort; sandbox and agent-local hooks are reported as unmapped. | Native subagents per bundled guide with Markdown/YAML and required name/description; body is system prompt. The built-in materializer reports extended agent fields as unmapped. |
| `hooks` | `portable_core`: command handler, canonical event, typed matcher, timeout, and disabled state. `portable_extended`: prompt, HTTP, MCP-tool, and agent handlers; fail policy, status label, handler environment, and shortcut events where the Driver can represent them. | Native command hooks; events include session start, pre/post tool, permission request, prompt submit, and stop. Regex matcher, timeout, and status message are available. The built-in materializer reports handler environment and unsupported extended handlers explicitly. | Native hooks with command, HTTP, MCP-tool, prompt, and agent handlers, broad events, matcher semantics, filtering, and output decision control. The built-in materializer reports handler environment and any unmapped policy fields explicitly. | Bundled guides document command and prompt hooks, broad events, regex matchers, timeout, `failClosed`, and loop limits. The built-in materializer reports handler environment and unsupported handler kinds explicitly. |
| `instructions` | `portable_core`: instruction material enters effective context; source can be path or inline content; fingerprint participates in resume guard; snapshot states distinguish native/file managed, prompt injected, and unsupported. `portable_extended`: scope hints, path-scoped rules, append vs replace, provider-specific rules format. | Implemented profile-native materialization to `$CODEX_HOME/AGENTS.md`; `replace` mode maps to `$CODEX_HOME/AGENTS.override.md`. Prompt injection remains fallback for run-scoped bundles. | Implemented profile-native materialization to `$CLAUDE_CONFIG_DIR/CLAUDE.md`. Prompt injection remains fallback for run-scoped bundles. | Profile sync remains fallback because public CLI docs expose project rules and user settings, but no stable profile-level rules file. `Run` materializes project/local scoped bundles to `<workspace>/.cursor/rules/<id>.mdc`; other scopes remain prompt fallback. |
| `config` | Public config uses Driver-declared capability patches rather than arbitrary file/section writes. Each capability's support level is reported by the bound Driver; an explicit `NativeConfigPatch` is always `native_escape`. | Current allowlist materializes `model`, `reasoning_effort`, `sandbox`, and `approval` into `config.toml`. | Current allowlist materializes `model`, `effort`, `permission`, and `env` into `settings.json`. | Current allowlist materializes `sandbox`, `approval`, `permissions`, and `display` into `cli-config.json`; model remains unsupported because the bundled guide marks model fields as model-picker managed. |

## Final v1 Public Contract

Application-facing declarations live in `profile`. Driver-facing normalized declarations live in `driver`. The two packages intentionally own separate concrete types so application code does not depend on the Driver SPI; the root package performs the single conversion between them.

### SubAgent and AgentSpec

The application-facing shape is exactly:

```go
type SubAgent struct {
	Key               string
	RuntimeName       string
	Description       string
	Instructions      string
	SourcePath        string
	SourceFingerprint string

	Model           string
	ReasoningEffort string
	ToolPolicy      *ToolPolicy
	PermissionMode  string
	SandboxMode     string
	MCPServers      []string
	Skills          []string
	Hooks           []Hook

	Native   map[string]any
	Metadata map[string]string
}

type ToolPolicy struct {
	Allow []string
	Deny  []string
}
```

`driver.AgentSpec` has the same fields and ordering, with `*driver.AgentToolPolicy` and `[]driver.HookSpec` as the SPI-owned nested types.

Rules:

- `Instructions` is the only inline sub-agent instruction body.
- `SourcePath` and `Instructions` are mutually exclusive. A source path is provider-native material and is not translated across providers.
- `SourceFingerprint` is supplied by an advanced host or computed from the source content before execution. It participates in profile and Thread compatibility fingerprints.
- Extended fields are accepted only where the bound Driver can materialize them; unsupported intent is reported rather than silently presented as managed.

### Hook and HookSpec

The application-facing shape is exactly:

```go
type Hook struct {
	Key         string
	Event       HookEvent
	MatcherSpec HookMatcher
	Handler     HookHandler

	Timeout       time.Duration
	FailPolicy    HookFailPolicy
	StatusMessage string
	Disabled      bool

	Native   map[string]any
	Metadata map[string]string
}

type HookMatcher struct {
	Subject HookMatcherSubject
	Syntax  HookMatcherSyntax
	Pattern string
}

type HookHandler struct {
	Type    HookHandlerType
	Command string
	Args    []string
	Env     map[string]string

	Prompt string
	URL    string
	Server string
	Tool   string
	Input  map[string]any
	Agent  string
}
```

`driver.HookSpec` has the same fields and ordering, using the corresponding `driver` event, matcher, handler, and fail-policy types.

Rules:

- `MatcherSpec` is the only matcher contract. Its `Subject`, `Syntax`, and `Pattern` preserve host intent without conflating provider-native matcher strings with canonical matching.
- `Handler` is the only action contract. Command, argv, and environment are `HookHandler.Command`, `HookHandler.Args`, and `HookHandler.Env`.
- `command` is `portable_core`; all three researched providers have a command/script hook surface.
- `prompt` is `portable_extended`; Claude and Cursor support it while Codex does not currently expose it.
- `http`, `mcp_tool`, and `agent` are `portable_extended` only where the Driver reports support; they are currently Claude-native rather than portable across all three.
- Drivers must safely encode command argv for the provider representation or reject an unrepresentable command.
- `Timeout`, `FailPolicy`, and `StatusMessage` remain explicit because provider behavior differs and must not be hidden.

### Hook Event Mapping

Canonical events are host-intent names, not provider event names:

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

### Instructions and InstructionsBundleRef

The application-facing shape is exactly:

```go
type Instructions struct {
	ID          string
	Path        string
	Content     string
	Fingerprint string
	Scope       InstructionScope
	Mode        InstructionMode
	Native      map[string]any
}
```

The Driver SPI mirrors it as:

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
- `Fingerprint`, when supplied, is the host's stable content identity. Otherwise the execution pipeline derives a fingerprint from the resolved instruction material.
- Profile state reports the actual support and materialization outcome, including `native_managed`, `file_managed`, `prompt_injected`, or `not_materialized` as applicable.

### Config

The application-facing shape is exactly:

```go
type ConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
}

type NativeConfigPatch struct {
	Provider string
	FileKind ConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}
```

The Driver SPI mirrors it as:

```go
type ProfileConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
}

type NativeConfigPatch struct {
	Provider string
	FileKind ProfileConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}
```

Rules:

- Exactly one of `Capability` or `Native` is required. Supplying both or neither is rejected before Driver execution.
- `Capability` is validated against the profile-config capabilities implemented for the bound Driver. An unknown capability is reported as unsupported; it is not converted into an arbitrary file write.
- `NativeConfigPatch` owns `Provider`, `FileKind`, `Path`, `Section`, and native `Values`. A non-empty `Provider` restricts the patch to that Driver; an empty provider targets the already-bound Driver.
- Outer `Values` is the portable capability payload and also supplies native values when `Native.Values` is empty. `Native.Values` takes precedence when explicitly populated.
- Stable capability names currently materialized by the built-in Drivers are recorded in the capability matrix above; support and materialization remain observable through `Agent.ProfileState`.
- Native patches are explicit escape hatches and are excluded from portable examples.
- Secret-bearing config values must be environment references or secret handles; manifest must never persist raw secret values.

## Change Gates

Before changing a field or marking additional provider support as managed:

1. Update this matrix with provider-specific file paths, config keys, event names, and local CLI versions.
2. Add a temp-profile smoke test that writes the minimum resource and verifies the provider accepts or loads it.
3. Add Driver conformance covering `portable_core`, `portable_extended`, `fallback`, `native_escape`, and `unsupported`.
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
