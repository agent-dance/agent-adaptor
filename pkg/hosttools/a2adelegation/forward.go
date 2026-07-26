// Package a2adelegation forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/hosttools/a2adelegation;
// this forwarding package will be removed in v1.0.0.
package a2adelegation

import (
	newa2adelegation "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

// Types.
type (
	A2AClient                    = newa2adelegation.A2AClient
	A2AStream                    = newa2adelegation.A2AStream
	AfterDelegation              = newa2adelegation.AfterDelegation
	BeforeDelegation             = newa2adelegation.BeforeDelegation
	ClientFactory                = newa2adelegation.ClientFactory
	DelegationArtifact           = newa2adelegation.DelegationArtifact
	DelegationError              = newa2adelegation.DelegationError
	DelegationEvent              = newa2adelegation.DelegationEvent
	DelegationEventKind          = newa2adelegation.DelegationEventKind
	DelegationLifecycleHook      = newa2adelegation.DelegationLifecycleHook
	DelegationLifecycleHookFuncs = newa2adelegation.DelegationLifecycleHookFuncs
	DelegationMessage            = newa2adelegation.DelegationMessage
	DelegationPolicy             = newa2adelegation.DelegationPolicy
	DelegationRequest            = newa2adelegation.DelegationRequest
	DelegationResult             = newa2adelegation.DelegationResult
	DelegationStageContext       = newa2adelegation.DelegationStageContext
	Delegator                    = newa2adelegation.Delegator
	DelegatorOption              = newa2adelegation.DelegatorOption
	EventBus                     = newa2adelegation.EventBus
	InputArtifact                = newa2adelegation.InputArtifact
	MCPServer                    = newa2adelegation.MCPServer
	MCPServerOptions             = newa2adelegation.MCPServerOptions
	Registry                     = newa2adelegation.Registry
	RemoteAgentSpec              = newa2adelegation.RemoteAgentSpec
	RemoteArtifact               = newa2adelegation.RemoteArtifact
	RemotePart                   = newa2adelegation.RemotePart
	StatusPartDecoder            = newa2adelegation.StatusPartDecoder
	ToolConstraints              = newa2adelegation.ToolConstraints
	ToolContext                  = newa2adelegation.ToolContext
	ToolInput                    = newa2adelegation.ToolInput
	ToolInputBody                = newa2adelegation.ToolInputBody
	ToolSpec                     = newa2adelegation.ToolSpec
)

// Constants.
const (
	DelegateToolName = newa2adelegation.DelegateToolName
	ProtocolA2A      = newa2adelegation.ProtocolA2A

	DelegationStarted         DelegationEventKind = newa2adelegation.DelegationStarted
	DelegationStatus          DelegationEventKind = newa2adelegation.DelegationStatus
	DelegationTextStart       DelegationEventKind = newa2adelegation.DelegationTextStart
	DelegationTextDelta       DelegationEventKind = newa2adelegation.DelegationTextDelta
	DelegationTextEnd         DelegationEventKind = newa2adelegation.DelegationTextEnd
	DelegationReasoningStart  DelegationEventKind = newa2adelegation.DelegationReasoningStart
	DelegationReasoningDelta  DelegationEventKind = newa2adelegation.DelegationReasoningDelta
	DelegationReasoningEnd    DelegationEventKind = newa2adelegation.DelegationReasoningEnd
	DelegationToolCallStart   DelegationEventKind = newa2adelegation.DelegationToolCallStart
	DelegationToolCallArgs    DelegationEventKind = newa2adelegation.DelegationToolCallArgs
	DelegationToolCallResult  DelegationEventKind = newa2adelegation.DelegationToolCallResult
	DelegationToolCallEnd     DelegationEventKind = newa2adelegation.DelegationToolCallEnd
	DelegationArtifactCreated DelegationEventKind = newa2adelegation.DelegationArtifactCreated
	DelegationCustom          DelegationEventKind = newa2adelegation.DelegationCustom
	DelegationStreamDropped   DelegationEventKind = newa2adelegation.DelegationStreamDropped
	DelegationInputRequired   DelegationEventKind = newa2adelegation.DelegationInputRequired
	DelegationFinished        DelegationEventKind = newa2adelegation.DelegationFinished
	DelegationFailed          DelegationEventKind = newa2adelegation.DelegationFailed
	DelegationCancelled       DelegationEventKind = newa2adelegation.DelegationCancelled
)

// Functions.
var (
	ToolSchema            = newa2adelegation.ToolSchema
	NewDelegator          = newa2adelegation.NewDelegator
	WithStatusPartDecoder = newa2adelegation.WithStatusPartDecoder
	NewEventBus           = newa2adelegation.NewEventBus
	NewMCPServer          = newa2adelegation.NewMCPServer
	NewRegistry           = newa2adelegation.NewRegistry
	ParseToolInput        = newa2adelegation.ParseToolInput
)
