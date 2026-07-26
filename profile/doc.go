// Package profile is the v1 vocabulary package for provider profile
// selection and profile resource declarations (docs/api-v1-redesign.md §2.9,
// docs/api-v1-implementation-plan.md P3.4).
//
// It answers two host questions with one import:
//
//   - Where does the provider profile live? Native(), Dedicated(dir),
//     CloneNative(dir, ...), and CloneFrom(src, dst, ...) build the same
//     normalized Selection that the historical root options
//     WithNativeProfile / WithDedicatedProfile / WithCloneProfile /
//     WithCloneProfileFrom produce today. Clone constructors accept
//     CloneOption values such as LinkAuth() to share OAuth login state
//     without copying token files.
//
//   - What must exist inside it? Resources declares desired sub-agents,
//     hooks, instructions, and structured config patches. SubAgent, Hook,
//     Instructions, and ConfigPatch (plus their enum families) are
//     re-exported here so a host can write a complete declaration with only
//     this package imported.
//
// Every name in this package is an alias for (or a pure constructor over)
// the existing public contract types; nothing changes behavior. Skill and
// MCP entries inside Resources keep their own vocabulary packages (skill/,
// mcp/) — until those land, the root package aliases (SkillRef, MCPConfig)
// remain the element types. Option wiring (adaptor.WithProfile /
// WithProfileResources in the v1 consumer package) arrives in a later wave;
// the truthful materialization report stays on the agent surface
// (ProfileState / SyncProfile).
package profile
