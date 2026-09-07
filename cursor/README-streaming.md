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
  their equality is checked. A malformed known spelling cannot be treated as
  absent, while unknown additive fields remain opaque. Names are compared without trimming,
  normalization, underscore splitting, or prefix matching.
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
Raw and the existing tool Transcript remain intact.

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
and never read/copy an operator profile. The authorized runner supplies API-key
authentication through its environment, plus optional
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
the exact formal session ID as a directory name; unknown or ambiguous layouts
fail rather than substitute an SDK checkpoint or marker file. A forwarding test
Driver records only resolved inputs and delegates to the real configured Driver.
Cursor still spawns each turn; this test adds no persistent-process claim.
Each run captures CLI `--version` in the isolated environment. Native
schema, interactive approvals, persistent-process and fork probes are not
applicable. Example for an authorized isolated runner:

```sh
AGENT_ADAPTOR_LIVE_CONFORMANCE=1 go test -count=1 -tags=cursor_live ./cursor \
  -run 'TestCursorDriverConformance|TestAlignmentLiveCursor'
```

T17 supplies these tests but does not run real providers or paid calls. macOS
fixture results are not native Windows/Linux or live acceptance evidence.
