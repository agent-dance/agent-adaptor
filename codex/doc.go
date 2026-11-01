// Package codex provides the built-in Driver implementation for Codex.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(codex.Driver(codex.Config{
//		Model: "gpt-5.4",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and transport availability checks occur when the
// agent runs or is inspected. The appserver subpackage contains the typed
// Codex app-server transport used when that transport is selected.
//
// App-server invocations deliver messages with turn/steer (Codex >=0.100.0),
// interrupt and restart in the same thread, and consume turn-end continuations
// inside one driver Run. Exec invocations expose continuation through resume
// only. The driver probes the configured CLI version and falls back to resume
// when it cannot verify the minimum version required for in-run delivery.
// Interrupt RPC errors retain messages until the active turn completes: a
// natural completion delivers them in a normal continuation, and a failed turn
// requeues them for core to continue delivery.
// Permission policy applies to every SDK-declared MCP server, including the
// private WithTools endpoint and host-supplied WithMCP declarations. Auto-approve
// sets default_tools_approval_mode to "approve"; Ask and Auto-deny set "prompt".
// SDK-declared servers remain enabled under every filesystem sandbox, including
// read-only: the host controls its tools' side effects. Auto-deny requests use
// the core rejection policy. The default renderer preserves explicit server
// defaults and exact tool settings; this version's MCP API does not expose
// those explicit policies.
// Servers owned solely by the user's native profile retain their own settings.
// Invocation config overrides prevent shared-profile policy races.
// SyncProfile has no execution policy and materializes the prompt fallback;
// Send applies its resolved permission policy when preparing the invocation.
//
// App-server MCP confirmation elicitations (empty object forms and URL consent),
// shell approvals, and file approvals route through the core approval handler.
// Nonempty elicitation forms require structured user input and are explicitly
// unsupported; they are never auto-approved as permission confirmations.
//
// Notifications for unrelated threads/turns are ignored. Late usage for turns
// owned by the current execution updates their accounting without repeating
// TurnCompleted. TurnCompleted reports usage observed at completion; Response
// includes all usage observed before return. A resident process also observes
// idle usage snapshots so the next run excludes historical usage. Returned
// results are immutable and are not revised by notifications arriving later.
//
// Native output schemas are sent on every turn/start, including continuation
// and interrupt restarts, and the final turn is validated before completion.
// Responses retain all turns' raw streams and transcript, sum their observed
// usage, and expose the last turn's assistant text and terminal payload.
package codex
