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
