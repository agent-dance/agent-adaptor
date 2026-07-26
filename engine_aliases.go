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

// --- Run result / host-hook managers (was run_types.go) --------------------

type (
	RunResult             = engine.RunResult
	WorkspaceManager      = engine.WorkspaceManager
	RuntimeServiceManager = engine.RuntimeServiceManager
)

// --- Sessions (was session_types.go) ----------------------------------------

type (
	SessionRequest             = engine.SessionRequest
	SessionCompatibilityStatus = engine.SessionCompatibilityStatus
	SessionCompatibility       = engine.SessionCompatibility
	SessionRef                 = engine.SessionRef
	SessionQuery               = engine.SessionQuery
	SessionStatus              = engine.SessionStatus
	SessionRecord              = engine.SessionRecord
	SessionLease               = engine.SessionLease
	SessionFinalizeRequest     = engine.SessionFinalizeRequest
	SessionStore               = engine.SessionStore
)

const (
	SessionCompatibilityNew          = engine.SessionCompatibilityNew
	SessionCompatibilityCompatible   = engine.SessionCompatibilityCompatible
	SessionCompatibilityIncompatible = engine.SessionCompatibilityIncompatible
)

const (
	SessionStatusActive   = engine.SessionStatusActive
	SessionStatusArchived = engine.SessionStatusArchived
)

// --- MCP config (was mcp_types.go) -------------------------------------------

// MCPConfig is the binding-level or per-run collection of MCP servers. Per-run
// WithMCP replaces the full effective config rather than appending to defaults.
type MCPConfig = engine.MCPConfig

// --- Skills (was skill_types.go) ---------------------------------------------

type (
	SkillFromPath             = engine.SkillFromPath
	SkillFromFS               = engine.SkillFromFS
	SkillFromInline           = engine.SkillFromInline
	SkillProvider             = engine.SkillProvider
	SkillCatalog              = engine.SkillCatalog
	SkillSet                  = engine.SkillSet
	SkillMaterializer         = engine.SkillMaterializer
	SkillKeyConflictError     = engine.SkillKeyConflictError
	SkillMaterializationError = engine.SkillMaterializationError
)

// --- HITL typed handlers (was decision_types.go) -----------------------------

type (
	PermissionRequest  = engine.PermissionRequest
	PlanReviewRequest  = engine.PlanReviewRequest
	QuestionRequest    = engine.QuestionRequest
	ApprovalResult     = engine.ApprovalResult
	QuestionResult     = engine.QuestionResult
	PermissionResponse = engine.PermissionResponse
	PlanReviewResponse = engine.PlanReviewResponse
	QuestionResponse   = engine.QuestionResponse
	PermissionHandler  = engine.PermissionHandler
	PlanReviewHandler  = engine.PlanReviewHandler
	QuestionHandler    = engine.QuestionHandler
)

const (
	// ApprovalApproved is returned by Permission/PlanReview handlers to approve.
	ApprovalApproved = engine.ApprovalApproved
	// ApprovalRejected is returned by Permission/PlanReview handlers to reject.
	ApprovalRejected = engine.ApprovalRejected
)

const (
	// QuestionAnswered means the handler supplied an Answer/Choice payload.
	QuestionAnswered = engine.QuestionAnswered
	// QuestionRejected means the handler declined to answer the question.
	QuestionRejected = engine.QuestionRejected
)

// --- Profile resources / snapshots (was profile_resources.go) ----------------

type (
	ProfileKind                    = engine.ProfileKind
	ProfileResourceKind            = engine.ProfileResourceKind
	ProfileResources               = engine.ProfileResources
	ResourceSnapshot               = engine.ResourceSnapshot
	ProfileResourceSupport         = engine.ProfileResourceSupport
	ProfileResourceMaterialization = engine.ProfileResourceMaterialization
	ProfileSnapshot                = engine.ProfileSnapshot
)

const (
	ProfileKindShared      = engine.ProfileKindShared
	ProfileKindHostManaged = engine.ProfileKindHostManaged
)

const (
	ProfileResourceSkills       = engine.ProfileResourceSkills
	ProfileResourceMCP          = engine.ProfileResourceMCP
	ProfileResourceAgents       = engine.ProfileResourceAgents
	ProfileResourceHooks        = engine.ProfileResourceHooks
	ProfileResourceInstructions = engine.ProfileResourceInstructions
	ProfileResourceConfig       = engine.ProfileResourceConfig
)

const (
	ProfileResourceSupportPortableCore     = engine.ProfileResourceSupportPortableCore
	ProfileResourceSupportPortableExtended = engine.ProfileResourceSupportPortableExtended
	ProfileResourceSupportNativeEscape     = engine.ProfileResourceSupportNativeEscape
	ProfileResourceSupportFallback         = engine.ProfileResourceSupportFallback
	ProfileResourceSupportUnsupported      = engine.ProfileResourceSupportUnsupported
)

const (
	ProfileResourceMaterializationNativeManaged   = engine.ProfileResourceMaterializationNativeManaged
	ProfileResourceMaterializationFileManaged     = engine.ProfileResourceMaterializationFileManaged
	ProfileResourceMaterializationPromptInjected  = engine.ProfileResourceMaterializationPromptInjected
	ProfileResourceMaterializationFallback        = engine.ProfileResourceMaterializationFallback
	ProfileResourceMaterializationNotMaterialized = engine.ProfileResourceMaterializationNotMaterialized
)
