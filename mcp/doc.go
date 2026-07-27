// Package mcp is the v1 vocabulary for declaring Model Context Protocol
// (MCP) servers a host attaches to an agent, replacing hand-written
// configuration structs with one-line constructors:
//
//	adaptor.WithMCP(
//	    mcp.HTTP("docs", "https://example.com/mcp"),
//	    mcp.Stdio("repo-tools", "npx", mcp.Args("repo-mcp")),
//	)
//
// The package is a pure declaration facade: Server is the existing
// driver-level server spec under its consumer-facing name, and the HTTP,
// SSE, and Stdio constructors only fill in its fields. Validation,
// driver-capability checks, profile materialization, and fingerprinting are
// unchanged and happen inside the SDK when a run is prepared, and per-call
// WithMCP keeps its replace (not append) merge semantics.
package mcp
