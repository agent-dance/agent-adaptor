# Pi and OpenCode

Both Drivers use the existing `Agent`, `Thread`, `Stream`, `Event`, and `Result`
contracts. They do not add a root API or a second execution pipeline.

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/memory"
    "github.com/agent-dance/agent-adaptor/pi"
    "github.com/agent-dance/agent-adaptor/opencode"
)

piAgent := adaptor.New(
    pi.Driver(pi.Config{Provider: "openai", Model: "gpt-5.6-luna"}),
    adaptor.WithWorkspace("/repo"),
    adaptor.WithThreadStore(memory.NewStore()),
)
openCodeAgent := adaptor.New(
    opencode.Driver(opencode.Config{Model: "openai/gpt-5.6-luna"}),
    adaptor.WithWorkspace("/repo"),
    adaptor.WithThreadStore(memory.NewStore()),
)
defer piAgent.Close(ctx)
defer openCodeAgent.Close(ctx)

result, err := piAgent.Thread("ticket:42").Run(ctx, "Review the change")
```

The selected provider must actually expose the requested model. The Driver
passes the model identifier through; it does not invent provider model support.
`adaptor.WithModel` overrides the construction model for one invocation.

## Capability contract

| Capability | Pi | OpenCode |
|---|---|---|
| Validated CLI version | 0.86.1 (`@earendil-works/pi-coding-agent`) | 1.14.30 (`opencode`) |
| Transport | `--mode rpc` for Threads or native extensions; `--mode json --print` for stateless calls with native extension discovery disabled | Private server HTTP/SSE for explicit Threads, resolved streaming, approvals, MCP and native schemas; CLI JSON for other batch invocations |
| Native text streaming | Token deltas | Native SSE text and reasoning deltas |
| Thinking and tool events | Yes | Yes |
| Tool argument deltas | Native `toolcall_delta` | No; complete invocation snapshot |
| Thread resume / fork | `--session` / `--fork` | Native session and fork APIs; CLI session flags in batch transport |
| Persistent process | Explicit Threads reuse their private RPC process; WithSpawn forces a fresh process | Explicit Threads reuse their private server; WithSpawn forces a fresh process |
| Host `WithTools` | Official Pi extension + SDK MCP client | Native dynamic MCP API |
| External MCP | stdio, HTTP, SSE tools | stdio, HTTP, SSE |
| Selected skills | Native `--skill` | Native `skills.paths` |
| Instructions | Additive or replacement system prompt | Additive instructions or verified native base-prompt replacement |
| Structured output | Core prompt + local JSON Schema validation | Native `format.json_schema` and local validation, including HITL |
| Explicit permission auto-approve | Yes; Pi's normal execution behavior | CLI auto-approval, preserving native explicit denies |
| Explicit permission auto-deny | Official pre-tool extension gate | Native permission requests enter core auto-denial |
| Interactive Permission / PlanReview / Question | Official extension + core ApprovalRequest sink | Native server permission/question APIs, correlated `plan_exit` review |
| Sandbox / web-search / browser policy | Unsupported | Unsupported |
| Quota probe | Explicitly unavailable | Explicitly unavailable |

Unsupported explicit policies fail before launch. CLI-native defaults still
apply when no SDK policy is supplied. Pi extensions and OpenCode plugins remain
provider code; `NoExtensions` and `Pure` disable automatic discovery where
offered by the provider. Pi's `NoTools` conflicts with requested MCP or approval tools.

Pi explicit interactive policy installs an official extension. Ordinary
tools enter Permission through Pi's real pre-execution `tool_call` event.
`adaptor_review_plan` and `adaptor_ask_user` are explicit provider tools for plan
review and questions; the latter accepts choices or free-form host answers.
Both callback and Event responses use the existing core `ApprovalRequest`.
Timeout, auto-denial, fallback and exactly-once settlement remain core policy.
An aborted decision cancels the process; Continue forwards denial as a provider
tool error. Native extensions select RPC even on stateless calls, preserving
`ctx.ui.select`, `confirm`, `input` and `editor` as Questions. Generic confirmation
text never becomes a Permission or PlanReview signal. Notifications, status,
widgets and editor updates remain complete provider transcript records.
MCP sessions and approval responders belong to the current invocation; an RPC
extension listener can survive across turns while its bindings are replaced.
Idle listeners reject requests instead of calling a completed invocation.

OpenCode launches `serve --hostname 127.0.0.1 --port 0` with a random Basic-auth
password supplied only through the process environment. It waits for the SSE
connection barrier before sending the prompt and never retries prompt delivery.
Explicit Ask/auto-deny promotes native allowed operations to session and agent
approval gates while retaining resolved deny rules. Auto-approve replies once to native
requests without removing explicit denies. Native `plan_exit` questions become
PlanReview only when their tool-call identity matches; other questions retain
their choices and free-form answers. Multi-question requests are answered in
order; the public single-answer API selects one answer per native question.
Permission, question and plan replies all use core policy, including cancellation
and Continue. The server enables native continuation on denial when core selects
Continue. The provider decides which tools are available under native policy.

Full Claude alignment remains an active workstream; see the
[acceptance inventory](./claude-parity.md) for outstanding gaps.

Pi `Config` exposes `Command`, `CWD`, `Env`, `Provider`, `Model`, `Thinking`,
`Tools`, `NoTools`, `Extensions`, `NoExtensions`, `NoSkills`, and `SessionDir`.
OpenCode exposes `Command`, `CWD`, `Env`, `Model`, `Agent`, `Variant`, and `Pure`.
Both own real configuration structs and snapshot their slices on construction.
Use the normal adaptor options for execution deadlines, workspace, profile,
instructions, skills, MCP, and runtime services. No extra argument escape hatch
can replace the resolved transport, prompt, or Thread selector.

## Output and checkpoint integrity

Parsers consume only the official event shapes. `Text` is the final assistant
text; intermediate assistant messages remain in `Transcript()`. Neither CLI
provides a separate bounded summary, so `Summary` stays empty. Unknown events
are retained as system transcript records with their raw payload; stderr stays
in `Raw()` and is never promoted to assistant text.

`Raw()` preserves complete stdout/stderr and Pi's final `agent_end` or
OpenCode's final CLI `step_finish`/`error` or server `session.message` response.
Server SSE bytes are retained separately in `Metadata["opencode.event_stream"]`;
they are HTTP data and are not relabeled as process stdout. Provider errors also
retain their terminal event. Usage is summed once per authoritative
message or step; absent usage remains nil. SDK `Run` and `Stream.Result` use
the same parsing and result conversion.

A checkpoint requires a successful process outcome, an official successful
terminal event, a consistent explicit session identifier, and well-formed
protocol. Pi's zero process exit alone is insufficient: assistant
`stopReason: "error"` / `"aborted"` is a business failure. Intermediate
OpenCode `step_finish` with `reason: "tool-calls"` is not final completion.
Pi RPC additionally requires a correlated prompt acknowledgement, `agent_settled`,
an explicit idle `get_state` response and a stable resumable session ID. Schema
validation precedes keeping the writer; failed output cannot retain it.
Server checkpoints additionally require an idle barrier and an authoritative
completed assistant message (including successful native structured output).
Stateless, WithSpawn and failed invocations stop their owned server; successful
Threads retain it until replacement, five minutes of inactivity or Agent.Close.
Pi RPC has the same lifecycle; its teardown metadata is `pi.rpc_exit_code` and
`pi.rpc_signal`, with `pi.rpc_pid` and `pi.rpc_reused` identifying process reuse.
Configuration changes stop the previous writer before starting its replacement.
Only startup before prompt delivery can retry once. Approval policy is reapplied
each turn from the saved native baseline. OpenCode's session permission PATCH
is append-only: each changed
policy appends a complete resolved segment, retaining deny precedence and
verifying returned native state. An empty array is not used as a reset.
The original baseline remains unchanged in the checkpoint. Audit bytes belong
to the current invocation; earlier turns are never replayed into Raw. Teardown exit/signal remain in
`opencode.server_exit_code` / `opencode.server_signal` metadata. Unexpected server
exit during execution, including after text has arrived, invalidates checkpoints.
Cancellation, malformed records and failed runs cannot overwrite the previous
healthy Thread checkpoint. Forks must return a different provider identifier.

Guards cover resolved cwd, workspace ID, profile resources, native profile
configuration bytes, and session storage location. Pi IDs and OpenCode IDs stay
inside checkpoints; the host's opaque Thread key remains the public identity.

## Profiles and resources

Pi honors `PI_CODING_AGENT_DIR`, normally `~/.pi/agent`, and
`PI_CODING_AGENT_SESSION_DIR` / `Config.SessionDir`. Dedicated/clone profile
selection uses the existing `profile` package. Clone settings include
`settings.json` and `models.json`; authentication is `auth.json`.

OpenCode honors `OPENCODE_CONFIG_DIR`, `OPENCODE_CONFIG`, and
`OPENCODE_CONFIG_CONTENT`. Configuration defaults to the OpenCode directory
under `XDG_CONFIG_HOME` (or `~/.config`). Authentication and sessions use the
provider's separate XDG data directories. A dedicated **config** profile does
not isolate those data directories; set `XDG_DATA_HOME`/`XDG_STATE_HOME` in Env
when isolation is required. Auth cloning is rejected rather than pretending to
copy credentials from the config directory.

OpenCode `profile.CopyMCP()` selects only `mcp` from native JSON/JSONC;
`CopySettings()` selects the other fields. Select both to copy both families.
Comments are omitted from clone output. The native config files form one seed
group: if either target file already exists, the entire group is preserved so
cloning cannot introduce a different lower-precedence configuration. Fresh MCP
clones retain verified ownership, transport and required-connection policy;
externally modified owned entries fail before copying. Clone writes and
ownership commit are coordinated under bounded, cancellation-aware locks.

Explicit Run/Stream skills use invocation-local arguments or native overlays.
`SyncProfile` installs the selected skills into the native profile `skills/`
directory. A fresh Agent using that profile can discover them without
`WithSkills`. Shared profiles retain other installed skills; dedicated profiles
prune unselected SDK-owned skills. External installations are preserved and a
conflicting runtime name returns an error. `Inspect().Skills` and `ProfileState`
read actual files and SDK ownership without initializing or cloning a profile.
They do not claim that the model executed a skill. Native skill contents are
included in Thread compatibility and persistent-process guards.

```go
agent := adaptor.New(pi.Driver(pi.Config{}),
    adaptor.WithProfile(profile.Dedicated("/profiles/reviewer")),
    adaptor.WithSkills(skill.Inline("review", reviewSkillMarkdown)),
)
snapshot, err := agent.SyncProfile(ctx)
```

The same API applies to `opencode.Driver`. The provider's native discovery rules
still apply: Pi `NoSkills` disables automatic discovery; OpenCode skill
permissions can hide or block an installation. The delivery contract follows
[Pi skills](https://pi.dev/docs/latest/skills) and
[OpenCode skills](https://dev.opencode.ai/docs/skills/).

Pi model inspection reads configured models/settings; OpenCode model detection
reports an explicit Config model and leaves native auto-selection unavailable.
Quota and portable hooks/config patches remain unsupported.
A requested unsupported durable resource fails before any skill synchronization
or clone. Pi and OpenCode profile agents support the fields described below.

`SyncProfile` also supports native instructions with default/user scope:
Pi additive bundles use `APPEND_SYSTEM.md`, Pi replacement bundles use
`SYSTEM.md`, and OpenCode additive bundles use `AGENTS.md`. Switching Pi modes
removes the prior SDK-owned file. A fresh Agent with no `WithInstructions` reads
these files through native discovery; normal provider precedence still applies
(for example, a Pi project `SYSTEM.md` can shadow the profile file).

```go
agent := adaptor.New(pi.Driver(pi.Config{}),
    adaptor.WithProfile(profile.Dedicated("/profiles/reviewer")),
    adaptor.WithProfileResources(profile.Resources{
        Instructions: &profile.Instructions{
            ID: "review-rules", Content: "Explain the evidence for each finding.",
            Scope: profile.InstructionScopeUser,
        },
    }),
)
_, err := agent.SyncProfile(ctx)
```

Instruction ownership includes the exact installed bytes. `ProfileState`
reports an externally edited file as external; sync and explicit clear refuse
to overwrite it. Undeclared instructions are preserved. An Agent configured with
`WithInstructions("")` explicitly clears the SDK-owned instructions when
`SyncProfile` is called. Failed file replacement, removal or manifest commit
rolls back changes under the profile lock; external concurrent replacements are
preserved and reported. Native profile instruction contents participate in
Thread and persistent-process compatibility.

Run/Stream bundles remain invocation-local, with file contents captured before
resource acquisition. Invalid modes/scopes or native extensions fail explicitly.
OpenCode supports invocation-local replacement through native `agent.prompt`.
With an explicit `Config.Agent`, only that agent's prompt is replaced. Without
one, visible primary-capable agents receive the replacement and native default
selection stays unchanged, including custom defaults and plan/build switches.
Hidden title/compaction agents and subagent-only prompts retain their settings.
Native context files, tool/schema instructions and per-run runtime context
remain governed by OpenCode; replacement changes the base prompt, not those
additional context sources.

Replacement uses the private server even for batch calls. Before sending the
user prompt, a discovery process reads resolved native agent settings, exits,
and is replaced by a process with an invocation-local JSON overlay. The Driver
checks the applied prompt and all other native agent fields, including model,
permissions and default selection. A mismatch fails before prompt delivery.
Equivalent adjacent permission rules with the same action may be reordered by
native skill discovery; action precedence is preserved. Both processes' raw
output is retained, and compatible Thread turns reuse the configured process.
Literal `{env:...}` / `{file:...}` inside replacement text are not interpolated
as configuration. No native profile configuration is rewritten.

```go
agent := adaptor.New(opencode.Driver(opencode.Config{Pure: true}),
    adaptor.WithProfileResources(profile.Resources{
        Instructions: &profile.Instructions{
            Mode: profile.InstructionModeReplace,
            Content: "You are a reviewer. Explain the evidence for each finding.",
        },
    }),
)
result, err := agent.Run(ctx, "Review this change.")
```

Durable OpenCode replacement and project/local/run scope are not implemented:
`SyncProfile` rejects those
scopes before initialization instead of broadening them into global rules.
Project/local/run bundles can still be supplied to Run/Stream for that invocation.
These gaps remain open in the Claude parity inventory.

For additive instructions, OpenCode receives an invocation-local JSON overlay and the native prompt system
field (a temporary instruction file in CLI mode). Pi receives explicit
skill/system-prompt arguments. Neither rewrites a
shared profile to deliver invocation-local resources. Runtime service bindings
and context are forwarded, but declarations are not echoed as observed service
execution reports.

Pi MCP tools use an extension registered through Pi's official
`registerTool` API. The existing official Go MCP SDK handles initialization,
pagination, calls and transport shutdown. Tool names use a stable hash of the
server/name tuple to avoid collisions with native tools. Original names and
descriptions remain visible to the model. Only MCP tools are mapped; resource
subscriptions, prompts, sampling and elicitation are not advertised. Text/image
results map directly; other content and structured output are preserved in
tool details, with an explicit JSON text representation for unsupported Pi
content kinds. MCP errors become provider tool errors.

`SyncProfile` persists explicitly declared MCP servers for subsequent fresh
Agents. Pi stores declarations in `agent-adaptor-mcp.json` and reports
`file_managed`: its Driver loads them into the existing extension bridge on
each invocation. Plain Pi CLI use outside this SDK does not acquire a native
MCP client. OpenCode writes the native `mcp` field in `opencode.jsonc`, preserves
comments/trailing commas and other fields, and reports `native_managed`.
Existing entries in `opencode.json` or unowned JSONC entries are protected from
being overwritten during sync. Both support stdio, HTTP and SSE.

```go
agent := adaptor.New(opencode.Driver(opencode.Config{}),
    adaptor.WithProfile(profile.Dedicated("/profiles/reviewer")),
    adaptor.WithMCP(mcp.Server{
        Key: "knowledge", Transport: mcp.TransportHTTP,
        URL: "https://example.test/mcp", BearerTokenEnvVar: "KNOWLEDGE_TOKEN",
        Required: true,
    }),
)
_, err := agent.SyncProfile(ctx)
```

`BearerTokenEnvVar` remains an environment reference in files and ownership
metadata. Literal headers and command environments are stored only in native
configuration, without duplicating their values in the ownership manifest. Native values must still match the SDK's ownership hash; external
edits are observed as external and are never silently overwritten or removed.
Failed JSONC/manifest commits restore the prior bytes. Undeclared MCP is left
alone; `WithMCP()` explicitly clears the SDK-managed set during sync. On Run or
Stream it suppresses that set only for the current invocation, without rewriting
the profile. Provider-native external MCP remains subject to native configuration.
Runtime services and hosted Tools stay invocation-local and do not become durable
profile declarations merely because they inject MCP.

OpenCode verifies required MCP connection status through its native server
before prompt delivery; SDK MCP selects server transport even for batch calls. Pi establishes its current invocation's MCP sessions before
starting the model. Native configuration and durable requirement changes enter
checkpoint/process guards. Profile snapshots prove installation/ownership only;
the live tests separately prove actual tool calls.

The extension reaches a private loopback listener with a random per-run bearer
credential carried only in an unpredictable environment variable. Requests
are bounded and origin-checked. Cancellation closes requests, MCP sessions and
stdio processes; completion closes the invocation's MCP connections. JSON mode
also removes its extension and listener; RPC retains only the process-owned
listener and generated source until teardown. No credentials
are embedded in generated extension source or invocation arguments.

OpenCode installs invocation MCP connections through the official `POST /mcp`
API. Their native slots are disabled at startup, so changing a URL or transport
cannot connect the old endpoint or inherit old headers/environment values from
OpenCode's deep config merge. This selects private server transport whenever
SDK MCP is present. Connections are retained on compatible Thread turns;
connection changes replace the writer. Actual connection status is observed
before each prompt, with notices for optional failures and errors for required
failures. Literal `{env:...}` and `{file:...}` in portable MCP values remain
literal; only `BearerTokenEnvVar` creates an active durable environment reference.

## Pi profile agents

`profile.Resources.Agents` is stored in `agent-adaptor-agents.json` through
`SyncProfile`, with `file_managed` ownership. These declarations are consumed
by this Driver's extension; plain Pi CLI use does not load them automatically.
A fresh Agent loads synchronized declarations. An undeclared family is preserved,
an explicit empty slice clears SDK-owned entries, and invocation overrides replace
the current family without changing the file. Settings clones retain verified
ownership. External modifications remain protected.

The extension registers `adaptor_subagent` with `agent` and `task` arguments.
The request must match an observed native tool call before execution. Each child
uses a fresh native Pi session and the existing Driver transports/parsers, with
the same workspace/profile and configured endpoint. It receives an independent
conversation and does not inherit the portable delegation tool. This follows
Pi's official [subagent extension pattern](https://github.com/earendil-works/pi/blob/v0.86.1/packages/coding-agent/examples/extensions/subagent/index.ts).

| Field | Pi mapping / boundary |
|---|---|
| `Key`, `RuntimeName` | Host key identifies the owned entry; runtime name selects the tool target. Names require ASCII letters, digits, hyphens or underscores. Duplicate keys/names are rejected. |
| `Description`, `Instructions` | Tool target description and native child system prompt. Empty instructions preserve Pi defaults. |
| `Model`, `ReasoningEffort` | Empty/`inherit` model uses the parent's officially observed model/provider, falling back to Config before observation; an explicit `provider/model` changes both, while a bare model keeps the configured provider. Thinking uses Pi's supported levels, inheriting Config when omitted. |
| `ToolPolicy` | Exact read/bash/edit/write/grep/find/ls and registered SDK review/question tools. Read/Bash/Edit/Write/Grep/Glob/LS aliases map to native names. A nonempty allow list excludes every other tool, including MCP/extensions; denies win. The extension filters active tools and checks the actual pre-tool event. |
| `PermissionMode` | Empty/default inherits the invocation's approval policy. Other modes are explicitly unsupported. |
| `Skills` | Preload complete `SKILL.md` contents from the invocation's materialized selections or the effective profile's installed `skills/` directory. Host keys take precedence; unambiguous runtime names are also accepted. Selected sources override installed copies. |
| `MCPServers` | A nonempty list selects exact server keys from the parent's resolved MCP set. Empty inherits that set, including an empty set; it does not mean explicit clear. Children never reload additional durable servers. ToolPolicy applies independently and can deny the selected tools. |
| `SourcePath`, `SourceFingerprint` | Capture Markdown in the official subagent example's format: required string `name`/`description`, optional `model`, and `tools` as a comma-separated string or string array. The body maps to the same child system prompt as `Instructions`. Source name does not override host identity. The actual captured content participates in compatibility even with a supplied fingerprint. See [source imports](#agent-source-imports). |
| `SandboxMode`, `Hooks`, `Native` | Explicitly unsupported; advanced field mapping remains parity work. |

Skill preloads use the official `before_agent_start` extension event to append
captured contents and their source paths to the child's system prompt. They work
with `NoSkills` and replacement instructions. This is additive preloading, not a
restriction on native skill discovery. Only explicit SDK selections and installed
profile skills are resolved; workspace/package discovery needs explicit selection.
Missing or ambiguous references, unreadable/non-regular files, empty or invalid
UTF-8, and files larger than 4 MiB fail before the parent receives its prompt.
`SyncProfile` stores references; execution checks their availability. Captured
contents participate in checkpoint and process compatibility. A changed preload
cannot silently reuse an old Thread session. Resources and credentials remain in
their existing scopes; MCP selection does not sandbox native extensions or shell
access.

Child Permission/PlanReview/Question requests retain core policy and the same
callback/Event responders. Child message/tool identifiers use collision-free
`pi-child:` tuple encoding; transcript metadata identifies the owning tool and
agent, and native session IDs are retained once observed. There is one run
lifecycle. Cancellation waits for child process teardown before its terminal
event. `Result.Text` remains the parent answer; total Usage includes children.
Raw stdout/stderr includes child streams, while `Metadata["pi.child_runs"]`
preserves each child's separate streams, official terminal payload and failure.

## OpenCode profile agents

`profile.Resources.Agents` maps to the native `agent` object in `opencode.jsonc`.
`SyncProfile` owns only its installed entries, preserves unrelated JSONC and
external definitions, and reports actual ownership. `CopySettings` carries
verified agent ownership into a clone. An undeclared family is preserved;
an explicit empty agent slice removes SDK-owned entries. `Run` and `Stream`
apply the same declarations in process-local config without changing the file.
A fresh Agent also loads previously synchronized agents.

| Field | OpenCode mapping / boundary |
|---|---|
| `Key`, `RuntimeName` | Host key is retained in ownership metadata; runtime name selects the native subagent. Runtime names must contain ASCII letters, digits, hyphens or underscores and must not collide with native built-ins or another declaration. |
| `Description`, `Instructions` | Native description and prompt, with `mode: subagent`. Portable text remains literal, including `{env:...}` and `{file:...}`. |
| `Model` | Explicit `provider/model`; empty or `inherit` follows the calling agent. |
| `ReasoningEffort` | Native model variant, requiring an explicit model. The resolved provider must advertise the variant; otherwise execution fails before prompting. |
| `ToolPolicy` | Exact native permission names; a nonempty allow list starts with deny-all, then enables its members; denies take precedence. Read/Bash/Grep/Glob/WebFetch/WebSearch/Task/Agent/Skill aliases are translated. File mutation tools share `edit`; use that native group explicitly. Wildcard policy names and individually scoped Write/Edit/Patch aliases are rejected. |
| `PermissionMode` | Empty/default inherits native defaults; `acceptEdits` allows the edit group; `bypassPermissions` allows all; `dontAsk` denies tools unless enabled by the tool allow list. Other modes are rejected. |
| `Native` | `temperature`, `top_p`, `steps`, `hidden`, `color`, and `options`; validated native JSON values. |
| `SourcePath`, `SourceFingerprint` | Capture native Markdown body, model/variant, ordered permissions and supported native options. Actual resolved content participates in compatibility even with a supplied fingerprint. See [source imports](#agent-source-imports). |
| `Skills`, `MCPServers`, `SandboxMode`, `Hooks` | Explicitly unsupported; skill preloading and per-agent MCP scope remain parity work. |

The private server verifies resolved configuration before any prompt. Managed
overrides and residual fields from native deep merging cause an error, including
removing an older model or permission field through an invocation override.
Use explicit durable synchronization to remove such owned fields. Changes to
effective declarations replace a persistent writer; compatible turns reuse it.
Native task children forward Permission, Question and correlated `plan_exit`
requests through the same core approval pipeline. Explicit Ask/auto-deny
discovers and gates all resolved native agents before prompt delivery, preserving
deny precedence. OpenCode's internal tool-output cache directory retains its
native directory allow; read/edit/bash permissions remain gated. Unrepresentable
permission precedence and ineffective config overlays fail before prompting.

Only an active task's official `metadata.sessionId`, verified against the native
session API's `parentID`, binds a child to the run. Nested and parallel children
share the Event stream; child identifiers are namespaced, transcript preserves
ownership and observed child usage is included in the total. Result.Text remains
the main assistant answer. A failed task closes descendant lifecycles; a task
cannot succeed with unfinished child content. Late/unrelated child approvals
are ignored. Core retains all timeout, denial and cancellation policy.

A task approval with `task_id` verifies the child belongs to its invoking
session before replying to OpenCode. Under explicit Ask/auto-deny, a resumed
child with session-specific allow rules is currently rejected: those native
rules can override agent gates. Task requests without an observed tool call are
also rejected rather than granting unverified resume authority. This limitation,
saved permission grant mediation and the unsupported profile fields above remain
part of the parity audit. The Driver adds no host routing or team policy.

Saved project grants are evaluated after the session's rules by OpenCode. Explicit
Ask/auto-deny checks them through the native read-only `opencode db ... --format
json` command, using the current project identity from the native API. Checks
surround permission-cache initialization and are retained for that private
process. Nonempty saved rules, malformed output and failed probes reject the
invocation before prompting; the Driver does not modify grants. Raw includes
probe stdout/stderr. Default/auto-approve keeps native behavior. These checks
prevent a silent bypass; supporting existing saved grants with host mediation
is still an open parity requirement.

The mapping follows the pinned official
[agent configuration](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/config/agent.ts)
and [task tool](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/tool/task.ts).

## Agent source imports

`SourcePath` is an alternative to inline role/configuration fields. Only `Key`,
`RuntimeName`, `SourceFingerprint` and `Metadata` may accompany it. RuntimeName
(or Key when omitted) identifies the imported agent; a name in the file does
not change that identity. Source files are read once per Driver invocation or
sync before profile initialization. They must be regular, nonempty UTF-8 files
no larger than 4 MiB. Explicit source symlinks are resolved to regular files.

```go
adaptor.WithProfileResources(profile.Resources{
    Agents: []profile.SubAgent{{
        Key:         "host/reviewer",
        RuntimeName: "reviewer",
        SourcePath:  "/config/agents/reviewer.md",
    }},
})
```

Pi imports the fields defined by the official
[subagent example loader](https://github.com/earendil-works/pi/blob/v0.86.1/packages/coding-agent/examples/extensions/subagent/agents.ts).
An empty tools list inherits the child's available tools; a nonempty list uses
the existing exact ToolPolicy allow mapping. Markdown body uses this Driver's
`Instructions` mapping (child system-prompt replacement); it does not install
or execute the example extension, whose own prompt handling is additive.

OpenCode imports the native
[Markdown agent schema](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/config/agent.ts):
description, model, variant, temperature, top_p, hidden, color, steps, options
and permission. `mode` must be omitted or `subagent`; `disable` must be omitted
or false. The body overrides frontmatter prompt, matching native loading.
Deprecated `maxSteps` and boolean `tools` are normalized using the native
precedence rules. Permissions accept an action or ordered per-tool/per-pattern
objects. Wildcard and pattern ordering survives sync, clone and invocation
overlays, and altered effective precedence is rejected before prompting.
Source text and options remain literal, including `{file:...}` and `{env:...}`.

Imports currently require strict JSON-compatible YAML. Duplicate/non-string
mapping keys, aliases, merges, custom tags, nonfinite numbers, malformed fields
and unsupported top-level fields fail explicitly. Nesting is limited to 64
levels. Arbitrary OpenCode provider options belong under `options`; its native
unknown-field promotion and permissive invalid-YAML fallback are not emulated.
Broader native source compatibility remains in the parity inventory.

`SyncProfile` persists resolved values rather than a live file reference. A
fresh Agent, including a settings clone, can therefore use the agent after the
source is removed. An Agent still declaring SourcePath requires that source on
each subsequent call or sync. Effective content changes invalidate Thread
compatibility even with a supplied fingerprint. ProfileState remains read-only;
explicit clear and external-edit protections apply to imported agents too.
For OpenCode agents, ownership now includes object insertion order, because
reordering permissions can change behavior.

## Real-provider tests

Ordinary tests use hermetic CLI processes and in-process MCP servers. Paid live
calls require both `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` and the corresponding
`AGENT_ADAPTOR_PI_LIVE=1` / `AGENT_ADAPTOR_OPENCODE_LIVE=1`. Supply an explicit
model via `AGENT_ADAPTOR_PI_MODEL` or `AGENT_ADAPTOR_OPENCODE_MODEL`, optional
`*_COMMAND`, and `AGENT_ADAPTOR_PI_PROVIDER` when using a custom Pi provider.

```sh
go test -v -count=1 -run '^TestLivePiAndOpenCode' .
go test -v -count=1 -run '^TestLivePiApprovals$' .
go test -v -count=1 -run '^TestLivePi(Persistent|RPCDialogs)$' .
go test -v -count=1 -run '^TestLiveOpenCodeApprovals$' .
go test -v -count=1 -run '^TestLiveOpenCodePersistent$' .
go test -v -count=1 -run '^TestLiveOpenCodeProfileAgents$' .
go test -v -count=1 -run '^TestLiveOpenCodeManagedMCPGuard$' .
```

Configure custom endpoints using Pi `models.json` (`openai-responses`, `baseUrl`,
an environment reference for `apiKey`) and OpenCode `opencode.json` (the
`@ai-sdk/openai` provider, `options.baseURL`, `options.apiKey: "{env:KEY}"`).
Use a private profile and inherit the key through the process environment.
Never put a real API key into a test fixture or committed configuration.

The live suite exercises `adaptertest`, Run/Stream, native tools, skills, schema
and instructions, Thread resume, fork isolation, and actual hosted Go Tool calls.
Parser fuzz targets run without credentials; CI runs race tests on Linux.

## Protocol evidence and dependency choice

- [Pi JSON event protocol](https://pi.dev/docs/latest/json),
  [Pi CLI and configuration](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md),
  [Pi extensions](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md).
- [OpenCode CLI](https://dev.opencode.ai/docs/cli/),
  [OpenCode run implementation](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/cli/cmd/run.ts),
  [server wire types](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/sdk/js/src/v2/gen/types.gen.ts),
  [native plan review](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/tool/plan.ts).

Pi's MCP transport
reuses `github.com/modelcontextprotocol/go-sdk`, already pinned by the SDK. It
provides maintained official framing, cancellation and schema contracts and
stays within the Pi Driver boundary. Provider JSON parsing uses `encoding/json`
against explicit event structs; no generic JSON session guessing is introduced.

Markdown import promotes the existing pinned `go.yaml.in/yaml/v4`
`v4.0.0-rc.2` dependency to direct use. Its syntax tree handles quoted values,
block scalars and nested collections while preserving mapping order, avoiding a
handwritten YAML parser. The [YAML organization maintained implementation](https://github.com/yaml/go-yaml)
provides version history, tests and issue tracking; this change reuses the
already-selected version and checksum. Imports are confined to
`internal/agentmarkdown`; Driver-specific field interpretation stays in each
Driver and no dependency type enters the public API. JSONC reconciliation uses
the existing HuJSON dependency, with provider-selected order preservation.
