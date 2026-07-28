package a2a

import (
	"context"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

// AgentCard describes the public A2A identity and capabilities exposed by a
// [Server]. Name and Version are required. URL is required unless Interfaces
// contains at least one entry.
type AgentCard struct {
	// Name is the human-readable agent name.
	Name string
	// Description explains the agent's purpose and supported use cases.
	Description string
	// URL is the default JSON-RPC endpoint and the fallback URL for interface
	// entries whose URL is empty.
	URL string
	// Version identifies the implementation version advertised by the agent.
	Version string
	// DocumentationURL points callers to human-readable agent documentation.
	DocumentationURL string
	// IconURL points callers to an icon representing the agent.
	IconURL string
	// Provider identifies the organization operating the agent, when known.
	Provider *Provider
	// Capabilities declares optional protocol behavior and extensions.
	Capabilities Capabilities
	// DefaultInputModes lists accepted media types. An empty slice defaults to
	// text/plain.
	DefaultInputModes []string
	// DefaultOutputModes lists produced media types. An empty slice defaults to
	// text/plain.
	DefaultOutputModes []string
	// Skills advertises the agent's supported tasks.
	Skills []Skill
	// Interfaces lists supported protocol endpoints. When empty, URL is exposed
	// as a single JSON-RPC interface.
	Interfaces []AgentInterface
	// SecuritySchemes declares named authentication schemes referenced by
	// Security.
	SecuritySchemes []SecurityScheme
	// Security lists alternative authentication requirements. Schemes within
	// one requirement are all required; separate entries are alternatives.
	Security []SecurityRequirement
}

// Provider identifies the organization responsible for an A2A agent.
type Provider struct {
	// Organization is the provider's display name.
	Organization string
	// URL is the provider's website or information endpoint.
	URL string
}

// CapabilityMode controls a boolean A2A capability while preserving a
// field-specific default.
type CapabilityMode uint8

const (
	// CapabilityDefault applies the bridge default for the capability.
	CapabilityDefault CapabilityMode = iota
	// CapabilityEnabled explicitly enables the capability.
	CapabilityEnabled
	// CapabilityDisabled explicitly disables the capability.
	CapabilityDisabled
)

// Capabilities declares the optional A2A features advertised by an agent.
// Streaming defaults to enabled; the other capabilities default to disabled.
type Capabilities struct {
	// Streaming controls SendStreamingMessage support.
	Streaming CapabilityMode
	// PushNotifications advertises push-notification support. Enabling it
	// requires matching [PushNotificationSupport] in [ServerOptions].
	PushNotifications bool
	// ExtendedAgentCard advertises the authenticated extended-card endpoint.
	// Enabling it requires matching [ExtendedAgentCardSupport] in
	// [ServerOptions].
	ExtendedAgentCard bool
	// Extensions declares additional protocol extensions. NewServer always
	// appends the bridge's adapter.stream.v1 extension.
	Extensions []Extension
}

// Extension describes one A2A Agent Card extension.
type Extension struct {
	// URI is the globally unique extension identifier.
	URI string
	// Description explains the extension to clients.
	Description string
	// Required reports whether clients must understand the extension to
	// interact with the agent.
	Required bool
	// Params contains extension-specific, JSON-compatible parameters.
	Params map[string]any
}

// AgentInterface describes one protocol endpoint advertised by the Agent Card.
type AgentInterface struct {
	// URL is the endpoint for this interface. An empty value falls back to
	// AgentCard.URL.
	URL string
	// ProtocolBinding is the A2A transport protocol identifier. An empty value
	// defaults to JSON-RPC.
	ProtocolBinding string
	// Tenant identifies a tenant-specific endpoint, when applicable.
	Tenant string
	// ProtocolVersion overrides the A2A protocol version for this interface.
	ProtocolVersion string
}

// Skill describes a task the agent advertises through its Agent Card. ID is
// required.
type Skill struct {
	// ID is the stable, machine-readable skill identifier.
	ID string
	// Name is the human-readable skill name.
	Name string
	// Description explains what the skill does.
	Description string
	// Tags are search and discovery labels.
	Tags []string
	// Examples contains representative prompts or use cases.
	Examples []string
	// InputModes overrides the Agent Card's default input modes for this skill.
	InputModes []string
	// OutputModes overrides the Agent Card's default output modes for this
	// skill.
	OutputModes []string
}

// SecurityScheme declares one named authentication scheme on an Agent Card.
// Fields that do not apply to Type are ignored.
type SecurityScheme struct {
	// Name is the identifier referenced by [SecurityRequirement]. Empty names
	// are omitted.
	Name string
	// Type selects the authentication scheme shape. Its zero value selects
	// SecurityHTTP.
	Type SecuritySchemeType
	// Description explains the authentication requirement to callers.
	Description string
	// Scheme is the HTTP authentication scheme. It defaults to Bearer for
	// SecurityHTTP.
	Scheme string
	// BearerFormat documents the bearer token format for SecurityHTTP.
	BearerFormat string
	// In is the API-key location for SecurityAPIKey. It defaults to header.
	In string
	// ParamName is the header, query, or cookie parameter name for
	// SecurityAPIKey.
	ParamName string
}

// SecuritySchemeType identifies an Agent Card authentication scheme shape.
type SecuritySchemeType string

const (
	// SecurityHTTP selects HTTP authentication, such as Bearer or Basic.
	SecurityHTTP SecuritySchemeType = "http"
	// SecurityAPIKey selects an API key carried in a header, query parameter,
	// or cookie.
	SecurityAPIKey SecuritySchemeType = "apiKey"
	// SecurityMutualTLS selects mutual TLS authentication.
	SecurityMutualTLS SecuritySchemeType = "mutualTLS"
)

// SecurityRequirement describes one alternative authentication requirement.
type SecurityRequirement struct {
	// Schemes maps scheme names to required authorization scopes. Every scheme
	// in the map is required for this alternative.
	Schemes map[string][]string
}

const (
	// DefaultEphemeralTaskLimit is the maximum number of tasks retained by the
	// default in-memory task store.
	DefaultEphemeralTaskLimit = 256
	// DefaultEphemeralTaskTTL is the retention period used by the default
	// in-memory task store.
	DefaultEphemeralTaskTTL = time.Hour
)

const (
	// ArtifactAgentAdaptorResult is the bridge-owned A2A artifact name used
	// for the terminal structured SDK result summary and opt-in diagnostics.
	ArtifactAgentAdaptorResult = "agent-adaptor-result"
)

// EphemeralTaskStoreOptions configures the bounded in-memory task store used
// when [TaskLifecycleOptions.Store] is nil. Zero fields use the package
// defaults; negative values are invalid.
type EphemeralTaskStoreOptions struct {
	// MaxTasks is the maximum retained task count. Oldest tasks are evicted
	// first when the limit is exceeded.
	MaxTasks int
	// TTL is the maximum time since the latest update before a task expires.
	TTL time.Duration
}

// TaskLifecycleOptions selects how an A2A server persists protocol tasks.
type TaskLifecycleOptions struct {
	// Store is a host-provided task store. When non-nil, it takes precedence
	// over Ephemeral.
	Store taskstore.Store
	// Ephemeral configures the default in-memory store used when Store is nil.
	// A nil value uses [DefaultEphemeralTaskLimit] and
	// [DefaultEphemeralTaskTTL].
	Ephemeral *EphemeralTaskStoreOptions
}

// PushNotificationSupport supplies the persistence and delivery components
// required when an Agent Card enables push notifications.
type PushNotificationSupport struct {
	// Store persists caller push-notification configurations.
	Store push.ConfigStore
	// Sender delivers task updates to configured endpoints.
	Sender push.Sender
}

// ExtendedAgentCardRequest contains caller context passed to a dynamic
// extended-card provider.
type ExtendedAgentCardRequest struct {
	// Tenant is the tenant identifier from the A2A request, when present.
	Tenant string
}

// ExtendedAgentCardProvider builds an authenticated extended Agent Card.
type ExtendedAgentCardProvider interface {
	// ExtendedCard returns the Agent Card visible to the request's tenant.
	ExtendedCard(ctx context.Context, req ExtendedAgentCardRequest) (AgentCard, error)
}

// ExtendedAgentCardProviderFunc adapts a function to
// [ExtendedAgentCardProvider].
type ExtendedAgentCardProviderFunc func(context.Context, ExtendedAgentCardRequest) (AgentCard, error)

// ExtendedCard calls fn with the request context and tenant.
func (fn ExtendedAgentCardProviderFunc) ExtendedCard(ctx context.Context, req ExtendedAgentCardRequest) (AgentCard, error) {
	return fn(ctx, req)
}

// ExtendedAgentCardSupport configures either a static or dynamically generated
// extended Agent Card. Exactly one of Static and Provider must be set.
type ExtendedAgentCardSupport struct {
	// Static is the same extended card for every authenticated caller.
	Static *AgentCard
	// Provider builds an extended card for each request.
	Provider ExtendedAgentCardProvider
}

// ExposurePolicy controls which non-user-facing bridge artifacts are exposed
// to remote A2A callers.
//
// The zero value is intentionally conservative:
//   - assistant-facing Result.Text still flows through the task status message
//   - terminal summary still flows through the agent-adaptor-result artifact
//   - reasoning, tool-call internals, HITL events, and diagnostics stay hidden
type ExposurePolicy struct {
	// IncludeReasoning exposes thinking events in streaming status updates.
	IncludeReasoning bool
	// IncludeToolCalls exposes tool-call and tool-result events in streaming
	// status updates.
	IncludeToolCalls bool
	// IncludeHITL exposes approval lifecycle events. Approval payloads remain
	// gated by Diagnostics.
	IncludeHITL bool

	// Diagnostics controls additional, sanitized diagnostic fields.
	Diagnostics DiagnosticsPolicy
}

// DiagnosticsPolicy controls opt-in exposure of internal execution details.
//
// All enabled fields are sanitized before they leave the bridge.
type DiagnosticsPolicy struct {
	// IncludeMetadata exposes Result metadata, RunError details, and provider
	// source coordinates on streamed events.
	IncludeMetadata bool
	// IncludeUsage exposes observed token usage. Unobserved usage remains
	// omitted; an observed zero value is preserved.
	IncludeUsage bool
	// IncludeProviderResult exposes the provider's validated terminal payload.
	IncludeProviderResult bool
	// IncludeTranscript exposes normalized transcript entries.
	IncludeTranscript bool
	// IncludeRawStreams exposes captured process stdout and stderr.
	IncludeRawStreams bool
	// IncludeHITLPayloads exposes structured approval request details and
	// choices.
	IncludeHITLPayloads bool
	// IncludeHITLRaw exposes raw approval data carried by approval notices.
	IncludeHITLRaw bool
}

// InboundRequest is the bridge-owned view of one A2A execution request passed
// to a [PromptBuilder] or [ResultBuilder].
type InboundRequest struct {
	// TaskID is the A2A task identifier.
	TaskID string
	// ContextID is the A2A conversation context identifier.
	ContextID string
	// Message is the submitted user message.
	Message Message
	// Metadata contains request-level A2A metadata.
	Metadata map[string]any
}

// Message is the stable, protocol-local projection of an A2A message.
type Message struct {
	// ID is the message identifier.
	ID string
	// Role is the A2A message role value.
	Role string
	// TaskID is the task associated with the message.
	TaskID string
	// ContextID is the conversation context associated with the message.
	ContextID string
	// Parts contains the message content in source order.
	Parts []Part
	// ReferenceTasks contains identifiers of tasks referenced by the message.
	ReferenceTasks []string
	// Extensions contains extension URIs used by the message.
	Extensions []string
	// Metadata contains message-level A2A metadata.
	Metadata map[string]any
}

// Part is the stable, protocol-local projection of one A2A content part. The
// field corresponding to Kind carries the payload; MediaType, Filename, and
// Metadata provide optional part attributes.
type Part struct {
	// Kind identifies which payload field is active.
	Kind PartKind
	// Text contains a PartText payload.
	Text string
	// Raw contains a PartRaw payload.
	Raw []byte
	// Data contains a JSON-compatible PartData payload.
	Data any
	// URL contains a PartURL payload.
	URL string
	// MediaType identifies the payload's media type.
	MediaType string
	// Filename is the suggested file name for raw or URL payloads.
	Filename string
	// Metadata contains part-level A2A metadata.
	Metadata map[string]any
}

// PartKind identifies the payload representation of a [Part].
type PartKind string

const (
	// PartText identifies a UTF-8 text payload.
	PartText PartKind = "text"
	// PartRaw identifies an inline byte payload.
	PartRaw PartKind = "raw"
	// PartData identifies a structured JSON-compatible payload.
	PartData PartKind = "data"
	// PartURL identifies a remotely hosted payload.
	PartURL PartKind = "url"
)

// BuiltResult is the terminal A2A projection returned by ResultBuilder.
//
// When ReplaceDefaultArtifacts is false, Artifacts are appended after the
// bridge-owned terminal defaults (`agent-adaptor-result`). When true, only the
// custom terminal Artifacts are emitted. Streamed artifacts may already have
// been emitted before ResultBuilder runs and are not affected by this setting.
//
// StatusText, when non-nil, overrides the final completed status message text.
// A nil value preserves the bridge's default Result.Text behavior.
type BuiltResult struct {
	// StatusText overrides the final completed status message. Nil preserves
	// Result.Text; a pointer to an empty string deliberately emits empty text.
	StatusText *string
	// ReplaceDefaultArtifacts suppresses the bridge-owned terminal artifact.
	ReplaceDefaultArtifacts bool
	// Artifacts contains custom terminal artifacts in emission order.
	Artifacts []ArtifactSpec
}

// ArtifactSpec describes one terminal A2A artifact emitted by ResultBuilder.
// It reuses the bridge's stable Part projection so hosts can define
// TextPart/DataPart/URLPart payloads without depending on upstream proto types.
type ArtifactSpec struct {
	// ID is the artifact identifier. When empty, Name is used; at least one of
	// ID and Name must be non-empty.
	ID string
	// Name is the artifact's machine-readable name.
	Name string
	// Description explains the artifact to callers.
	Description string
	// Parts contains the artifact content in source order.
	Parts []Part
	// Extensions contains extension URIs used by the artifact.
	Extensions []string
	// Metadata contains artifact-level A2A metadata.
	Metadata map[string]any
}
