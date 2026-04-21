package appserver

// go-jsonschema emits one Go type per JSON Schema document plus any nested
// types the schemas $ref. We restrict generation to flat notification
// schemas that the codex adapter consumes verbatim (agent-message deltas,
// reasoning deltas, command-output deltas, token-usage updates, errors).
// Envelope types with discriminated unions (ThreadItem, UserInput,
// SandboxPolicy, etc.) are hand-written in union.go because go-jsonschema
// degrades oneOf to interface{}.
//
// Keep this list in sync with the notification map in translate.go. When
// Codex ships a new protocol version, rerun:
//
//	codex app-server generate-json-schema --out codex/appserver/schema/
//	go generate ./codex/appserver/...
//
// and reconcile the diff.

//go:generate go-jsonschema -p appserver -t --tags json --only-models --capitalization ID --capitalization URL --capitalization JSON --capitalization URI --capitalization UUID schema/v2/AgentMessageDeltaNotification.json schema/v2/ReasoningTextDeltaNotification.json schema/v2/ReasoningSummaryTextDeltaNotification.json schema/v2/ReasoningSummaryPartAddedNotification.json schema/v2/CommandExecOutputDeltaNotification.json schema/v2/CommandExecutionOutputDeltaNotification.json schema/v2/FileChangeOutputDeltaNotification.json schema/v2/PlanDeltaNotification.json schema/v2/ErrorNotification.json schema/v2/ThreadTokenUsageUpdatedNotification.json -o generated.go
