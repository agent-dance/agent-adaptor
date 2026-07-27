package mcp

import "errors"

// MCP declaration and capability sentinels live with the public MCP
// vocabulary. Engine and root re-exports reference these exact values.
var (
	ErrInvalidConfig        = errors.New("agentadaptor: invalid MCP configuration")
	ErrUnsupported          = errors.New("agentadaptor: MCP unsupported by adapter")
	ErrTransportUnsupported = errors.New("agentadaptor: MCP transport unsupported by adapter")
)
