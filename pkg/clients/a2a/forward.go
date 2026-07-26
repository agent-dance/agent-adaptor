// Package a2a forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/clients/a2a; this
// forwarding package will be removed in v1.0.0.
package a2a

import (
	newclienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

// Types.
type (
	AgentCard           = newclienta2a.AgentCard
	AgentInterface      = newclienta2a.AgentInterface
	Artifact            = newclienta2a.Artifact
	Auth                = newclienta2a.Auth
	CancelTaskRequest   = newclienta2a.CancelTaskRequest
	Capabilities        = newclienta2a.Capabilities
	Client              = newclienta2a.Client
	Event               = newclienta2a.Event
	EventKind           = newclienta2a.EventKind
	Extension           = newclienta2a.Extension
	GetTaskRequest      = newclienta2a.GetTaskRequest
	Message             = newclienta2a.Message
	Options             = newclienta2a.Options
	Part                = newclienta2a.Part
	PartKind            = newclienta2a.PartKind
	ProtocolError       = newclienta2a.ProtocolError
	Provider            = newclienta2a.Provider
	SendRequest         = newclienta2a.SendRequest
	Skill               = newclienta2a.Skill
	Stream              = newclienta2a.Stream
	StreamRecoveryError = newclienta2a.StreamRecoveryError
	SubscribeRequest    = newclienta2a.SubscribeRequest
	Task                = newclienta2a.Task
	TaskState           = newclienta2a.TaskState
	TaskStatus          = newclienta2a.TaskStatus
	TransportProtocol   = newclienta2a.TransportProtocol
)

// Constants.
const (
	EventTask     EventKind = newclienta2a.EventTask
	EventMessage  EventKind = newclienta2a.EventMessage
	EventStatus   EventKind = newclienta2a.EventStatus
	EventArtifact EventKind = newclienta2a.EventArtifact
	EventTerminal EventKind = newclienta2a.EventTerminal

	PartText PartKind = newclienta2a.PartText
	PartRaw  PartKind = newclienta2a.PartRaw
	PartData PartKind = newclienta2a.PartData
	PartURL  PartKind = newclienta2a.PartURL

	TaskStateUnspecified   TaskState = newclienta2a.TaskStateUnspecified
	TaskStateSubmitted     TaskState = newclienta2a.TaskStateSubmitted
	TaskStateWorking       TaskState = newclienta2a.TaskStateWorking
	TaskStateCompleted     TaskState = newclienta2a.TaskStateCompleted
	TaskStateFailed        TaskState = newclienta2a.TaskStateFailed
	TaskStateCanceled      TaskState = newclienta2a.TaskStateCanceled
	TaskStateInputRequired TaskState = newclienta2a.TaskStateInputRequired
	TaskStateRejected      TaskState = newclienta2a.TaskStateRejected
	TaskStateAuthRequired  TaskState = newclienta2a.TaskStateAuthRequired

	TransportJSONRPC  TransportProtocol = newclienta2a.TransportJSONRPC
	TransportHTTPJSON TransportProtocol = newclienta2a.TransportHTTPJSON
)

// Variables.
var (
	ErrInvalidAgentCard = newclienta2a.ErrInvalidAgentCard
	ErrProtocol         = newclienta2a.ErrProtocol
	ErrUnauthorized     = newclienta2a.ErrUnauthorized
	ErrNotFound         = newclienta2a.ErrNotFound
	ErrUnsupported      = newclienta2a.ErrUnsupported
	ErrUntrustedOrigin  = newclienta2a.ErrUntrustedOrigin
)

// Functions.
var (
	BearerToken        = newclienta2a.BearerToken
	BearerTokenFromEnv = newclienta2a.BearerTokenFromEnv
	New                = newclienta2a.New
)
