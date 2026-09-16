// Package cursor provides the built-in Driver implementation for the Cursor
// Agent CLI.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(cursor.Driver(cursor.Config{
//		Model: "gpt-5",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and CLI availability checks occur when the agent
// runs or is inspected.
//
// Both Run and Stream use Cursor print stream-json. Formal MCP and custom
// subagent calls are observed only through exact resolved-catalog matches;
// skill activation and Todo observation remain unavailable. Skill resource
// synchronization is independent from observing its activation. Unrecognized
// facts produce safe notices and preserve Raw and Transcript.
//
// Native profiles preserve Cursor's independent config, data, and HOME/.cursor
// resource roots. The legacy SDK CURSOR_HOME selector, Dedicated and Clone
// map their selected directory to official CURSOR_CONFIG_DIR/CURSOR_DATA_DIR.
// Each isolated invocation projects only MCP, skills and hooks into its own
// private HOME and profile agents into an agents-only plugin. No native HOME
// resource is imported, and temporary projections do not carry session data.
// Windows objects use protected owner/SYSTEM DACLs established before writes.
//
// Cursor 2026.07.23 stores print/resume SQLite chats below the config root;
// project artifacts and transcripts use the data root. Both roots, the resource
// source, configuration (except formal authInfo) and actual agents/hooks enter
// the resume guard. Old checkpoints missing these proofs are rejected before
// launch; normal continue-or-start may use the existing safe fresh fallback.
// Raw call_id bytes are retained in Raw. Opaque IDs requiring escaping and
// IDs in the reserved cursor:call-id: domain are encoded without collision in
// Transcript and capability facts; ordinary safe IDs retain their spelling.
//
// Nonempty WithAppendSystemPrompt is unsupported and fails before resources
// or process launch. Empty text preserves the existing prompt path. The driver
// neither switches to ACP nor adds persistent processes or interactive approval.
package cursor
