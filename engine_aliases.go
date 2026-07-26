package agentadaptor

// This file keeps the historical root-package consumer surface intact after
// the execution pipeline and its contract types moved into internal/engine
// (docs/api-v1-implementation-plan.md P0.2, docs/p0-inventory.md).
//
// Every name below is a type/const alias (or value re-export) for the
// identical declaration in internal/engine, so existing hosts, adapters,
// bridges, and tests compile and behave exactly as before. Go permits a
// public package to export aliases of internal-package types; downstream
// code keeps using the root names.

import "github.com/agent-dance/agent-adaptor/internal/engine"

// --- Adapter configs (was config_types.go) ---------------------------------

type (
	CommonConfig            = engine.CommonConfig
	CodexConfig             = engine.CodexConfig
	ClaudeConfig            = engine.ClaudeConfig
	CursorConfig            = engine.CursorConfig
	CodeBuddyConfig         = engine.CodeBuddyConfig
	CodeBuddyPermissionMode = engine.CodeBuddyPermissionMode
	ReasoningEffort         = engine.ReasoningEffort
	ThinkingEffort          = engine.ThinkingEffort
	CursorMode              = engine.CursorMode
)

const (
	CodeBuddyPermissionUnset       = engine.CodeBuddyPermissionUnset
	CodeBuddyPermissionDefault     = engine.CodeBuddyPermissionDefault
	CodeBuddyPermissionAcceptEdits = engine.CodeBuddyPermissionAcceptEdits
	CodeBuddyPermissionPlan        = engine.CodeBuddyPermissionPlan
	CodeBuddyPermissionAuto        = engine.CodeBuddyPermissionAuto
	CodeBuddyPermissionDontAsk     = engine.CodeBuddyPermissionDontAsk
	CodeBuddyPermissionBypass      = engine.CodeBuddyPermissionBypass
	CodeBuddyPermissionFullAccess  = engine.CodeBuddyPermissionFullAccess
)

// --- Workspace vocabulary (was workspace_skill_types.go) -------------------

type (
	WorkspaceReleaseMode    = engine.WorkspaceReleaseMode
	WorkspaceStrategy       = engine.WorkspaceStrategy
	WorkspaceRuntimeConfig  = engine.WorkspaceRuntimeConfig
	WorkspaceRequest        = engine.WorkspaceRequest
	WorkspaceSpec           = engine.WorkspaceSpec
	WorkspaceRequestData    = engine.WorkspaceRequestData
	GitWorktreeWorkspace    = engine.GitWorktreeWorkspace
	SharedWorkspace         = engine.SharedWorkspace
	AdapterManagedWorkspace = engine.AdapterManagedWorkspace
	RuntimeServiceRequest   = engine.RuntimeServiceRequest
)

const (
	WorkspaceReleaseKeep = engine.WorkspaceReleaseKeep
	WorkspaceReleaseStop = engine.WorkspaceReleaseStop
)
