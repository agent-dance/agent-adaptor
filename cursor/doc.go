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
// To accept messages at tool boundaries, call RunHookRelayIfRequested first in
// main, before parsing arguments. Avoid expensive init functions in that binary,
// since Cursor starts it for every hook. Config.HookRelay can override the relay
// argv; a custom relay must answer the appended --agent-adaptor-hook-probe
// argument with {"agent_adaptor_cursor_hook":"v1"} and implement the same bounded,
// token-authenticated loopback protocol. The default argv is the host executable
// followed by __agent_adaptor_cursor_hook.
//
// Tool-boundary delivery requires Cursor >=2026.10.14, a verified relay, and a
// workspace explicitly marked git_worktree, cloud_sandbox or driver_managed.
// Windows, primary/unspecified workspaces and existing .cursor/hooks.json files
// use turn-end resume delivery with a lifecycle notice. Even an SDK-marked
// existing file is left untouched because another run may own it. Successful
// installation is exclusive across host processes and cleanup removes only the
// exact managed file. A hook reply is acknowledged only after Cursor echoes the
// complete nonce-tagged context in hookAdditionalContexts; unconfirmed claims
// return to core for retry. Delivered context persistence across resume remains
// unverified, so PersistsDelivered is false.
package cursor
