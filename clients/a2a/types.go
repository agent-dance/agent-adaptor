package a2a

import "time"

// TransportProtocol names an A2A transport binding.
type TransportProtocol string

const (
	// TransportJSONRPC selects the A2A JSON-RPC transport binding.
	TransportJSONRPC TransportProtocol = "JSONRPC"
	// TransportHTTPJSON selects the A2A HTTP+JSON transport binding.
	TransportHTTPJSON TransportProtocol = "HTTP+JSON"
)

// AgentCard is the validated discovery document returned by AgentCard.
type AgentCard struct {
	// Name is the agent's display name.
	Name string
	// Description explains the agent's purpose.
	Description string
	// URL is the preferred endpoint advertised by the agent.
	URL string
	// Version is the agent implementation version.
	Version string
	// DocumentationURL points to human-readable agent documentation.
	DocumentationURL string
	// IconURL points to the agent's display icon.
	IconURL string
	// Provider identifies the organization operating the agent, when declared.
	Provider *Provider
	// Capabilities describes optional protocol behavior.
	Capabilities Capabilities
	// DefaultInputModes lists accepted input media types.
	DefaultInputModes []string
	// DefaultOutputModes lists produced output media types.
	DefaultOutputModes []string
	// Skills describes the operations advertised by the agent.
	Skills []Skill
	// SupportedInterfaces lists usable protocol endpoints.
	SupportedInterfaces []AgentInterface
	// Fingerprint is a deterministic digest of the validated discovery document.
	Fingerprint string
	// Raw preserves the normalized discovery payload.
	Raw map[string]any
}

// Provider identifies the organization responsible for an A2A agent.
type Provider struct {
	// Organization is the provider's display name.
	Organization string
	// URL is the provider's public URL.
	URL string
}

// Capabilities describes optional behavior advertised by an A2A agent.
type Capabilities struct {
	// Streaming reports support for streaming message execution.
	Streaming bool
	// PushNotifications reports support for push notification configuration.
	PushNotifications bool
	// ExtendedAgentCard reports support for authenticated extended discovery.
	ExtendedAgentCard bool
	// Extensions lists additional advertised protocol features.
	Extensions []Extension
}

// Extension is one optional protocol feature declared by an agent.
type Extension struct {
	// URI uniquely identifies the extension contract.
	URI string
	// Description explains the extension's behavior.
	Description string
	// Required reports whether clients must understand the extension.
	Required bool
	// Params preserves extension-specific parameters.
	Params map[string]any
}

// AgentInterface describes one protocol endpoint advertised by an agent.
type AgentInterface struct {
	// URL is the absolute endpoint URL.
	URL string
	// ProtocolBinding identifies the endpoint's transport binding.
	ProtocolBinding TransportProtocol
	// Tenant is the endpoint's optional tenant selector.
	Tenant string
	// ProtocolVersion is the A2A protocol version implemented by the endpoint.
	ProtocolVersion string
}

// Skill describes one operation advertised in an AgentCard.
type Skill struct {
	// ID is the stable protocol identifier for the skill.
	ID string
	// Name is the skill's display name.
	Name string
	// Description explains the skill's behavior.
	Description string
	// Tags provide discovery keywords.
	Tags []string
	// Examples contain representative requests.
	Examples []string
	// InputModes lists accepted media types for the skill.
	InputModes []string
	// OutputModes lists media types the skill may produce.
	OutputModes []string
}

// Message is an A2A message projected into a stable local shape.
type Message struct {
	// ID is the message identifier assigned by its producer.
	ID string
	// Role identifies the message author role.
	Role string
	// TaskID associates the message with a task, when available.
	TaskID string
	// ContextID associates the message with a remote conversation context.
	ContextID string
	// Parts contains the ordered message content.
	Parts []Part
	// ReferenceTasks lists tasks referenced by this message.
	ReferenceTasks []string
	// Extensions lists extension URIs used by this message.
	Extensions []string
	// Metadata preserves application-defined message metadata.
	Metadata map[string]any
	// Raw preserves the normalized protocol message.
	Raw map[string]any
}

// Part is one typed content part in an A2A message or artifact.
type Part struct {
	// Kind identifies which content field is populated.
	Kind PartKind
	// Text contains text content.
	Text string
	// Raw contains inline binary content.
	Raw []byte
	// Data contains structured data content.
	Data any
	// URL identifies remotely hosted content.
	URL string
	// MediaType is the content MIME type.
	MediaType string
	// Filename is the suggested content filename.
	Filename string
	// Metadata preserves application-defined part metadata.
	Metadata map[string]any
}

// PartKind identifies the representation used by a Part.
type PartKind string

const (
	// PartText identifies UTF-8 text content.
	PartText PartKind = "text"
	// PartRaw identifies inline binary content.
	PartRaw PartKind = "raw"
	// PartData identifies structured data content.
	PartData PartKind = "data"
	// PartURL identifies content referenced by URL.
	PartURL PartKind = "url"
)

// Task is a normalized snapshot of one remote A2A task.
type Task struct {
	// ID is the remote task identifier.
	ID string
	// ContextID is the remote conversation context identifier.
	ContextID string
	// Status is the task's current execution state.
	Status TaskStatus
	// Messages contains task conversation history returned by the server.
	Messages []Message
	// Artifacts contains outputs produced by the task.
	Artifacts []Artifact
	// Metadata preserves application-defined task metadata.
	Metadata map[string]any
	// Raw preserves the normalized protocol task.
	Raw map[string]any
}

// TaskStatus describes the current state and optional status message of a task.
type TaskStatus struct {
	// State is the normalized task lifecycle state.
	State TaskState
	// Message provides status detail when supplied by the server.
	Message *Message
	// Timestamp is the server-reported status time.
	Timestamp *time.Time
}

// TaskState identifies an A2A task lifecycle state.
type TaskState string

const (
	// TaskStateUnspecified means no task state was reported.
	TaskStateUnspecified TaskState = ""
	// TaskStateSubmitted means the task has been accepted but not started.
	TaskStateSubmitted TaskState = "TASK_STATE_SUBMITTED"
	// TaskStateWorking means the task is executing.
	TaskStateWorking TaskState = "TASK_STATE_WORKING"
	// TaskStateCompleted means the task completed successfully.
	TaskStateCompleted TaskState = "TASK_STATE_COMPLETED"
	// TaskStateFailed means the task terminated with failure.
	TaskStateFailed TaskState = "TASK_STATE_FAILED"
	// TaskStateCanceled means the task was canceled.
	TaskStateCanceled TaskState = "TASK_STATE_CANCELED"
	// TaskStateInputRequired means the task is waiting for user input.
	TaskStateInputRequired TaskState = "TASK_STATE_INPUT_REQUIRED"
	// TaskStateRejected means the task was rejected.
	TaskStateRejected TaskState = "TASK_STATE_REJECTED"
	// TaskStateAuthRequired means the task is waiting for authentication.
	TaskStateAuthRequired TaskState = "TASK_STATE_AUTH_REQUIRED"
)

// Terminal reports whether s represents a final task state.
func (s TaskState) Terminal() bool {
	return s == TaskStateCompleted || s == TaskStateFailed || s == TaskStateCanceled || s == TaskStateRejected
}

// Artifact is one output produced by an A2A task.
type Artifact struct {
	// ID is the artifact identifier.
	ID string
	// Name is the artifact's display name.
	Name string
	// Description explains the artifact contents.
	Description string
	// Parts contains the ordered artifact content.
	Parts []Part
	// Extensions lists extension URIs used by the artifact.
	Extensions []string
	// Metadata preserves application-defined artifact metadata.
	Metadata map[string]any
	// Raw preserves the normalized protocol artifact.
	Raw map[string]any
}

// Event is one ordered A2A stream/subscription update.
type Event struct {
	// Kind identifies the event payload and lifecycle meaning.
	Kind EventKind
	// Task contains a full task snapshot for task events.
	Task *Task
	// Message contains a message event payload.
	Message *Message
	// Status contains a status update payload.
	Status *TaskStatus
	// Artifact contains an artifact update payload.
	Artifact *Artifact
	// TaskID identifies the affected task.
	TaskID string
	// ContextID identifies the affected conversation context.
	ContextID string
	// Append reports that artifact content appends to earlier content.
	Append bool
	// LastChunk reports that an artifact update is complete.
	LastChunk bool
	// RecoveredState reports that the terminal event was reconstructed with GetTask.
	RecoveredState bool
	// Raw preserves the normalized protocol event.
	Raw map[string]any
}

// EventKind identifies the normalized payload carried by an Event.
type EventKind string

const (
	// EventTask carries a full task snapshot.
	EventTask EventKind = "task"
	// EventMessage carries a standalone message.
	EventMessage EventKind = "message"
	// EventStatus carries a task status update.
	EventStatus EventKind = "status"
	// EventArtifact carries an artifact update.
	EventArtifact EventKind = "artifact"
	// EventTerminal carries the final recovered or observed task state.
	EventTerminal EventKind = "terminal"
)

// SendRequest configures one Send or SendStream operation.
type SendRequest struct {
	// Message is the message to send.
	Message Message
	// ContextID continues a remote conversation context when non-empty.
	ContextID string
	// TaskID associates the message with an existing task when non-empty.
	TaskID string
	// Tenant selects the remote tenant when supported.
	Tenant string
	// AcceptedOutputModes restricts acceptable response media types.
	AcceptedOutputModes []string
	// ReturnImmediately requests an immediate protocol response.
	ReturnImmediately bool
	// HistoryLength limits task history returned with the result.
	HistoryLength *int
	// Metadata contains application-defined request metadata.
	Metadata map[string]any
}

// SubscribeRequest identifies an existing task to observe.
type SubscribeRequest struct {
	// TaskID identifies the task to subscribe to.
	TaskID string
	// Tenant selects the remote tenant when supported.
	Tenant string
	// Since is rejected when set. A2A 1.0 SubscribeToTask has no cursor replay
	// field, and the client refuses to pretend host-side replay cursors are
	// supported by the remote protocol.
	Since string
}

// GetTaskRequest identifies a task snapshot to retrieve.
type GetTaskRequest struct {
	// TaskID identifies the task to retrieve.
	TaskID string
	// Tenant selects the remote tenant when supported.
	Tenant string
	// HistoryLength limits message history returned with the task.
	HistoryLength *int
}

// CancelTaskRequest identifies a task to cancel.
type CancelTaskRequest struct {
	// TaskID identifies the task to cancel.
	TaskID string
	// Tenant selects the remote tenant when supported.
	Tenant string
	// Metadata contains application-defined cancellation metadata.
	Metadata map[string]any
}
