# Workstream: CodeBuddy CLI Adapter Plan

> Status: research-backed implementation plan. Created from issue #3 research and follow-up local probes. This file is the durable planning record for adding a first-class `codebuddy` adapter without changing the core SDK execution model.

## 0. Goal

Add CodeBuddy CLI as a localized built-in adapter/binding:

- `codebuddy.New(cfg, opts...)`
- `codebuddy.NewAdapter()`
- `agentadaptor.CodeBuddyConfig`
- equivalent `Run` / `Start().Wait()` semantics
- adapter-owned protocol parsing
- separated `Output`, `RawStreams`, `Transcript`, `Summary`, and provider terminal `Result`
- explicit session behavior for stateless, start-new, continue/resume, fork, and failure cases supported by the CLI

The adapter must stay inside the existing SDK flow:

1. binding defaults + per-call overrides
2. `resolvedInvocation`
3. session coordination
4. adapter execution
5. checkpoint persistence and result archival

## 1. Confirmed CLI Facts

Evidence sources:

- Local `codebuddy --help` / `cbc --help` on this machine: initial installed command reported version `2.99.1`; it was upgraded to npm latest `2.117.2` on 2026-07-08 and re-probed.
- npm package metadata: `@tencent-ai/codebuddy-code` latest is `2.117.2`, with bins `codebuddy`, `cbc`, and `cbc-prewarm`.
- Official docs: [Installation](https://www.codebuddy.ai/docs/cli/installation), [Headless](https://www.codebuddy.ai/docs/cli/headless), [CLI Reference](https://www.codebuddy.ai/docs/cli/cli-reference), [TypeScript SDK Reference](https://www.codebuddy.ai/docs/cli/sdk-typescript), [SDK Sessions](https://www.codebuddy.ai/docs/cli/sdk-sessions), [IAM](https://www.codebuddy.ai/docs/cli/iam), [Settings](https://www.codebuddy.ai/docs/cli/settings), [MCP](https://www.codebuddy.ai/docs/cli/mcp), and [Skills](https://www.codebuddy.ai/docs/cli/skills).

Confirmed command surface:

| Capability | Evidence |
|---|---|
| Executables | `codebuddy` and `cbc` are bins of `@tencent-ai/codebuddy-code`. |
| Non-interactive mode | `-p` / `--print`. |
| Output formats | `--output-format text`, `json`, `stream-json`; only works with `--print`. |
| Input formats | `--input-format text` or `stream-json`; only works with `--print`. |
| Structured output | `--json-schema <schema>`. |
| Streaming deltas | `--include-partial-messages`, only with `--output-format stream-json`. |
| Resume | `-r` / `--resume [sessionId]`. |
| Continue latest | `-c` / `--continue`. |
| Fixed session ID | `--session-id <uuid>`. |
| Fork session | `--fork-session` with resume/continue. |
| Model | `--model <model>`, plus `CODEBUDDY_MODEL` in docs. |
| Effort | `--effort minimal|low|medium|high|xhigh|max` in `2.117.2`; initial `2.99.1` help listed `low|medium|high|xhigh`. |
| Max turns | `--max-turns <number>`. |
| Permissions | `--permission-mode`, `--dangerously-skip-permissions`, `--allowedTools`, `--disallowedTools`, `--tools`. |
| MCP | `--mcp-config <fileOrString>`, `--strict-mcp-config`, and `codebuddy mcp ...`. |
| Additional tool dirs | `--add-dir <directories...>`. |
| Instructions | `--system-prompt`, `--system-prompt-file`, `--append-system-prompt`. |
| Settings | `--settings <file-or-json>`, `--setting-sources <sources>`. |

Confirmed protocol shape from official SDK docs:

- `system` / `init` includes top-level `session_id`, `cwd`, `tools`, `mcp_servers`, `model`, `permissionMode`, `codebuddy_code_version`, `skills`, and `plugins`.
- `assistant` messages use content blocks.
- terminal `result` can include `session_id`, `duration_ms`, `num_turns`, `result`, `total_cost_usd`, `usage`, `permission_denials`, and optional `structured_output`.
- error terminal subtypes include `error_during_execution`, `error_max_turns`, and `error_max_budget_usd`.

Confirmed profile/auth differences from Claude Code:

- CodeBuddy uses `~/.codebuddy/settings.json`, project `.codebuddy/settings.json`, and project `.codebuddy/settings.local.json`.
- Auth can use `CODEBUDDY_API_KEY`, `CODEBUDDY_AUTH_TOKEN`, and `CODEBUDDY_INTERNET_ENVIRONMENT`.
- No Claude-style `CLAUDE_CONFIG_DIR` equivalent has been confirmed yet.

## 2. Claude-Like Reuse Boundary

CodeBuddy is close enough to Claude Code to reuse implementation shape, but not close enough to share code before fixtures lock the differences.

Reusable patterns:

- `New` / `NewAdapter` / `ValidateConfig` typed binding shape from `claude/driver.go`.
- `Run` order from `claude/driver.go`: config, model override, session guard, profile/materialization, runtime env, args, parser, `clihelper.Run`, parser finalize, `DriverRunResult`.
- parser separation of `Output`, `RawStreams`, `Transcript`, `Summary`, and `Result`.
- session codec round-trip and guard fingerprint over cwd/workspace/profile.
- adapter conformance suite pattern.

Do not blindly copy:

- Claude profile env and default directory.
- Claude credentials/config files.
- Claude `--permission-prompt-tool stdio` HITL flow.
- Claude-specific model/provisioning assumptions.
- Claude Bedrock/Vertex auth branches.
- Claude-specific MCP/config/hook/native instruction layouts unless CodeBuddy docs or probes confirm an equivalent.

## 3. Proposed File Plan

Core/public surface:

- `config_types.go`: add `CodeBuddyConfig { CommonConfig; Model string; Effort ThinkingEffort; MaxTurnsPerRun int }`.
- `config.go`: include `CodeBuddyConfig` in common config extraction.

New provider package:

- `codebuddy/driver.go`: adapter descriptor, admin hooks, argument builder, `Run`.
- `codebuddy/parser.go`: CodeBuddy protocol parser.
- `codebuddy/session_codec.go`: session params codec.
- `codebuddy/auth_probe.go`: CodeBuddy auth/config/profile checks.
- `codebuddy/skills.go`: profile-local skills snapshot/sync if `.codebuddy/skills` is confirmed as provider-native.
- `codebuddy/mcp.go`: MCP profile materialization if CodeBuddy settings layout is confirmed.
- `codebuddy/testdata/*.jsonl`: official or local probe fixtures.

Internal support switches:

- `internal/mcpruntime/resource.go`: add `codebuddy` layout only after MCP settings target is confirmed.
- `internal/profileconfig/*`: add CodeBuddy capability patches only after settings keys are confirmed.
- `internal/profileinstructions/instructions.go`: native instruction support only after file target is confirmed; otherwise keep prompt fallback.
- `internal/profileagents/agents.go`: native agents only if CodeBuddy agent definition format is confirmed.
- `internal/profilehooks/hooks.go`: native hooks only if hook schema is confirmed.

Docs/examples:

- `README.md`
- `README.zh-CN.md`
- `docs/api-reference.md`
- `docs/usage-guide.md`
- `examples/internal/exampleutil/live_agent.go`

## 4. Parser Contract

`codebuddyParser` owns all semantic parsing.

Rules:

- `RawStreams.Stdout` and `RawStreams.Stderr` come only from `clihelper.Run`.
- non-JSON stdout, invalid JSON stdout, and stderr may become transcript items, but never assistant-facing `Output`.
- `Output` is built only from assistant text content blocks.
- `Summary` prefers terminal `result` / `summary`, then last assistant text.
- `Result` stores the terminal result payload as a raw `map[string]any`.
- `Transcript` entries are produced only from CodeBuddy protocol events, not generic recursive JSON guessing.
- Checkpoints only use top-level official session fields such as `session_id`, `sessionId`, or `sessionID`.
- failure checkpoint behavior must be locked by tests after probes clarify whether CodeBuddy failed sessions are resumable.

Initial event mapping:

| CodeBuddy event | SDK transcript/result |
|---|---|
| `system` + `subtype:init` | `TranscriptInit`, capture model/session. |
| `assistant` text block | `TranscriptAssistant`, append to `Output`. |
| `assistant` thinking block | `TranscriptThinking`. |
| `assistant` tool_use block | `TranscriptToolCall`. |
| `user` tool_result block | `TranscriptToolResult`. |
| `result` success | terminal `Result`, `Summary`, `Usage`, `CostUSD`, checkpoint. |
| `result` error subtype | terminal `Result`, `Failure`, possible checkpoint only if official session exists. |
| `error` | `TranscriptFailure`, `FailureAgentError`. |
| unknown JSON protocol event | `TranscriptSystem` plus raw payload. |

## 5. Capability Defaults

Conservative first pass:

| Capability | First-pass declaration |
|---|---|
| Sessions | `SupportsResume: true`, if probes confirm `--resume <session_id>` works from captured `session_id`. |
| Streaming | native stream-json support; token/tool/reasoning flags depend on fixture evidence. |
| Structured output | native JSON Schema support for `Run` and `Start`; not with HITL until verified. |
| MCP | supported only after settings/profile materialization path is confirmed; CLI `--mcp-config` is confirmed. |
| Skills | likely supported through `.codebuddy/skills`, but first tests must prove materialization. |
| Instructions | prompt fallback is safe; native support needs confirmed target file or settings key. |
| Agents/hooks | unsupported until format is confirmed. |
| HITL Permission.Ask | unsupported in CLI `--print`; supported through SDK/control-mode `can_use_tool` after 2026-07-08 probes. |
| HITL PlanReview.Ask | unsupported in CLI `--print`; supported through SDK/control-mode `ExitPlanMode` + `can_use_tool` after 2026-07-08 probes. |
| HITL Question.Ask | unsupported in CLI `--print`; supported through SDK/control-mode `AskUserQuestion` + `can_use_tool` after 2026-07-08 probes. |
| AutoApprove | can map to `--dangerously-skip-permissions` or permission mode after probes pick one. |

Probe-adjusted notes:

- `--output-format json` on local CodeBuddy `2.99.1` and `2.117.2` emits a JSON array of frames, not a single terminal object.
- `--include-partial-messages` on `2.117.2` emits Claude-like `stream_event` frames with `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, and `message_stop`.
- Native JSON Schema output depends on the `StructuredOutput` tool. It fails semantically when tools are disabled with `--tools ""`, and succeeds with default tools by putting `structured_output` on the terminal `result` frame.
- `--permission-prompt-tool stdio` is not supported in local CodeBuddy `2.99.1` or `2.117.2`.
- In non-interactive `--print` mode, permission prompts are not surfaced as host-resolvable requests. Tool calls that need approval receive a `user.tool_result` error message.
- `-y` and `--permission-mode bypassPermissions` both set `system.init.permissionMode` to `bypassPermissions` and are verified AutoApprove mappings for write actions in `2.117.2`.

## 6. Verification Matrix

Probe commands should run from a temporary workspace, with prompts that ask for read-only behavior unless the probe explicitly needs a permission event.

| Probe | Purpose | Command shape | Expected evidence |
|---|---|---|---|
| version/help | Lock local CLI facts | `codebuddy --version`, `codebuddy --help` | command, version, flags. |
| text print | Minimal auth/model smoke | `codebuddy -p --tools "" --output-format text ...` | model call succeeds or auth error shape is captured. |
| json result | Terminal result shape | `codebuddy -p --tools "" --output-format json ...` | final JSON fields and `session_id`. |
| stream-json | JSONL protocol shape | `codebuddy -p --tools "" --output-format stream-json ...` | `system/assistant/result` frames. |
| structured output | Native schema | `codebuddy -p --tools "" --output-format json --json-schema ...` | `structured_output` location. |
| resume | Session continuity | second run with `--resume <session_id>` | accepted/resumed session evidence. |
| fork | Fork semantics | `--resume <session_id> --fork-session` | new session id or documented behavior. |
| permission/HITL | Uncertain HITL shape | prompt requiring safe file write in temp dir, without bypass flags | permission event shape, interactive behavior, timeout behavior. |
| stream input | Bidirectional uncertainty | `--input-format stream-json --output-format stream-json --replay-user-messages` | whether one-shot stdin frame exits and what ACK frames look like. |

## 7. Open Questions

1. Does local `codebuddy -p --output-format json` emit only a terminal result object, or a wrapper envelope? Answer: local `2.99.1` and `2.117.2` emit a JSON array frame envelope.
2. Does `stream-json` stdout exactly match the official SDK message schema? Answer: broadly yes for `system` / `assistant` / `user` / `result`, but local runs include CodeBuddy extension frames such as `file-history-snapshot` and `system.status`; `--include-partial-messages` adds Claude-like `stream_event` frames.
3. Does failed execution return a top-level resumable `session_id`, and should the SDK persist it? Answer: a max-turns failure returned a top-level `session_id`; persist policy still needs SDK-level decision and tests.
4. Which profile isolation knob should `WithDedicatedProfile` map to, if any?
5. Is `.codebuddy/skills` sufficient for SDK-managed persistent skills under a dedicated profile?
6. What is the native MCP settings JSON target for profile sync?
7. Does CodeBuddy expose a true non-UI HITL channel equivalent to Claude `control_request` / `control_response`? Answer: not via Claude's `--permission-prompt-tool stdio` and not in CLI `--print`; yes via CodeBuddy SDK/control mode (`--input-format=stream-json --output-format=stream-json` plus `control_request.initialize`). `AskUserQuestion` and `ExitPlanMode` are not host-resolvable in `--print`, but both are host-resolvable as `can_use_tool` control requests in SDK/control mode.
8. Which permission flag best represents `HumanDecisionAutoApprove`: `--dangerously-skip-permissions`, `--permission-mode bypassPermissions`, or both? Answer: both are verified in `2.117.2`; `-y` normalizes to `system.init.permissionMode = "bypassPermissions"`.

## 8. Probe Log

This section is intentionally updated as probes run.

### 2026-07-08 initial local facts

- `command -v codebuddy` found `/Users/blurooo/.xvm/data/node/npm-packages/bin/codebuddy`.
- `command -v cbc` found `/Users/blurooo/.xvm/data/node/npm-packages/bin/cbc`.
- `codebuddy --version` returned `2.99.1`.
- `npm view @tencent-ai/codebuddy-code name version bin dist-tags --json` returned package `@tencent-ai/codebuddy-code`, latest `2.117.2`, and bins `codebuddy`, `cbc`, `cbc-prewarm`.
- `codebuddy --help` confirmed the flag surface in §1.

### 2026-07-08 basic execution probes

Workspace: `/tmp/codebuddy-probe-12LlP1`.

`codebuddy -p --tools "" --output-format json ...`:

- exited `0`;
- emitted a JSON array frame envelope;
- included `message` frames, `file-history-snapshot`, `reasoning`, assistant `message`, and terminal `result`;
- terminal `result.session_id` was `c48ae7f5-c41d-4483-bcf6-48e7fc4e0181`;
- terminal `result.result` was the assistant-facing text.

`codebuddy -p --tools "" --output-format stream-json ...`:

- exited `0`;
- emitted NDJSON;
- first frame was `{"type":"system","subtype":"init",...}`;
- `system.init.session_id` and `result.session_id` matched;
- included CodeBuddy extension frames `system.status` and `file-history-snapshot`;
- assistant text arrived as a full `assistant.message.content[]` text block;
- terminal `result` included `usage`, `duration_ms`, `num_turns`, `permission_denials`, and `session_id`.

### 2026-07-08 structured output probes

`codebuddy -p --tools "" --output-format json --json-schema ...`:

- exited `0`, but did not produce native structured output;
- model attempted to use `StructuredOutput` but reported that no such tool was available;
- terminal `result` lacked `structured_output`.

`codebuddy -p --output-format json --json-schema ...` with default tools:

- exited `0`;
- emitted a `function_call` frame named `StructuredOutput`;
- emitted a matching `function_call_result`;
- terminal `result.structured_output` contained the schema-valid object;
- terminal `result.result` was ordinary assistant text (`Done.`), so adapter code must read `structured_output` explicitly rather than treating `result` text as structured data.

### 2026-07-08 session probes

`codebuddy -p --output-format stream-json --resume ecece836-ab7c-42bf-be40-9cfc3fb8dbfe ...`:

- exited `0`;
- `system.init.session_id` stayed `ecece836-ab7c-42bf-be40-9cfc3fb8dbfe`;
- terminal `result.session_id` stayed the same.

`codebuddy -p --output-format stream-json --resume ecece836-ab7c-42bf-be40-9cfc3fb8dbfe --fork-session ...`:

- exited `0`;
- `system.init.session_id` was a new ID: `92e6de32-70cf-43e8-9a62-e6c7c13f0fc9`;
- terminal `result.session_id` matched the new forked ID.

### 2026-07-08 HITL and permission probes

`codebuddy -p --output-format stream-json --permission-prompt-tool stdio ...`:

- exited `1`;
- stderr/stdout reported `error: unknown option '--permission-prompt-tool'`.

`codebuddy -p --output-format stream-json --permission-mode default ...` asking it to write `hitl-probe.txt`:

- exited `0`;
- emitted a `Write` tool call;
- `Write` was denied through a `user.tool_result` text payload saying permission prompts are not available in non-interactive mode;
- model then attempted `Bash`, which was denied the same way;
- terminal `result.subtype` was still `success`, because the agent explained the denial in assistant text;
- no file was created in the workspace.

`codebuddy -p --output-format stream-json --permission-mode bypassPermissions ...` asking it to write `hitl-probe.txt`:

- exited `0`;
- `system.init.permissionMode` was `bypassPermissions`;
- emitted a `Write` tool call;
- emitted `file-history-snapshot` updates for `hitl-probe.txt`;
- `user.tool_result` reported the file was created;
- terminal `result.result` was `done`;
- local file content was exactly `hitl-probe-ok`.

`codebuddy -p --output-format stream-json --permission-mode default ...` asking it to use `AskUserQuestion`:

- exited `0`;
- `system.init.tools` listed `AskUserQuestion`;
- the model could not actually invoke `AskUserQuestion`; `ToolSearch` reported no matching tool;
- terminal `result` was a text explanation that the tool was unavailable.

`codebuddy -p --output-format stream-json --permission-mode plan --max-turns 3 ...`:

- exited `0`;
- `system.init.permissionMode` was `plan`;
- `Bash` was denied with the same non-interactive permission message;
- `Glob` and `Read` were allowed;
- terminal `result.subtype` was `error_during_execution`;
- terminal `errors` contained `Max turns (3) exceeded`;
- terminal `result.session_id` was still present.

### 2026-07-08 upgrade to 2.117.2

Upgrade command:

- `npm install -g --prefix /Users/blurooo/.xvm/data/node/npm-packages @tencent-ai/codebuddy-code@latest`

Post-upgrade facts:

- `codebuddy --version` returned `2.117.2`.
- package file `/Users/blurooo/.xvm/data/node/npm-packages/lib/node_modules/@tencent-ai/codebuddy-code/package.json` reported `2.117.2`.
- `codebuddy --help` still does not list `--permission-prompt-tool`.
- `--permission-mode` choices are `acceptEdits`, `bypassPermissions`, `default`, `plan`, `dontAsk`, and `auto`.
- `--effort` choices now include `minimal` and `max`.
- model list changed from the initial `2.99.1` list; do not hardcode older model options.

Workspace: `/tmp/codebuddy-probe-upgraded-RehIfE`.

`codebuddy -p --tools "" --output-format json ...`:

- exited `0`;
- still emitted a JSON array frame envelope;
- summary: `isArray=true`, `length=4`, types `message`, `file-history-snapshot`, `result`;
- terminal `result.session_id` was `c148a687-af5c-4102-ab15-880b10ec2751`;
- terminal `result.result` was the assistant-facing JSON text;
- no `structured_output`.

`codebuddy -p --tools "" --output-format stream-json ...`:

- exited `0`;
- emitted five NDJSON frames with types `system:init`, `system:status`, `file-history-snapshot`, `assistant`, and `result:success`;
- `system.init.session_id` and terminal `result.session_id` matched;
- `system.init.model` was `hy3-preview-ioa` for this run;
- assistant text and terminal `result.result` matched `stream-upgraded-ok`.

`codebuddy -p --tools "" --output-format stream-json --include-partial-messages ...`:

- exited `0`;
- emitted 17 NDJSON frames;
- emitted 12 `stream_event` frames;
- `stream_event.event.type` values included `message_start`, `content_block_start`, `content_block_delta`, `content_block_stop`, `message_delta`, and `message_stop`;
- text deltas were `partial`, `-upgraded`, and `-ok`;
- terminal `result.result` was `partial-upgraded-ok`.

`codebuddy -p --tools "" --output-format json --json-schema ...`:

- exited `0`, but did not produce native structured output;
- terminal `result.result` was a "Tool call YAML" text fallback;
- terminal `result.structured_output` was absent.

`codebuddy -p --output-format json --json-schema ...` with default tools:

- one unconstrained attempt ran longer than 90 seconds and was interrupted with Ctrl-C;
- retrying with `--max-turns 4` exited `0`;
- emitted `function_call:StructuredOutput` and `function_call_result:StructuredOutput`;
- terminal `result.result` was `Done.`;
- terminal `result.structured_output` contained the schema-valid object.

`codebuddy -p --output-format stream-json --resume 567aa1c3-ef45-485e-9197-0c98deaaef85 ...`:

- exited `0`;
- `system.init.session_id` stayed `567aa1c3-ef45-485e-9197-0c98deaaef85`;
- terminal `result.session_id` stayed the same;
- terminal `result.result` was `resumed-upgraded-ok`.

`codebuddy -p --output-format stream-json --resume 567aa1c3-ef45-485e-9197-0c98deaaef85 --fork-session ...`:

- exited `0`;
- `system.init.session_id` became `2a38f7f9-f2d7-4be0-b2fa-91f17c71d2e2`;
- terminal `result.session_id` matched the new forked ID;
- terminal `result.result` was `fork-upgraded-ok`.

`codebuddy -p --output-format stream-json --permission-prompt-tool stdio ...`:

- exited `1`;
- output was `error: unknown option '--permission-prompt-tool'`.

`codebuddy -p --output-format stream-json --permission-mode default --max-turns 4 ...` asking it to write `hitl-upgraded-default.txt`:

- exited `0`;
- emitted `Write` and `Bash` tool calls;
- both received `user.tool_result` denial text saying permission prompts are not available in non-interactive mode;
- terminal `result.subtype` was `success` because the agent explained the denial;
- no file was created.

`codebuddy -p -y --output-format stream-json --max-turns 4 ...` asking it to write `hitl-upgraded-y.txt`:

- exited `0`;
- `system.init.permissionMode` was `bypassPermissions`;
- emitted a `Write` tool call;
- `user.tool_result` reported file creation succeeded;
- terminal `result.result` was `done`;
- local file content was `hitl-y-ok`.

`codebuddy -p --permission-mode bypassPermissions --output-format stream-json --max-turns 4 ...` asking it to write `hitl-upgraded-bypass.txt`:

- exited `0`;
- `system.init.permissionMode` was `bypassPermissions`;
- emitted a `Write` tool call;
- `user.tool_result` reported file creation succeeded;
- terminal `result.result` was `done`.

`codebuddy -p --output-format stream-json --permission-mode default --max-turns 4 ...` asking it to use `AskUserQuestion`:

- exited `0`;
- `system.init.tools` included `AskUserQuestion`;
- no `AskUserQuestion` tool call appeared;
- terminal `result.subtype` was `error_during_execution`;
- terminal `errors` contained `Tool AskUserQuestion not found in agent cli.`;
- this confirms CLI-print transport must not advertise `Question.Ask` despite the init tool list.

`codebuddy -p --output-format stream-json --permission-mode plan ...` asking it to use `ExitPlanMode`:

- a direct `ExitPlanMode` probe ran longer than 60 seconds and was interrupted;
- a shorter `--permission-mode plan --max-turns 1` probe exited `0`;
- `system.init.permissionMode` was `plan`;
- the agent wrote a plan file under `/Users/blurooo/.codebuddy/plans/` via a `Write` tool call and then ended with `result.subtype=error_during_execution` / `errors=["Max turns (1) exceeded"]`;
- the generated plan file was removed after the probe;
- this confirms CLI-print plan mode is not a host-resolvable PlanReview channel. `PlanReview.Ask` requires SDK/control mode.

### 2026-07-08 SDK/control PlanReview.Ask probes

Temporary probe script: `/tmp/codebuddy-sdk-probe-LZTDFQ/plan-probe.cjs`.

Probe command shape:

```sh
codebuddy \
  --input-format=stream-json \
  --output-format=stream-json \
  --verbose \
  --setting-sources user \
  --include-partial-messages \
  --max-turns 8 \
  --permission-mode plan \
  --permission-mode-before-plan default
```

Initialization frame:

```json
{
  "type": "control_request",
  "request_id": "manual_plan_1",
  "request": {
    "subtype": "initialize",
    "capabilities": { "askUserQuestion": true },
    "hasPrompt": true
  }
}
```

Approve path:

- CodeBuddy emitted a `Write` tool use to `/Users/blurooo/.codebuddy/plans/<name>.md`; the tool input contained the plan markdown and probe marker.
- CodeBuddy then emitted an `ExitPlanMode` tool use.
- Before the ordinary assistant tool-use frame finished, stdout emitted:

```json
{
  "type": "control_request",
  "request_id": "perm_...",
  "request": {
    "subtype": "can_use_tool",
    "tool_name": "ExitPlanMode",
    "input": {},
    "tool_use_id": "chatcmpl-tool-..."
  }
}
```

- Replying with this control response approved the plan:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "perm_...",
    "response": {
      "allowed": true,
      "updatedInput": {},
      "tool_use_id": "chatcmpl-tool-..."
    }
  }
}
```

- CodeBuddy emitted a `user.tool_result` saying the user approved the plan; `_meta.rawResponse` contained `planFilePath` and `plan`.
- Terminal result was `subtype:"success"`, `result:"plan-approved"`, and empty `permission_denials`.
- The probe removed its generated plan file.

Reject with abort path:

- Same `ExitPlanMode` `can_use_tool` request shape as the approve path.
- Replying with this control response rejected and interrupted the run:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "perm_...",
    "response": {
      "allowed": false,
      "reason": "plan rejected by plan-probe",
      "interrupt": true,
      "tool_use_id": "chatcmpl-tool-..."
    }
  }
}
```

- Terminal result was `subtype:"error_during_execution"`, `is_error:true`.
- Terminal `errors` contained `Permission denied for tool(s): ExitPlanMode`.
- Terminal `permission_denials` contained `{ "tool_name": "ExitPlanMode", "tool_use_id": "...", "tool_input": {} }`.
- The process exited `0`; the SDK adapter must map this provider-level terminal result to `RunFailure{HumanDecision.Kind: PlanReview, Decision: Rejected}` when the rejection came from the SDK policy.
- The probe removed its generated plan file.

Reject without abort path:

- Replying `allowed:false` with a reason but without `interrupt:true` produced a `tool_result` telling the model the user did not want to proceed yet and should keep planning.
- The model continued in plan mode and can remain in a plan loop until it emits another `ExitPlanMode`, hits `--max-turns`, or the context is cancelled.
- Therefore SDK `OnReject=FailureAbort` / timeout-abort should include `interrupt:true`; `FailureContinue` may omit it only when the host intentionally wants iterative replanning and has bounded max-turns / context cancellation.

## 9. Dependency Selection

No new Go runtime dependency is justified by current evidence.

- Reliability: CodeBuddy exposes JSON/JSONL protocol frames simple enough for a localized stdlib parser, matching the existing Claude/Cursor pattern.
- Maintainability: no official Go protocol package has been confirmed.
- Locality: a future dependency, if adopted, must stay inside `codebuddy/` or a provider-private subpackage and must not leak into core SDK public API.

Revisit this if CodeBuddy publishes a stable Go SDK/protocol package that materially reduces parser drift risk.

`@tencent-ai/agent-sdk` is useful as the official reference implementation for the SDK/control protocol, but the current probes show it is not required as a runtime dependency for the Go adapter: the Go adapter can spawn `codebuddy` directly and speak the same NDJSON `control_request` / `control_response` protocol over stdio. This avoids requiring SDK users to install a second npm package beyond the CodeBuddy CLI itself.

## 10. HITL Ask / PlanReview Implementation Research

Status: verified locally on CodeBuddy CLI `2.117.2` and `@tencent-ai/agent-sdk` `0.3.203` on 2026-07-08.

### 10.1 What does not work

CLI `--print` mode cannot implement SDK `QuestionAsk` or `PlanReviewAsk`.

Evidence:

- `codebuddy -p --output-format stream-json --permission-prompt-tool stdio ...` exits with `unknown option '--permission-prompt-tool'`.
- `codebuddy -p --permission-mode default ...` asking for a file write emits ordinary `Write` / `Bash` tool calls, but the tools receive denial `user.tool_result` text saying permission prompts are unavailable in non-interactive mode; no host-resolvable request is emitted.
- `codebuddy -p ...` asking the model to use `AskUserQuestion` lists `AskUserQuestion` in `system.init.tools`, but the run fails with `Tool AskUserQuestion not found in agent cli.`
- `codebuddy -p --permission-mode plan ...` can write a plan file under `~/.codebuddy/plans`, but no host-resolvable `ExitPlanMode` approval request is emitted in print mode.
- CodeBuddy release notes explicitly say SDK mode supports interactive tools such as `AskUserQuestion` while Print mode disables interactive tools.

Conclusion: the baseline CLI-print adapter can support execution, sessions, streaming, structured output, and AutoApprove, but must declare `PlanReview.Ask=false` and `Question.Ask=false` unless a SDK/control-mode transport is implemented.

### 10.2 Official SDK shape

The TypeScript SDK exposes `canUseTool`:

```ts
type CanUseTool = (
  toolName: string,
  input: Record<string, unknown>,
  options: {
    signal: AbortSignal
    suggestions?: PermissionUpdate[]
    blockedPath?: string
    decisionReason?: string
    toolUseID: string
    agentID?: string
  }
) => Promise<PermissionResult>
```

`PermissionResult` is:

```ts
| { behavior: "allow"; updatedInput?: Record<string, unknown>; updatedPermissions?: PermissionUpdate[]; toolUseID?: string }
| { behavior: "deny"; message: string; interrupt?: boolean; toolUseID?: string }
```

`AskUserQuestionInput` is:

```ts
{
  questions: Array<{
    question: string
    header: string
    options: Array<{ label: string; description: string }>
    multiSelect: boolean
  }>
  answers?: Record<string, string>
}
```

The SDK internally declares `capabilities.askUserQuestion=true` during `initialize`, receives CLI `control_request` frames with `subtype:"can_use_tool"`, calls `canUseTool`, then returns a lower-level control response:

```json
{
  "allowed": true,
  "updatedInput": { "...": "...", "answers": { "Transport": "SDK" } },
  "tool_use_id": "..."
}
```

For denial it returns:

```json
{
  "allowed": false,
  "reason": "message",
  "interrupt": true,
  "tool_use_id": "..."
}
```

### 10.3 Runtime probes

Temporary probe directory: `/tmp/codebuddy-sdk-probe-LZTDFQ`.

`@tencent-ai/agent-sdk` probe:

- installed `@tencent-ai/agent-sdk@0.3.203` into a temp npm project;
- simple `query()` run produced normal `system:init`, `stream_event`, `assistant`, and `result` messages;
- a write prompt triggered `canUseTool("Write", input, options)` before execution; returning `{behavior:"deny"}` prevented file creation and populated terminal `result.permission_denials`;
- an `AskUserQuestion` prompt triggered `canUseTool("AskUserQuestion", input, options)` with:

```json
{
  "questions": [
    {
      "header": "Transport",
      "options": [
        { "description": "Use SDK control callback", "label": "SDK" },
        { "description": "Use print stream only", "label": "CLI" }
      ],
      "question": "Choose the adapter transport?"
    }
  ]
}
```

Returning:

```json
{
  "behavior": "allow",
  "updatedInput": {
    "questions": ["..."],
    "answers": { "Transport": "SDK" }
  }
}
```

made CodeBuddy emit a successful `user.tool_result` (`Transport -> SDK`) and finish with terminal `result.result = "selected=SDK"`.

Manual stdio/control probe:

- spawned `codebuddy --input-format=stream-json --output-format=stream-json --verbose --setting-sources user --include-partial-messages --max-turns 6`;
- sent:

```json
{
  "type": "control_request",
  "request_id": "manual_1",
  "request": {
    "subtype": "initialize",
    "capabilities": { "askUserQuestion": true },
    "hasPrompt": true
  }
}
```

- waited for `control_response`;
- sent a `type:"user"` frame with the prompt;
- received:

```json
{
  "type": "control_request",
  "request_id": "perm_...",
  "request": {
    "subtype": "can_use_tool",
    "tool_name": "AskUserQuestion",
    "input": { "questions": ["..."] },
    "tool_use_id": "chatcmpl-tool-..."
  }
}
```

- replied:

```json
{
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": "perm_...",
    "response": {
      "allowed": true,
      "updatedInput": {
        "questions": ["..."],
        "answers": { "Transport": "SDK" }
      },
      "tool_use_id": "chatcmpl-tool-..."
    }
  }
}
```

- the run completed successfully with `result.result = "selected=SDK"`.

PlanReview probe:

- spawned `codebuddy --input-format=stream-json --output-format=stream-json --verbose --include-partial-messages --permission-mode plan --permission-mode-before-plan default --max-turns 8`;
- sent the same `control_request.initialize` shape before the user prompt;
- the model wrote a plan file via `Write`, then invoked `ExitPlanMode`;
- CodeBuddy emitted `control_request{subtype:"can_use_tool", tool_name:"ExitPlanMode", input:{}, tool_use_id:"..."}`;
- `allowed:true` + `updatedInput:{}` approved the plan and produced terminal `result.result = "plan-approved"`;
- `allowed:false` + `interrupt:true` rejected the plan and produced terminal `result.subtype = "error_during_execution"` with `permission_denials[0].tool_name = "ExitPlanMode"`;
- `allowed:false` without `interrupt:true` kept the model in plan mode and may continue until another `ExitPlanMode`, max-turns, or context cancellation.

Conclusion: the Go adapter can implement HITL Ask / PlanReview.Ask by directly speaking CodeBuddy SDK/control protocol over stdio; a Node helper is not required for the first implementation.

### 10.4 Adapter design

Use two internal transports behind the same public `codebuddy` adapter:

1. CLI-print transport for non-HITL runs:
   - command: `codebuddy -p --output-format stream-json ...`
   - supports normal parsing, session resume/fork, native structured output, and AutoApprove.
2. SDK/control transport when `RunPolicy.HumanDecision.Permission == Ask`, `PlanReview == Ask`, or `Question == Ask`:
   - command: `codebuddy --input-format=stream-json --output-format=stream-json --verbose --include-partial-messages ...`
   - add `--permission-mode plan --permission-mode-before-plan <mapped permission mode>` when `PlanReview == Ask`;
   - first stdin frame: `control_request.initialize` with `capabilities.askUserQuestion=true` and `hasPrompt=true`;
   - then stdin `user` frame for the prompt;
   - stdout control requests bypass normal transcript parsing and go to a control handler;
   - ordinary stdout messages still feed the CodeBuddy parser.

`can_use_tool` mapping:

| CodeBuddy request | SDK request | Response back to CodeBuddy |
|---|---|---|
| `tool_name == "AskUserQuestion"` | `DecisionRequest{Kind: HumanDecisionQuestion, Source: "codebuddy.ask_user_question", ToolCallID, Prompt, Choices, Payload}` | `allowed:true`, `updatedInput` = original input plus `answers` |
| write/edit/bash/tool permission requests | `DecisionRequest{Kind: HumanDecisionPermission, Source: "codebuddy.permission.<tool>", ToolCallID, Prompt, Payload}` | approve: `allowed:true`, `updatedInput` original input; reject: `allowed:false`, `reason`, optional `interrupt` |
| `tool_name == "ExitPlanMode"` | `DecisionRequest{Kind: HumanDecisionPlanReview, Source: "codebuddy.exit_plan_mode", ToolCallID, Prompt, Payload}` | approve: `allowed:true`, `updatedInput` original input; reject: `allowed:false`, `reason`, `interrupt:true` for abort |

PlanReview plan-text extraction:

- CodeBuddy's `ExitPlanMode` control request currently has `input:{}`; it does not carry the plan markdown.
- In plan mode, CodeBuddy writes the draft plan before `ExitPlanMode` using a `Write` tool call targeting `~/.codebuddy/plans/<name>.md`; the `Write` tool input contains both `file_path` and `content`.
- The adapter should maintain run-local plan draft state while parsing ordinary assistant tool-use blocks:
  - when a `Write` tool targets a CodeBuddy plan path, store `plan_file_path` and `plan`;
  - when `ExitPlanMode` arrives, put that stored markdown into `DecisionRequest.Payload["plan"]` and `Payload["plan_file_path"]`;
  - if no draft was captured, still emit a PlanReview request with raw `input` and a conservative prompt such as `CodeBuddy requested approval to exit plan mode`, but leave `plan` empty.
- The approval `user.tool_result` later includes `_meta.rawResponse.planFilePath` and `_meta.rawResponse.plan`, but that arrives after the decision and is only useful for transcript/result enrichment, not for constructing the approval request.

Question answer encoding:

- Build `DecisionRequest.Prompt` from the first question text.
- Build `DecisionRequest.Choices` from the first question's `options`, using `label` as both key and label when no separate key exists.
- Preserve the full `questions` array in `Payload`.
- On `DecisionAnswered`, create `answers: Record<string,string>`.
- Prefer answer keys in this order:
  1. question `header` when non-empty;
  2. full `question` text;
  3. `q_<index>`.
- For multi-select answers, encode as a comma-separated label string initially because CodeBuddy's documented `answers` value type is `string`; preserve richer answer metadata in `DecisionResponse.Answer` / stream payload for bridge UIs.

Policy behavior:

- If `Question == QuestionAsk`, require SDK/control transport and a `DecisionCapableSink`; otherwise return `ErrHumanDecisionModeUnsupported`.
- If `Question == QuestionAutoReject`, a `can_use_tool` for `AskUserQuestion` should return `allowed:false` with a clear reason.
- If `PlanReview == HumanDecisionAsk`, require SDK/control transport and a `DecisionCapableSink`; otherwise return `ErrHumanDecisionModeUnsupported`.
- If `PlanReview == HumanDecisionAutoApprove` and control mode sees `ExitPlanMode`, answer `allowed:true`.
- If `PlanReview == HumanDecisionAutoReject` and control mode sees `ExitPlanMode`, answer `allowed:false` with `interrupt:true` unless the policy explicitly requests continue/retry semantics.
- If `Permission == HumanDecisionAsk`, route non-whitelisted permission tools through `sink.RequestDecision`.
- If `Permission == HumanDecisionAutoApprove`, prefer CLI-print `--permission-mode bypassPermissions` / `-y` for non-HITL runs; in SDK/control mode either set `--permission-mode bypassPermissions` or answer permission `can_use_tool` locally as `allowed:true`, but do not bypass `AskUserQuestion`.
- If `OnReject` / `OnTimeout` resolves to abort, include `interrupt:true` in deny responses and close stdin after writing the control response.
- If `OnReject=FailureContinue` for PlanReview, omitting `interrupt:true` lets CodeBuddy keep planning; bound this with `--max-turns` and context cancellation because the model can loop.

### 10.5 Implementation notes

- Do not copy Claude's `--permission-prompt-tool stdio`; CodeBuddy does not support it.
- Do not map CodeBuddy CLI-print `--permission-mode plan` to SDK `PlanReview.Ask`; only SDK/control mode surfaces `ExitPlanMode` as a host-resolvable `can_use_tool` callback.
- `ExitPlanMode` input is empty in verified `2.117.2`; capture the plan from the preceding plan-file `Write` tool use.
- The CodeBuddy SDK's `requestTimeoutMs` option currently maps to `--request-timeout-ms`, which local CLI `2.117.2` rejects. Do not depend on that flag in the Go implementation.
- The `initialize` response can include account metadata. Parser/logging code must avoid persisting sensitive account/token details in normalized result metadata.
- SDK/control mode should still return the same `RawStreams`, `Transcript`, `Output`, `Summary`, `Result`, and checkpoint semantics as CLI-print mode; this is an adapter-internal transport switch, not a second SDK execution entry.
