// Package a2a forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/bridges/a2a; this
// forwarding package will be removed in v1.0.0.
package a2a

import (
	newa2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
)

// Types.
type (
	AdapterStreamEnvelopeV1       = newa2a.AdapterStreamEnvelopeV1
	AdapterStreamEventV1          = newa2a.AdapterStreamEventV1
	AgentCard                     = newa2a.AgentCard
	AgentInterface                = newa2a.AgentInterface
	ArtifactSpec                  = newa2a.ArtifactSpec
	BuiltResult                   = newa2a.BuiltResult
	Capabilities                  = newa2a.Capabilities
	CapabilityMode                = newa2a.CapabilityMode
	DiagnosticsPolicy             = newa2a.DiagnosticsPolicy
	EphemeralTaskStoreOptions     = newa2a.EphemeralTaskStoreOptions
	ExposurePolicy                = newa2a.ExposurePolicy
	ExtendedAgentCardProvider     = newa2a.ExtendedAgentCardProvider
	ExtendedAgentCardProviderFunc = newa2a.ExtendedAgentCardProviderFunc
	ExtendedAgentCardRequest      = newa2a.ExtendedAgentCardRequest
	ExtendedAgentCardSupport      = newa2a.ExtendedAgentCardSupport
	Extension                     = newa2a.Extension
	InboundRequest                = newa2a.InboundRequest
	Message                       = newa2a.Message
	Part                          = newa2a.Part
	PartKind                      = newa2a.PartKind
	PromptBuilder                 = newa2a.PromptBuilder
	PromptBuilderFunc             = newa2a.PromptBuilderFunc
	Provider                      = newa2a.Provider
	PushNotificationSupport       = newa2a.PushNotificationSupport
	ResultBuilder                 = newa2a.ResultBuilder
	ResultBuilderFunc             = newa2a.ResultBuilderFunc
	RunStreamingMode              = newa2a.RunStreamingMode
	SecurityRequirement           = newa2a.SecurityRequirement
	SecurityScheme                = newa2a.SecurityScheme
	SecuritySchemeType            = newa2a.SecuritySchemeType
	Server                        = newa2a.Server
	ServerOptions                 = newa2a.ServerOptions
	SessionMapper                 = newa2a.SessionMapper
	SessionMapperFunc             = newa2a.SessionMapperFunc
	Skill                         = newa2a.Skill
	TaskLifecycleOptions          = newa2a.TaskLifecycleOptions
)

// Constants.
const (
	AdapterStreamSchemaV1     = newa2a.AdapterStreamSchemaV1
	AdapterStreamExtensionURI = newa2a.AdapterStreamExtensionURI

	DefaultEphemeralTaskLimit = newa2a.DefaultEphemeralTaskLimit
	DefaultEphemeralTaskTTL   = newa2a.DefaultEphemeralTaskTTL

	ArtifactAgentAdaptorResult = newa2a.ArtifactAgentAdaptorResult

	CapabilityDefault  CapabilityMode = newa2a.CapabilityDefault
	CapabilityEnabled  CapabilityMode = newa2a.CapabilityEnabled
	CapabilityDisabled CapabilityMode = newa2a.CapabilityDisabled

	PartText PartKind = newa2a.PartText
	PartRaw  PartKind = newa2a.PartRaw
	PartData PartKind = newa2a.PartData
	PartURL  PartKind = newa2a.PartURL

	RunStreamingDefault  RunStreamingMode = newa2a.RunStreamingDefault
	RunStreamingEnabled  RunStreamingMode = newa2a.RunStreamingEnabled
	RunStreamingDisabled RunStreamingMode = newa2a.RunStreamingDisabled

	SecurityHTTP      SecuritySchemeType = newa2a.SecurityHTTP
	SecurityAPIKey    SecuritySchemeType = newa2a.SecurityAPIKey
	SecurityMutualTLS SecuritySchemeType = newa2a.SecurityMutualTLS
)

// Functions.
var (
	DecodeAdapterStreamStatus = newa2a.DecodeAdapterStreamStatus
	NewServer                 = newa2a.NewServer
	SessionByContextID        = newa2a.SessionByContextID
	SessionByTaskID           = newa2a.SessionByTaskID
	Stateless                 = newa2a.Stateless
)
