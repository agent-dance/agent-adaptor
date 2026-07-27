package agentadaptor

// This file keeps the historical root-package SPI surface intact after the
// driver SPI moved into the driver subpackage (see docs/api-v1-redesign.md
// §2.10 and docs/api-v1-implementation-plan.md P0.3/P0.4).
//
// Every name below is a type/const alias for the identical declaration in
// github.com/agent-dance/agent-adaptor/driver, so existing hosts, adapters,
// bridges, and tests compile and behave exactly as before. Driver authors
// should prefer importing the driver package directly; consumers keep using
// the root package.

import "github.com/agent-dance/agent-adaptor/driver"

// --- Core SPI (was api.go) ------------------------------------------------

// DriverAdapter is the adapter SPI implemented by built-in and third-party
// agent integrations. It is an alias for [driver.Driver].
type DriverAdapter = driver.Driver

// EnvironmentAwareDriver is an alias for [driver.EnvironmentProbe].
type EnvironmentAwareDriver = driver.EnvironmentProbe

// ModelAwareDriver is an alias for [driver.ModelLister].
type ModelAwareDriver = driver.ModelLister

// ModelDetectorDriver is an alias for [driver.ModelDetector].
type ModelDetectorDriver = driver.ModelDetector

// ProfileAwareDriver is an alias for [driver.ProfileReporter].
type ProfileAwareDriver = driver.ProfileReporter

// SessionCodecAwareDriver is an alias for [driver.SessionCodecProvider].
type SessionCodecAwareDriver = driver.SessionCodecProvider

// ConfigSchemaAwareDriver is an alias for [driver.ConfigSchemaProvider].
type ConfigSchemaAwareDriver = driver.ConfigSchemaProvider

// QuotaAwareDriver is an alias for [driver.QuotaProbe].
type QuotaAwareDriver = driver.QuotaProbe

// SkillAwareDriver is an alias for [driver.SkillSupport].
type SkillAwareDriver = driver.SkillSupport

// StreamAwareDriver is an alias for [driver.StreamSupport].
type StreamAwareDriver = driver.StreamSupport

// EventSink is an alias for [driver.EventSink].
type EventSink = driver.EventSink

// DecisionCapableSink is an alias for [driver.DecisionCapableSink].
type DecisionCapableSink = driver.DecisionCapableSink

// StreamCapability is an alias for [driver.StreamCapability].
type StreamCapability = driver.StreamCapability

// DriverDescriptor is the adapter's static capability declaration. It is an
// alias for [driver.Descriptor].
type DriverDescriptor = driver.Descriptor

// Capability declaration blocks referenced by DriverDescriptor.
type (
	SessionCapability          = driver.SessionCapability
	SkillCapability            = driver.SkillCapability
	InstructionsCapability     = driver.InstructionsCapability
	WorkspaceCapability        = driver.WorkspaceCapability
	RuntimeCapability          = driver.RuntimeCapability
	StructuredOutputCapability = driver.StructuredOutputCapability
	RunPolicyCapabilities      = driver.RunPolicyCapabilities
	ConfigSchema               = driver.ConfigSchema
	ConfigOption               = driver.ConfigOption
	ConfigField                = driver.ConfigField
)

// --- Run request / response (was run_types.go) ----------------------------

// DriverRunRequest is the fully resolved invocation the SDK passes to an
// adapter. It is an alias for [driver.Request].
type DriverRunRequest = driver.Request

// DriverRunResult is the adapter-facing execution result. It is an alias for
// [driver.Response].
type DriverRunResult = driver.Response

// DriverSessionContext is an alias for [driver.SessionContext].
type DriverSessionContext = driver.SessionContext

// DriverSessionState is an alias for [driver.SessionState].
type DriverSessionState = driver.SessionState

// DriverCheckpoint is an alias for [driver.Checkpoint].
type DriverCheckpoint = driver.Checkpoint

type (
	AgentIdentity   = driver.AgentIdentity
	RawStreams      = driver.RawStreams
	TerminalPayload = driver.TerminalPayload
	Usage           = driver.Usage
	RunQuestion     = driver.RunQuestion
	RunChoice       = driver.RunChoice
	RunFailure      = driver.RunFailure
)

// --- Events / transcript / streaming (was run_types.go) -------------------

type (
	RunEventType   = driver.RunEventType
	RunEvent       = driver.RunEvent
	TranscriptKind = driver.TranscriptKind
	TranscriptItem = driver.TranscriptItem
	StreamKind     = driver.StreamKind
	Role           = driver.Role
	StreamPayload  = driver.StreamPayload
)

const (
	RunEventChunk      = driver.RunEventChunk
	RunEventItem       = driver.RunEventItem
	RunEventInvocation = driver.RunEventInvocation
	RunEventSpawn      = driver.RunEventSpawn
	RunEventRuntime    = driver.RunEventRuntime
	RunEventLifecycle  = driver.RunEventLifecycle
)

const (
	TranscriptAssistant  = driver.TranscriptAssistant
	TranscriptThinking   = driver.TranscriptThinking
	TranscriptUser       = driver.TranscriptUser
	TranscriptToolCall   = driver.TranscriptToolCall
	TranscriptToolResult = driver.TranscriptToolResult
	TranscriptInit       = driver.TranscriptInit
	TranscriptResult     = driver.TranscriptResult
	TranscriptStdout     = driver.TranscriptStdout
	TranscriptStderr     = driver.TranscriptStderr
	TranscriptSystem     = driver.TranscriptSystem
	TranscriptSummary    = driver.TranscriptSummary
	TranscriptQuestion   = driver.TranscriptQuestion
	TranscriptFailure    = driver.TranscriptFailure
)

const (
	StreamRunStarted       = driver.StreamRunStarted
	StreamRunFinished      = driver.StreamRunFinished
	StreamRunError         = driver.StreamRunError
	StreamStepStarted      = driver.StreamStepStarted
	StreamStepFinished     = driver.StreamStepFinished
	StreamTextStart        = driver.StreamTextStart
	StreamTextContent      = driver.StreamTextContent
	StreamTextEnd          = driver.StreamTextEnd
	StreamToolCallStart    = driver.StreamToolCallStart
	StreamToolCallArgs     = driver.StreamToolCallArgs
	StreamToolCallEnd      = driver.StreamToolCallEnd
	StreamToolCallResult   = driver.StreamToolCallResult
	StreamReasoningStart   = driver.StreamReasoningStart
	StreamReasoningContent = driver.StreamReasoningContent
	StreamReasoningEnd     = driver.StreamReasoningEnd
	StreamHITLRequested    = driver.StreamHITLRequested
	StreamHITLResolved     = driver.StreamHITLResolved
	StreamDropped          = driver.StreamDropped
)

const (
	RoleAssistant = driver.RoleAssistant
	RoleUser      = driver.RoleUser
)

// --- Structured output (was run_types.go) ---------------------------------

type (
	OutputFormat                  = driver.OutputFormat
	StructuredOutputMode          = driver.StructuredOutputMode
	StructuredOutputInvalidPolicy = driver.StructuredOutputInvalidPolicy
	OutputSchema                  = driver.OutputSchema
	StructuredOutputSource        = driver.StructuredOutputSource
	StructuredOutput              = driver.StructuredOutput
)

const OutputFormatJSONSchema = driver.OutputFormatJSONSchema

const (
	StructuredOutputNativeStrict   = driver.StructuredOutputNativeStrict
	StructuredOutputPreferNative   = driver.StructuredOutputPreferNative
	StructuredOutputPromptValidate = driver.StructuredOutputPromptValidate
)

const (
	StructuredOutputFailRun       = driver.StructuredOutputFailRun
	StructuredOutputReturnInvalid = driver.StructuredOutputReturnInvalid
)

const (
	StructuredOutputSourceNative         = driver.StructuredOutputSourceNative
	StructuredOutputSourcePromptValidate = driver.StructuredOutputSourcePromptValidate
)

// --- Session mode (was session_types.go) ----------------------------------

// SessionMode controls how the SDK coordinates a run with SessionStore. It is
// an alias for [driver.SessionMode].
type SessionMode = driver.SessionMode

const (
	SessionContinueOrStart = driver.SessionContinueOrStart
	SessionContinueOnly    = driver.SessionContinueOnly
	SessionStartNew        = driver.SessionStartNew
	SessionFork            = driver.SessionFork
	SessionStateless       = driver.SessionStateless
)

// --- Session codec (was session_codec.go) ---------------------------------

type (
	SessionParams = driver.SessionParams
	SessionCodec  = driver.SessionCodec
)

// --- Environment / models / profile probes (was workspace_skill_types.go) -

type (
	ModelInfo          = driver.ModelInfo
	DetectedModel      = driver.DetectedModel
	AgentProfileSource = driver.AgentProfileSource
	AgentProfile       = driver.AgentProfile
	EnvironmentStatus  = driver.EnvironmentStatus
	EnvironmentReport  = driver.EnvironmentReport
	EnvironmentCheck   = driver.EnvironmentCheck
	QuotaReport        = driver.QuotaReport
	QuotaWindow        = driver.QuotaWindow
)

const (
	AgentProfileSourceBindingEnv    = driver.AgentProfileSourceBindingEnv
	AgentProfileSourceProfileOption = driver.AgentProfileSourceProfileOption
	AgentProfileSourceProcessEnv    = driver.AgentProfileSourceProcessEnv
	AgentProfileSourceDefault       = driver.AgentProfileSourceDefault
	AgentProfileSourceManaged       = driver.AgentProfileSourceManaged
	AgentProfileSourceUnsupported   = driver.AgentProfileSourceUnsupported
)

const (
	EnvironmentPass = driver.EnvironmentPass
	EnvironmentWarn = driver.EnvironmentWarn
	EnvironmentFail = driver.EnvironmentFail
)

// --- Workspace lease / runtime services (was workspace_skill_types.go) ----

type (
	WorkspaceStrategyType   = driver.WorkspaceStrategyType
	WorkspaceMode           = driver.WorkspaceMode
	WorkspaceLease          = driver.WorkspaceLease
	RuntimeServiceSpec      = driver.RuntimeServiceSpec
	RuntimeServiceRef       = driver.RuntimeServiceRef
	RuntimePayload          = driver.RuntimePayload
	RuntimeServiceStatus    = driver.RuntimeServiceStatus
	RuntimeServiceLifecycle = driver.RuntimeServiceLifecycle
	RuntimeServiceHealth    = driver.RuntimeServiceHealth
	RuntimeServiceReport    = driver.RuntimeServiceReport
)

const (
	WorkspaceStrategyProjectPrimary = driver.WorkspaceStrategyProjectPrimary
	WorkspaceStrategyGitWorktree    = driver.WorkspaceStrategyGitWorktree
	WorkspaceStrategyAdapterManaged = driver.WorkspaceStrategyAdapterManaged
	WorkspaceStrategyCloudSandbox   = driver.WorkspaceStrategyCloudSandbox
)

const (
	WorkspaceModeShared       = driver.WorkspaceModeShared
	WorkspaceModeIsolated     = driver.WorkspaceModeIsolated
	WorkspaceModeOperator     = driver.WorkspaceModeOperator
	WorkspaceModeReuse        = driver.WorkspaceModeReuse
	WorkspaceModeAgentDefault = driver.WorkspaceModeAgentDefault
)

const (
	RuntimeServiceStarting = driver.RuntimeServiceStarting
	RuntimeServiceRunning  = driver.RuntimeServiceRunning
	RuntimeServiceStopped  = driver.RuntimeServiceStopped
	RuntimeServiceFailed   = driver.RuntimeServiceFailed
)

const (
	RuntimeLifecycleShared    = driver.RuntimeLifecycleShared
	RuntimeLifecycleEphemeral = driver.RuntimeLifecycleEphemeral
)

const (
	RuntimeHealthUnknown   = driver.RuntimeHealthUnknown
	RuntimeHealthHealthy   = driver.RuntimeHealthHealthy
	RuntimeHealthUnhealthy = driver.RuntimeHealthUnhealthy
)

// --- Env / instructions (was config_types.go) ------------------------------

type (
	EnvBinding            = driver.EnvBinding
	InstructionsBundleRef = driver.InstructionsBundleRef
	InstructionScope      = driver.InstructionScope
	InstructionMode       = driver.InstructionMode
)

const (
	InstructionScopeDefault = driver.InstructionScopeDefault
	InstructionScopeUser    = driver.InstructionScopeUser
	InstructionScopeProject = driver.InstructionScopeProject
	InstructionScopeLocal   = driver.InstructionScopeLocal
	InstructionScopeRun     = driver.InstructionScopeRun
)

const (
	InstructionModeAdditive = driver.InstructionModeAdditive
	InstructionModeReplace  = driver.InstructionModeReplace
)

// --- Skills (was skill_types.go) -------------------------------------------

type (
	Skill          = driver.Skill
	SkillSource    = driver.SkillSource
	SkillRef       = driver.SkillRef
	SkillKey       = driver.SkillKey
	ResolvedSkills = driver.ResolvedSkills
	ResolvedSkill  = driver.ResolvedSkill
	SkillSyncMode  = driver.SkillSyncMode
	SkillSnapshot  = driver.SkillSnapshot
	SnapshotEntry  = driver.SnapshotEntry
	SkillState     = driver.SkillState
	SkillOrigin    = driver.SkillOrigin
)

const (
	SkillMetadataRuntimeName = driver.SkillMetadataRuntimeName
	SkillMetadataDisplayName = driver.SkillMetadataDisplayName
)

const (
	SkillSyncUnsupported = driver.SkillSyncUnsupported
	SkillSyncEphemeral   = driver.SkillSyncEphemeral
	SkillSyncPersistent  = driver.SkillSyncPersistent
)

const (
	SkillStateAvailable  = driver.SkillStateAvailable
	SkillStateConfigured = driver.SkillStateConfigured
	SkillStateInstalled  = driver.SkillStateInstalled
	SkillStateMissing    = driver.SkillStateMissing
	SkillStateStale      = driver.SkillStateStale
	SkillStateExternal   = driver.SkillStateExternal
)

const (
	SkillOriginManaged  = driver.SkillOriginManaged
	SkillOriginRequired = driver.SkillOriginRequired
	SkillOriginUser     = driver.SkillOriginUser
	SkillOriginUnknown  = driver.SkillOriginUnknown
)

// --- MCP (was mcp_types.go) -------------------------------------------------

type (
	MCPTransport  = driver.MCPTransport
	MCPServerSpec = driver.MCPServerSpec
	MCPPayload    = driver.MCPPayload
	MCPCapability = driver.MCPCapability
)

const (
	MCPTransportStdio = driver.MCPTransportStdio
	MCPTransportHTTP  = driver.MCPTransportHTTP
	MCPTransportSSE   = driver.MCPTransportSSE
)

// --- HITL decisions (was decision_types.go) --------------------------------

type (
	HumanDecisionKind    = driver.HumanDecisionKind
	HumanDecisionMode    = driver.HumanDecisionMode
	QuestionMode         = driver.QuestionMode
	FailureAction        = driver.FailureAction
	FailureCode          = driver.FailureCode
	HumanDecisionPolicy  = driver.HumanDecisionPolicy
	HumanDecisionSupport = driver.HumanDecisionSupport
	QuestionSupport      = driver.QuestionSupport
	DecisionChoice       = driver.DecisionChoice
	DecisionResult       = driver.DecisionResult
	DecisionRequest      = driver.DecisionRequest
	DecisionResponse     = driver.DecisionResponse
	HumanDecisionFailure = driver.HumanDecisionFailure
	HITLRequestedPayload = driver.HITLRequestedPayload
	HITLResolvedPayload  = driver.HITLResolvedPayload
)

const (
	HumanDecisionPermission = driver.HumanDecisionPermission
	HumanDecisionPlanReview = driver.HumanDecisionPlanReview
	HumanDecisionQuestion   = driver.HumanDecisionQuestion
)

const (
	HumanDecisionUnset       = driver.HumanDecisionUnset
	HumanDecisionAsk         = driver.HumanDecisionAsk
	HumanDecisionAutoApprove = driver.HumanDecisionAutoApprove
	HumanDecisionAutoReject  = driver.HumanDecisionAutoReject
)

const (
	QuestionUnset      = driver.QuestionUnset
	QuestionAsk        = driver.QuestionAsk
	QuestionAutoReject = driver.QuestionAutoReject
)

const (
	FailureActionUnset = driver.FailureActionUnset
	FailureAbort       = driver.FailureAbort
	FailureContinue    = driver.FailureContinue
	FailureRetry       = driver.FailureRetry
)

const (
	FailureReject      = driver.FailureReject
	FailureTimeout     = driver.FailureTimeout
	FailureAgentError  = driver.FailureAgentError
	FailureCancelled   = driver.FailureCancelled
	FailurePolicyError = driver.FailurePolicyError
)

const (
	DecisionApproved = driver.DecisionApproved
	DecisionRejected = driver.DecisionRejected
	DecisionAnswered = driver.DecisionAnswered
	DecisionTimedOut = driver.DecisionTimedOut
	DecisionAborted  = driver.DecisionAborted
)

// --- Run policy (was run_policy.go) -----------------------------------------

type (
	RunPolicy      = driver.RunPolicy
	IsolationLevel = driver.IsolationLevel
	FeatureLevel   = driver.FeatureLevel
)

const (
	IsolationInherit        = driver.IsolationInherit
	IsolationReadOnly       = driver.IsolationReadOnly
	IsolationWorkspaceWrite = driver.IsolationWorkspaceWrite
	IsolationUnrestricted   = driver.IsolationUnrestricted
)

const (
	FeatureInherit = driver.FeatureInherit
	FeatureAllow   = driver.FeatureAllow
	FeatureDeny    = driver.FeatureDeny
)

const (
	// DefaultHumanDecisionTimeout is the timeout used for Ask decisions when
	// the host does not set HumanDecisionPolicy.Timeout.
	DefaultHumanDecisionTimeout = driver.DefaultHumanDecisionTimeout
	// DefaultHumanDecisionMaxRetries is the retry cap used when the host
	// requests FailureRetry without setting MaxRetries.
	DefaultHumanDecisionMaxRetries = driver.DefaultHumanDecisionMaxRetries
)

// --- Profile selection / resources (was profile.go / profile_resources.go) -

type (
	ProfileMode          = driver.ProfileMode
	CloneProfileOptions  = driver.CloneProfileOptions
	CloneProfileAuthMode = driver.CloneProfileAuthMode
	ProfileSelection     = driver.ProfileSelection
)

const (
	ProfileModeUnset     = driver.ProfileModeUnset
	ProfileModeNative    = driver.ProfileModeNative
	ProfileModeDedicated = driver.ProfileModeDedicated
	ProfileModeClone     = driver.ProfileModeClone
)

const (
	CloneProfileAuthNone = driver.CloneProfileAuthNone
	CloneProfileAuthCopy = driver.CloneProfileAuthCopy
	CloneProfileAuthLink = driver.CloneProfileAuthLink
)

type (
	ProfilePayload              = driver.ProfilePayload
	ProfileResourceDeclarations = driver.ProfileResourceDeclarations
	AgentSpec                   = driver.AgentSpec
	AgentToolPolicy             = driver.AgentToolPolicy
	AgentPayload                = driver.AgentPayload
	HookSpec                    = driver.HookSpec
	HookEvent                   = driver.HookEvent
	HookMatcher                 = driver.HookMatcher
	HookMatcherSubject          = driver.HookMatcherSubject
	HookMatcherSyntax           = driver.HookMatcherSyntax
	HookHandler                 = driver.HookHandler
	HookHandlerType             = driver.HookHandlerType
	HookFailPolicy              = driver.HookFailPolicy
	HookPayload                 = driver.HookPayload
	ProfileConfigFileKind       = driver.ProfileConfigFileKind
	ProfileConfigPatch          = driver.ProfileConfigPatch
	NativeConfigPatch           = driver.NativeConfigPatch
	ProfileConfigPayload        = driver.ProfileConfigPayload
)

const (
	HookEventSessionStart      = driver.HookEventSessionStart
	HookEventSessionEnd        = driver.HookEventSessionEnd
	HookEventPromptSubmit      = driver.HookEventPromptSubmit
	HookEventPromptExpand      = driver.HookEventPromptExpand
	HookEventPreTool           = driver.HookEventPreTool
	HookEventPostTool          = driver.HookEventPostTool
	HookEventToolFailure       = driver.HookEventToolFailure
	HookEventPermissionRequest = driver.HookEventPermissionRequest
	HookEventPreShell          = driver.HookEventPreShell
	HookEventPostShell         = driver.HookEventPostShell
	HookEventPreMCP            = driver.HookEventPreMCP
	HookEventPostMCP           = driver.HookEventPostMCP
	HookEventPreFileRead       = driver.HookEventPreFileRead
	HookEventPostFileEdit      = driver.HookEventPostFileEdit
	HookEventSubagentStart     = driver.HookEventSubagentStart
	HookEventSubagentStop      = driver.HookEventSubagentStop
	HookEventPreCompact        = driver.HookEventPreCompact
	HookEventPostCompact       = driver.HookEventPostCompact
	HookEventStop              = driver.HookEventStop
	HookEventStopFailure       = driver.HookEventStopFailure
)

const (
	HookMatcherSubjectDefault  = driver.HookMatcherSubjectDefault
	HookMatcherSubjectTool     = driver.HookMatcherSubjectTool
	HookMatcherSubjectCommand  = driver.HookMatcherSubjectCommand
	HookMatcherSubjectMCP      = driver.HookMatcherSubjectMCP
	HookMatcherSubjectPath     = driver.HookMatcherSubjectPath
	HookMatcherSubjectPrompt   = driver.HookMatcherSubjectPrompt
	HookMatcherSubjectSubagent = driver.HookMatcherSubjectSubagent
	HookMatcherSubjectSource   = driver.HookMatcherSubjectSource
)

const (
	HookMatcherSyntaxProvider = driver.HookMatcherSyntaxProvider
	HookMatcherSyntaxExact    = driver.HookMatcherSyntaxExact
	HookMatcherSyntaxRegex    = driver.HookMatcherSyntaxRegex
	HookMatcherSyntaxPrefix   = driver.HookMatcherSyntaxPrefix
	HookMatcherSyntaxContains = driver.HookMatcherSyntaxContains
)

const (
	HookHandlerCommand = driver.HookHandlerCommand
	HookHandlerPrompt  = driver.HookHandlerPrompt
	HookHandlerHTTP    = driver.HookHandlerHTTP
	HookHandlerMCPTool = driver.HookHandlerMCPTool
	HookHandlerAgent   = driver.HookHandlerAgent
)

const (
	HookFailPolicyProviderDefault = driver.HookFailPolicyProviderDefault
	HookFailPolicyOpen            = driver.HookFailPolicyOpen
	HookFailPolicyClosed          = driver.HookFailPolicyClosed
)

const (
	ProfileConfigFileJSON = driver.ProfileConfigFileJSON
	ProfileConfigFileTOML = driver.ProfileConfigFileTOML
)
