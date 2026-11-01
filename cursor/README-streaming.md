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

## In-run messages and hook relay

Applications start one execution through `Runner.Send`; supplemental input goes
through `Stream.Send`. The Driver statically declares `tool` delivery with core
`ContinueResume`, no interrupt, echo confirmation, `PersistsDelivered=false`, and
minimum Cursor version **2026.10.14**. The minimum is a requirement; the current
probe does not verify the installed CLI version.

The host binary must dispatch the relay **before argument parsing**:

```go
package main

import "github.com/agent-dance/agent-adaptor/v2/cursor"

func main() {
	cursor.RunHookRelayIfRequested()
	// The host's normal startup follows here.
}
```

The default hook command re-executes this binary with the SDK's hidden command.
Keep initialization cheap: each hook starts a process. Alternatively,
`cursor.Config.HookRelay` can select a separate relay binary; it must implement
the same SDK dispatch/probe (normally by calling RunHookRelayIfRequested). The
Driver probes the command before launching Cursor. A missing handshake, failed
probe, listener failure or hook setup failure emits a lifecycle Notice with
code `cursor_hook_unavailable` and leaves messages for core checkpoint resume.
The prompt can continue without tool delivery.

Only an explicitly managed workspace using GitWorktree, CloudSandbox or
DriverManaged strategy is eligible. A plain WithWorkspace path uses the primary
workspace strategy and does not enable hooks. The Driver creates
`.cursor/hooks.json` exclusively, never overwrites an existing file (even an
SDK-marked one), and refuses a symlink/non-directory `.cursor`. It installs
`postToolUse` and `postToolUseFailure`, and removes only the exact file it owns;
a replaced or edited file is preserved and cleanup errors remain observable.
**Windows relay is unavailable**, so Windows uses turn-end resume too.

The relay sends only hook event, conversation ID, tool-use ID and parent-tool ID
to an authenticated loopback endpoint; tool arguments and results stay out of
that request. It disables proxies and redirects and returns only
`additional_context`, or `{}` on failure. The Driver claims only main-agent
callbacks for the active conversation and tool call. The first eligible callback
opens the delivery window; termination closes it.

Each execution generates a random 128-bit nonce tag and tells the model the
exact tag in its prompt. Tool-output text cannot substitute for authenticated
hook guidance. A hook response is **not** a delivered receipt: the completed
tool event must contain the full tagged batch in the official
`hookAdditionalContexts[].content` field. Matching the nonce alone or content
nested in a tool result is insufficient. Missing echo requeues for turn-end
resume; ending with an unconfirmed claim also marks it MaybeDelivered so core
adds a possible-duplicate warning. Every claim is settled before Driver return.

The **entire** additional context, including nonce tag and batch separators, is
limited to **10000 UTF-16 code units**. Descriptor.Messages.MaxUnits reserves the
tag overhead, so core rejects an oversized framed message at admission; astral
characters count as two units. A batch that would overflow requeues its remaining
items instead of truncating them. The limit still applies when the resolver trims
tool delivery. AfterTurn and input arriving after the last tool boundary use the
healthy checkpoint through core resume. Ephemeral hook input is retained as
bounded earlier guidance for subsequent Thread resumes. Provider acceptance and
receipt persistence still have a crash window in which replay is possible.

Every returned turn emits `RunEventTurnCompleted`, which core projects as
`TurnEnd`; only core's final `Done` contains the authoritative execution outcome.
No Cursor stop hook is advertised for print mode.

## Dependency choice

The protocol is newline-delimited JSON with a small documented envelope and
open-ended tool variants. Go's maintained `encoding/json` is sufficient and
keeps parsing localized in the Cursor Driver. A provider SDK would not improve
protocol fidelity here because Cursor publishes a wire schema rather than a Go
client library; adding a runtime dependency would increase the audit surface
without improving reliability or maintenance.
