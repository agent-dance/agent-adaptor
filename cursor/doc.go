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
// Nonempty WithAppendSystemPrompt is unsupported and fails before resources
// or process launch. Empty text preserves the existing prompt path. The driver
// neither switches to ACP nor adds persistent processes or interactive approval.
package cursor
