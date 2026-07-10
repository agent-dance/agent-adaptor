package a2adelegation

import (
	"context"
	"encoding/json"
)

// ToolContext carries host-owned invocation context for one MCP tool call.
type ToolContext struct {
	RunID            string
	ParentToolCallID string
	Tenant           string
}

// ToolSpec describes one stage-oriented MCP tool built on top of Delegator.
//
// BuildRequest should parse tool arguments and produce a DelegationRequest.
// Fields left empty (`RunID`, `ParentToolCallID`, `Tenant`) are backfilled from
// ToolContext by the MCP server.
//
// BuildResult optionally projects the final DelegationResult into a typed tool
// payload. When nil, the raw DelegationResult is returned.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema map[string]any

	BuildRequest func(ctx context.Context, raw json.RawMessage, env ToolContext) (DelegationRequest, error)
	BuildResult  func(ctx context.Context, out DelegationResult) (any, error)
}
