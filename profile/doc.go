// Package profile defines provider profile selection and profile resource
// declarations (see docs/api-v1-redesign.md §2.9).
//
// It answers two host questions with one import:
//
//   - Where does the provider profile live? Native(), Dedicated(dir),
//     CloneNative(dir, ...), and CloneFrom(src, dst, ...) build the same
//     normalized Selection consumed by adaptor.WithProfile. Clone constructors
//     accept CloneOption values such as LinkAuth() to share OAuth login state
//     without copying token files.
//
//   - What must exist inside it? Resources declares desired skills, MCP
//     servers, sub-agents, hooks, instructions, and structured config
//     patches. Resource types and enum families are owned here; skill.Ref
//     and mcp.Server retain the vocabularies of their dedicated leaf packages.
//
// Resource declarations never expose the Driver SPI or internal engine
// representations. adaptor.WithProfileResources converts and owns a deep
// copy at the option boundary. Nil resource slices are undeclared; non-nil
// empty MCP, sub-agent, hook, or config slices explicitly clear SDK-managed
// entries. The truthful materialization report stays on the Agent surface
// (ProfileState / SyncProfile).
package profile
