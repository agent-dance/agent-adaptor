// Package a2a contains thin client primitives for consuming remote A2A agents.
//
// The package intentionally exposes agent-adaptor-owned DTOs instead of
// re-exporting github.com/a2aproject/a2a-go/v2/a2a types. Upstream protocol
// types are used at the package edge for wire behavior, while callers get a
// stable narrow API that does not pretend remote A2A traffic has local CLI
// stdout/stderr semantics.
package a2a

import "time"

// TransportProtocol names an A2A transport binding.
type TransportProtocol string

const (
	TransportJSONRPC  TransportProtocol = "JSONRPC"
	TransportHTTPJSON TransportProtocol = "HTTP+JSON"
)

// AgentCard is the validated discovery document returned by AgentCard.
type AgentCard struct {
	Name                string
	Description         string
	URL                 string
	Version             string
	DocumentationURL    string
	IconURL             string
	Provider            *Provider
	Capabilities        Capabilities
	DefaultInputModes   []string
	DefaultOutputModes  []string
	Skills              []Skill
	SupportedInterfaces []AgentInterface
	Fingerprint         string
	Raw                 map[string]any
}

type Provider struct {
	Organization string
	URL          string
}

type Capabilities struct {
	Streaming         bool
	PushNotifications bool
	ExtendedAgentCard bool
	Extensions        []Extension
}

type Extension struct {
	URI         string
	Description string
	Required    bool
	Params      map[string]any
}

type AgentInterface struct {
	URL             string
	ProtocolBinding TransportProtocol
	Tenant          string
	ProtocolVersion string
}

type Skill struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
	InputModes  []string
	OutputModes []string
}

// Message is an A2A message projected into a stable local shape.
type Message struct {
	ID             string
	Role           string
	TaskID         string
	ContextID      string
	Parts          []Part
	ReferenceTasks []string
	Extensions     []string
	Metadata       map[string]any
	Raw            map[string]any
}

type Part struct {
	Kind      PartKind
	Text      string
	Raw       []byte
	Data      any
	URL       string
	MediaType string
	Filename  string
	Metadata  map[string]any
}

type PartKind string

const (
	PartText PartKind = "text"
	PartRaw  PartKind = "raw"
	PartData PartKind = "data"
	PartURL  PartKind = "url"
)

type Task struct {
	ID        string
	ContextID string
	Status    TaskStatus
	Messages  []Message
	Artifacts []Artifact
	Metadata  map[string]any
	Raw       map[string]any
}

type TaskStatus struct {
	State     TaskState
	Message   *Message
	Timestamp *time.Time
}

type TaskState string

const (
	TaskStateUnspecified   TaskState = ""
	TaskStateSubmitted     TaskState = "TASK_STATE_SUBMITTED"
	TaskStateWorking       TaskState = "TASK_STATE_WORKING"
	TaskStateCompleted     TaskState = "TASK_STATE_COMPLETED"
	TaskStateFailed        TaskState = "TASK_STATE_FAILED"
	TaskStateCanceled      TaskState = "TASK_STATE_CANCELED"
	TaskStateInputRequired TaskState = "TASK_STATE_INPUT_REQUIRED"
	TaskStateRejected      TaskState = "TASK_STATE_REJECTED"
	TaskStateAuthRequired  TaskState = "TASK_STATE_AUTH_REQUIRED"
)

func (s TaskState) Terminal() bool {
	return s == TaskStateCompleted || s == TaskStateFailed || s == TaskStateCanceled || s == TaskStateRejected
}

type Artifact struct {
	ID          string
	Name        string
	Description string
	Parts       []Part
	Extensions  []string
	Metadata    map[string]any
	Raw         map[string]any
}

// Event is one ordered A2A stream/subscription update.
type Event struct {
	Kind           EventKind
	Task           *Task
	Message        *Message
	Status         *TaskStatus
	Artifact       *Artifact
	TaskID         string
	ContextID      string
	Append         bool
	LastChunk      bool
	RecoveredState bool
	Raw            map[string]any
}

type EventKind string

const (
	EventTask     EventKind = "task"
	EventMessage  EventKind = "message"
	EventStatus   EventKind = "status"
	EventArtifact EventKind = "artifact"
	EventTerminal EventKind = "terminal"
)

type SendRequest struct {
	Message             Message
	ContextID           string
	TaskID              string
	Tenant              string
	AcceptedOutputModes []string
	ReturnImmediately   bool
	HistoryLength       *int
	Metadata            map[string]any
}

type SubscribeRequest struct {
	TaskID string
	Tenant string
	// Since is retained for host recovery cursors. A2A 1.0 SubscribeToTask
	// has no since parameter, so the value is not sent on the wire.
	Since string
}
