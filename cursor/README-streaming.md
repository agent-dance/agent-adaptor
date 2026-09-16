# Cursor `stream-json` parser contract

Cursor Driver invokes the Agent CLI with `-p --output-format stream-json` and
parses the documented NDJSON protocol. The protocol authority is Cursor's
[Output format](https://docs.cursor.com/en/cli/reference/output-format)
reference; local fixtures mirror those shapes rather than synthetic adapter
events.

## Recognized events

| Cursor event | Driver projection |
|---|---|
| `system` / `init` | `TranscriptInit`; captures top-level `model` and `session_id` |
| `user.message.content[].type=text` | `TranscriptUser` |
| `assistant.message.content[].type=text` | incremental `TranscriptAssistant`; exact text chunks are concatenated for `Response.Output` |
| `tool_call` / `started` | `TranscriptToolCall`; `call_id` correlates the lifecycle, the nested variant name is `ToolName`, and its `args` are `Input` |
| `tool_call` / `completed` | `TranscriptToolResult`; preserves the nested variant result structurally and extracts readable success content when available |
| `result` / `success` | official terminal payload and `TranscriptResult`; its full `result` text is only an Output fallback when assistant deltas are absent |

Cursor documents that print mode suppresses thinking events, so the Driver
does not infer reasoning from unrelated fields. Unknown additive event types
are preserved as opaque `TranscriptSystem` items and never guessed into text,
tools, terminal state, or checkpoints.

## Output and checkpoint rules

- Assistant chunks are deltas and are concatenated byte-for-byte; whitespace
  is significant.
- Cursor's terminal `result` is the full assistant response, not a bounded
  summary. `Response.Summary` therefore remains empty.
- `RawStreams.Terminal` contains the exact recognized `result.success` JSON.
- A checkpoint is valid only when the process outcome is clean and the
  terminal `result.success` itself carries a non-empty top-level `session_id`.
  An init event cannot substitute for a missing terminal session identifier.
- Non-zero exit, signal, timeout, classified failure, malformed protocol,
  conflicting session IDs, missing terminal, or data after the terminal event
  invalidate checkpoint persistence.

## Dependency choice

The protocol is newline-delimited JSON with a small documented envelope and
open-ended tool variants. Go's maintained `encoding/json` is sufficient and
keeps parsing localized in the Cursor Driver. A provider SDK would not improve
protocol fidelity here because Cursor publishes a wire schema rather than a Go
client library; adding a runtime dependency would increase the audit surface
without improving reliability or maintenance.

## Capability observations and unsupported boundaries

Both values of `driver.Request.Streaming` still execute the same `-p
--output-format stream-json` command. `Descriptor.Observation.Batch` and
`Streaming` consequently both declare MCP and Subagent observation; the Batch
field does not imply a separate terminal-JSON transport. Public `Run` and
`Stream` observe the same facts, with or without a recorder or demand.

The parser recognizes only these exact, catalog-resolved references:

- `mcpToolCall.args.serverIdentifier` maps to the resolved MCP server key;
  `providerIdentifier` is the fixture-proven older spelling, used only when
  `serverIdentifier` is absent. `toolName` or `name` supplies the operation;
  each present spelling must independently be a valid, nonempty string before
  consistency is checked. The installed 2026.07.23 print path also emits
  `name = serverIdentifier + "-" + toolName`. That exact redundant label is
  accepted only with a valid nonempty `serverIdentifier`; a present
  `providerIdentifier` must agree. The operation remains `toolName`, and
  server/operation identity never comes from splitting the label. A malformed
  known spelling cannot be treated as absent; unknown additive fields remain
  opaque. Plain equal aliases and the name-only/toolName-only fixtures remain
  supported.
- `taskToolCall.args.subagentType.custom.name` maps to a unique resolved profile
  agent RuntimeName and emits its canonical Key with operation `spawn`.
- Only official `tool_call.started` and `tool_call.completed` envelopes with
  `call_id` and a consistent `session_id` participate. A terminal needs one
  explicit success/failure result member or a recognized result oneof case.
  Unknown/conflicting outcomes never become successful calls.

Facts use the existing typed CapabilityInvocation event, with ProviderProtocol
and Provider provenance. Duplicate starts and terminals do not emit duplicates;
changed references or arguments are rejected. Unfinished calls close as
Interrupted, cancellation as Cancelled, and malformed protocol as
Failed/ProtocolError. Parent coordinates and Duration remain empty because this
print protocol does not establish them. State and replay tombstones are bounded
at 4096 invocations per run; catalog entries have the same limit. A safe,
once-per-reason runtime notice reports rejected or unrecognized observations.
These notices contain no protocol body, tool arguments, or result contents.
Raw retains original bytes. The formal opaque `call_id` can contain LF.
Safe IDs outside the reserved `cursor:call-id:` domain keep their spelling;
all other nonempty valid UTF-8 IDs are represented as that prefix plus
unpadded URL-base64 of the complete original ID. Reserved-domain literals are
encoded too, so no literal can collide with an encoded ID. Safe IDs are bounded
at 2048 bytes and IDs needing encoding at 1400 bytes. Transcript tool-use IDs
and capability lifecycle IDs use the same normalization; no component is
trimmed, split, or guessed into parent identity.

Skill synchronization remains supported, while **skill invocation observation
is unsupported**: a `/review` prompt or user echo does not prove activation.
Todo/Plan observation, native append system prompt, native JSON Schema, safe
fork, persistent processes, and interactive approvals remain unsupported.
Demand for skill/todo observations produces the core's `observation_unavailable`
notice listing only those missing items. It does not invent a zero-call report
or an empty plan. Missing observations do not prove that a capability was unused.

Nonempty `WithAppendSystemPrompt` fails with
`*driver.SystemPromptUnsupportedError`, reason `unsupported_driver`, before
profile materialization or process launch; direct Driver.Run also enforces this.
The empty string clears a default and preserves the existing user prompt and
instructions behavior. Invalid UTF-8 and NUL keep the shared validation error
precedence. No content is redirected into user prompt or instructions.

The documented print terminal does not establish token usage. Usage remains
nil; unrecognized usage fields stay in Raw/terminal without guessed numbers.

## Real-provider conformance entry points

Both existing `TestCursorDriverConformance` live probes and
`TestAlignmentLiveCursor*` require `-tags=cursor_live` **and**
`AGENT_ADAPTOR_LIVE_CONFORMANCE=1`. Ordinary tests never invoke the real CLI,
including when it is installed or that environment variable alone is set.
Live tests allocate fresh HOME, USERPROFILE, CURSOR_HOME, and workspace paths,
and never read/copy an operator profile. The authorized runner supplies approved
credentials through its environment, plus optional
`AGENT_ADAPTOR_CURSOR_COMMAND` and `AGENT_ADAPTOR_CURSOR_MODEL`. Once enabled,
missing CLI or required formal evidence fails instead of being skipped.

The live entry points cover print output and real resume, MCP plus a custom
Subagent, cancellation with partial Result, and the append/skill/todo negative
matrix. `TestAlignmentLiveCursorDedicatedToolsColdResume` additionally uses
public Agent/Thread calls with the same store, Dedicated source, workspace,
identity and tools across a successful bounded Agent.Close. Its second Agent
uses ResumeOnly under the same key and must recall a first-turn random nonce
without receiving it in the new prompt or tool response. Both turns require
actual hosted-tool calls and healthy public checkpoints with the original
provider session identity. The test checks nonempty provider session files in
the effective isolated profile before/after Close and before the cold dispatch,
plus gateway revocation and endpoint/credential rotation. The file probe uses
the installed print/resume module's exact config-root path:
`chats/md5(path.resolve(cwd))/sessionID/store.db`. It requires real nonempty
regular database files and rejects links. Same-ID project transcript folders
are audit copies, not the source used for resume. Unknown layouts fail rather
than substitute an SDK checkpoint or marker file. A forwarding test
Driver records only resolved inputs and delegates to the real configured Driver.
Cursor still spawns each turn; this test adds no persistent-process claim.
Each run captures CLI `--version` in the isolated environment. Native
schema, interactive approvals, persistent-process and fork probes are not
applicable. Example for an authorized isolated runner:

```sh
AGENT_ADAPTOR_LIVE_CONFORMANCE=1 go test -count=1 -tags=cursor_live ./cursor \
  -run 'TestCursorDriverConformance|TestAlignmentLiveCursor'
```

Ordinary CI leaves the environment gate closed. Live runners must be explicitly
authorized and supply isolated authentication. Local macOS diagnostic passes
do not substitute for final integration live or native Windows/Linux gates.

## Native roots, selected profiles and isolated resource delivery

The installed `2026.07.23-e383d2b` CLI implements three independent roots.
`CURSOR_HOME` is an SDK selector and is not a CLI input. Config bindings follow
the same explicit-empty and fallback semantics during Inspect and Run.

| Selection | CLI config and resumable chats | CLI project data/transcripts | User MCP/skills/hooks and profile agents |
|---|---|---|---|
| Native | `CURSOR_CONFIG_DIR`, then `XDG_CONFIG_HOME/cursor`, then `HOME/.cursor` | `CURSOR_DATA_DIR`, then `HOME/.cursor` | actual `HOME/.cursor`; agents through a temporary agents-only plugin |
| SDK `CURSOR_HOME`, Dedicated, Clone | selected profile directory | selected profile directory | selected source projected per invocation |

Native preserves HOME. Its Profile.Dir reports the resource root; config and
auth probes use the actual separate config root. A clone from that Native root
copies requested MCP/skills from the resource root and settings/auth from the
config root; it never imports chats, projects or history. `IncludeSettings`
with AuthNone copies only the installed CLI's recognized static configuration
fields, excluding authentication, cache/history and unknown fields. Mixed config
reads require bounded regular files, with handle/path identity validation and
nonblocking/no-follow opens; legitimate AuthLink targets remain supported. AuthLink
and AuthCopy retain their explicit existing mixed-file selection semantics.

An isolated invocation owns this layout:

```text
selected-profile/
  cli-config.json, chats/, projects/       # persistent provider sources/state
  mcp.json, hooks.json, skills/, agents/   # resolved SDK resource sources
  .agent-adaptor-home/
    .agent-adaptor-owner
    cursor-run-<random>/                   # this invocation's HOME/USERPROFILE
      .agent-adaptor-owner
      .cursor/{mcp.json,hooks.json,skills/}
      agents-plugin/agents/               # only --plugin-dir payload
```

The empty parent and ownership marker publish atomically without replacement.
Runs have distinct projections, so no extra shared writer/lock is introduced.
The plugin basename is always `agents-plugin`, independent of random run ID.
Native MCP bytes are copied unchanged, preserving the official `${env:NAME}`
expansion; the agents plugin never duplicates MCP. Only top-level skill links
whose targets match this invocation's resolved skill sources may be copied;
unknown links and special nodes fail. Copy bounds are 4096 entries, 64 levels,
8 MiB per file and 32 MiB total. Directory reads use fixed 64-entry batches.

Directories/files are owner-only on Unix; Windows creates and validates
protected owner/SYSTEM DACLs before writing. Reuse verifies directory type,
permissions and marker identity/content. Cleanup remains anchored to held
roots, checks ownership and directory identity, never follows links, and is
bounded by 16384 entries, 64 levels and a 15-second cancellation deadline.
Failures are observable; post-execution cleanup failure preserves the partial
Response/Raw/Transcript/terminal but removes its checkpoint. Cancellation and
launch failure clean their own run projection. Persistent chats remain after
Close. Native source directories are never projection cleanup targets.

Official config/data/resource paths are pinned before any resource sync;
runtime credential contributions cannot redirect them, and ExtraArgs cannot
replace `--data-dir` or `--plugin-dir`. Read-only resolution creates no execution
HOME. Random projection paths never enter stable compatibility fingerprints.
The Driver guard hashes canonical config JSON (only formal `authInfo` is
excluded; unknown fields and caches remain), plus sorted actual agents/hooks
paths, content and permission modes. The same reads that create the isolated
agents/hooks projection produce its guard, using stable source-relative names.
Native agents use their copied plugin snapshot; Native hooks have a prelaunch
source guard and remain subject to external edits before the CLI's own read.
It excludes dynamic hosted MCP endpoint
and credential values owned by the existing resolved invocation contract.
Missing proofs in old checkpoints reject before launch with ErrResumeRejected;
ResumeOnly preserves the old healthy store, and continue-or-start retains the
existing single safe fresh fallback. This guard does not copy unknown settings
into an AuthNone clone.

The cancellation fixture explicitly trusts its own temporary workspace and
uses official `--stream-partial-output`. It cancels only after a same-RunID,
nonempty formal assistant `NoticeTranscriptItem` actually arrives. That print
contract does not require public TextDelta. It checks cancellation reason and
cause, observed partial Raw/Text/Transcript, bounded teardown and unchanged
healthy checkpoint. `TestAlignmentLiveCursorWorkspaceTrustRequired` retains
the original untrusted-workspace refusal as a separate negative control.
`TestAlignmentLiveCursorDedicatedResourceIsolation` first proves private Native
MCP/skill access, then requires selected MCP/skill/subagent access while the old
Native MCP stays live but receives no new requests and old-only nonces stay out
of the result. This confirms resource delivery, not skill invocation telemetry.
