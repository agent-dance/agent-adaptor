// Package a2adelegation provides host-owned Local and Remote delegation for an
// adaptor Agent.
//
// A Service owns a curated target registry, an authenticated per-run MCP
// sidecar, ordered DelegationEvent publication, and final result recording.
// Service.Option or adaptor.WithRunServices attaches that lifecycle to a
// leader's ordinary Run/Stream pipeline, where delegation progress appears as
// adaptor.SubagentUpdate on the existing Event channel.
//
// Local targets consume adaptor.Runner directly. Remote targets use
// clients/a2a. Both are normalized through the same A2A event mapper. The
// adapter.stream.v1 spelling used by the mapper is an intentional versioned
// wire schema, not a temporary Go API name.
//
// This package stays above core: it never dispatches a Driver directly, never
// introduces a second execution stream, and leaves network exposure, auth,
// tenant policy, durable storage, and target selection to the host.
package a2adelegation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

// ProtocolA2A identifies the A2A transport used by remote and local-loopback
// delegation results and events.
const ProtocolA2A = "a2a"

// DelegateToolName is the default MCP tool exposed by each Service sidecar.
const DelegateToolName = "delegate_to_agent"

// DelegationEventKind classifies one event in a delegated task lifecycle.
type DelegationEventKind string

const (
	// DelegationStarted reports that the target accepted the delegated task.
	DelegationStarted DelegationEventKind = "subagent.started"
	// DelegationStatus carries an A2A task status update.
	DelegationStatus DelegationEventKind = "subagent.status"
	// DelegationTextStart starts one delegated assistant text item.
	DelegationTextStart DelegationEventKind = "subagent.text.start"
	// DelegationTextDelta carries incremental delegated assistant text.
	DelegationTextDelta DelegationEventKind = "subagent.text.delta"
	// DelegationTextEnd closes one delegated assistant text item.
	DelegationTextEnd DelegationEventKind = "subagent.text.end"
	// DelegationReasoningStart starts one delegated reasoning item.
	DelegationReasoningStart DelegationEventKind = "subagent.reasoning.start"
	// DelegationReasoningDelta carries incremental delegated reasoning.
	DelegationReasoningDelta DelegationEventKind = "subagent.reasoning.delta"
	// DelegationReasoningEnd closes one delegated reasoning item.
	DelegationReasoningEnd DelegationEventKind = "subagent.reasoning.end"
	// DelegationToolCallStart starts one delegated tool call.
	DelegationToolCallStart DelegationEventKind = "subagent.tool_call.start"
	// DelegationToolCallArgs carries delegated tool-call arguments.
	DelegationToolCallArgs DelegationEventKind = "subagent.tool_call.args"
	// DelegationToolCallResult carries a delegated tool result.
	DelegationToolCallResult DelegationEventKind = "subagent.tool_call.result"
	// DelegationToolCallEnd closes one delegated tool call.
	DelegationToolCallEnd DelegationEventKind = "subagent.tool_call.end"
	// DelegationArtifactCreated reports an artifact from the delegated task.
	DelegationArtifactCreated DelegationEventKind = "subagent.artifact"
	// DelegationCustom carries a host-decoded custom status event.
	DelegationCustom DelegationEventKind = "subagent.custom"
	// DelegationStreamDropped summarizes delegated events lost to a sequence
	// gap, unsupported schema, or local EventBus backpressure.
	DelegationStreamDropped DelegationEventKind = "subagent.stream.dropped"
	// DelegationInputRequired reports that the remote task needs more input.
	DelegationInputRequired DelegationEventKind = "subagent.input_required"
	// DelegationFinished reports successful delegated-task completion.
	DelegationFinished DelegationEventKind = "subagent.finished"
	// DelegationFailed reports delegated-task failure.
	DelegationFailed DelegationEventKind = "subagent.failed"
	// DelegationCancelled reports delegated-task cancellation.
	DelegationCancelled DelegationEventKind = "subagent.cancelled"
)

// RemoteAgentSpec is one host-curated remote A2A target. It carries discovery,
// auth, transport, tenant, output-mode, and delegation-policy configuration;
// model-facing tool input names only Key and cannot replace these values.
type RemoteAgentSpec struct {
	Key                 string
	DisplayName         string
	Protocol            string
	AgentCardURL        string
	AgentCard           *clienta2a.AgentCard
	Tenant              string
	Auth                clienta2a.Auth
	HTTPClient          *http.Client
	TrustedAuthOrigins  []string
	AcceptedOutputModes []string
	PreferredTransports []clienta2a.TransportProtocol
	Policy              DelegationPolicy
}

// DelegationPolicy bounds remote execution and controls transport behavior.
// Zero timeout, polling, count, and artifact values select Delegator defaults.
type DelegationPolicy struct {
	MaxTimeout         time.Duration
	AllowInputRequired bool
	RequireStreaming   bool
	PollInterval       time.Duration
	MaxPolls           int
	MaxArtifactBytes   int64
}

// Registry stores host-curated remote target specifications by stable key.
// Registry returns defensive copies and is safe for read-only concurrent use
// after construction; hosts should complete registration before delegation.
type Registry struct {
	agents map[string]RemoteAgentSpec
}

// NewRegistry constructs a Registry and validates each supplied target.
func NewRegistry(specs ...RemoteAgentSpec) (*Registry, error) {
	r := &Registry{agents: map[string]RemoteAgentSpec{}}
	for _, spec := range specs {
		if err := r.Register(spec); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Register validates and adds one target. Duplicate keys are rejected.
func (r *Registry) Register(spec RemoteAgentSpec) error {
	if r.agents == nil {
		r.agents = map[string]RemoteAgentSpec{}
	}
	key := strings.TrimSpace(spec.Key)
	if key == "" {
		return &DelegationError{Code: "invalid_agent", Message: "remote agent key is required"}
	}
	if _, exists := r.agents[key]; exists {
		return &DelegationError{Code: "duplicate_agent", Message: fmt.Sprintf("remote agent %q already registered", key)}
	}
	spec.Key = key
	if spec.Protocol == "" {
		spec.Protocol = ProtocolA2A
	}
	if spec.Protocol != ProtocolA2A {
		return &DelegationError{Code: "unsupported_protocol", Message: fmt.Sprintf("remote agent %q uses unsupported protocol %q", key, spec.Protocol)}
	}
	if strings.TrimSpace(spec.AgentCardURL) == "" && spec.AgentCard == nil {
		return &DelegationError{Code: "invalid_agent", Message: fmt.Sprintf("remote agent %q requires a host-curated AgentCardURL or AgentCard", key)}
	}
	r.agents[key] = cloneRemoteAgentSpec(spec)
	return nil
}

// Lookup returns a defensive copy of the target registered under key.
func (r *Registry) Lookup(key string) (RemoteAgentSpec, bool) {
	if r == nil || len(r.agents) == 0 {
		return RemoteAgentSpec{}, false
	}
	spec, ok := r.agents[strings.TrimSpace(key)]
	return cloneRemoteAgentSpec(spec), ok
}

// Keys returns the registered target keys. Callers that need deterministic
// presentation order should sort the result.
func (r *Registry) Keys() []string {
	if r == nil || len(r.agents) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.agents))
	for key := range r.agents {
		keys = append(keys, key)
	}
	return keys
}

// DelegationRequest describes one host-authorized delegation. RunID attributes
// events to the leader run; Agent selects a Registry key. Message, when set,
// takes precedence over Prompt/Objective/Context and Artifacts.
type DelegationRequest struct {
	RunID                  string
	ParentToolCallID       string
	ContextID              string
	Agent                  string
	Objective              string
	Prompt                 string
	Context                string
	Message                *clienta2a.Message
	Artifacts              []InputArtifact
	IncludeRemoteArtifacts bool
	MaxArtifacts           *int
	HistoryLength          *int
	Timeout                time.Duration
	Stream                 bool
	Tenant                 string
	Metadata               map[string]any
	StageContext           DelegationStageContext
}

// InputArtifact is a model-facing reference supplied to a delegated task.
type InputArtifact struct {
	Name      string `json:"name,omitempty"`
	URI       string `json:"uri,omitempty"`
	MediaType string `json:"mime_type,omitempty"`
}

// DelegationStageContext carries optional host workflow coordinates without
// making the delegation package responsible for workflow orchestration.
type DelegationStageContext struct {
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	Stage         string `json:"stage,omitempty"`
	StepID        string `json:"step_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
}

// DelegationArtifact is the compact artifact projection returned to the
// leader and emitted in DelegationArtifactCreated events.
type DelegationArtifact struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	URI         string         `json:"uri,omitempty"`
	MediaType   string         `json:"mime_type,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// RemoteArtifact preserves an opt-in full remote A2A artifact projection.
type RemoteArtifact struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []RemotePart   `json:"parts,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

// RemotePart preserves one part of an opt-in RemoteArtifact.
type RemotePart struct {
	Kind      clienta2a.PartKind `json:"kind,omitempty"`
	Text      string             `json:"text,omitempty"`
	Raw       []byte             `json:"raw,omitempty"`
	Data      any                `json:"data,omitempty"`
	URL       string             `json:"url,omitempty"`
	MediaType string             `json:"mime_type,omitempty"`
	Filename  string             `json:"filename,omitempty"`
	Metadata  map[string]any     `json:"metadata,omitempty"`
}

// StatusPartDecoder converts one host-owned A2A Status DataPart schema into
// DelegationEvent values. Implementations fill semantic fields only; the
// mapper supplies delegation identity, A2A coordinates, profile, and time.
// matched=false allows the next registered decoder to inspect the value.
type StatusPartDecoder interface {
	Profile() string
	DecodeStatusPart(data any) (events []DelegationEvent, matched bool, err error)
}

// DelegationEvent is one typed, ordered observation from a delegated task.
// Identity and remote coordinates are separate from the semantic payload so a
// host can correlate leader tool calls, delegation attempts, and A2A objects.
type DelegationEvent struct {
	RunID            string
	ParentToolCallID string
	DelegationID     string
	AgentKey         string
	AgentName        string
	Protocol         string

	RemoteTaskID     string
	RemoteContextID  string
	RemoteMessageID  string
	RemoteArtifactID string
	RemoteToolCallID string
	Sequence         uint64

	Kind     DelegationEventKind
	Name     string
	Role     string
	Delta    string
	Text     string
	ToolName string
	Args     any
	Result   any
	Artifact *DelegationArtifact
	Status   string
	// StatusParts preserves the remote A2A status message parts for hosts that
	// consume structured status data.
	StatusParts []RemotePart
	Error       *DelegationError
	Raw         map[string]any
	Time        time.Time
}

// DelegationResult is the terminal, structured outcome of one delegation.
// Summary and Messages are the model-facing layers; RawTask and
// RemoteArtifacts preserve explicitly requested protocol detail.
type DelegationResult struct {
	DelegationID    string                 `json:"delegation_id"`
	Agent           string                 `json:"agent"`
	RemoteProtocol  string                 `json:"remote_protocol"`
	RemoteTaskID    string                 `json:"remote_task_id,omitempty"`
	RemoteContextID string                 `json:"remote_context_id,omitempty"`
	Status          string                 `json:"status"`
	Summary         string                 `json:"summary,omitempty"`
	Artifacts       []DelegationArtifact   `json:"artifacts,omitempty"`
	RemoteArtifacts []RemoteArtifact       `json:"remote_artifacts,omitempty"`
	Messages        []DelegationMessage    `json:"messages,omitempty"`
	Error           *DelegationError       `json:"error,omitempty"`
	RawTask         map[string]any         `json:"raw_task,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// DelegationMessage is one normalized message retained in a result.
type DelegationMessage struct {
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`
}

// DelegationError is a stable host-facing error with optional A2A status and
// retry metadata.
type DelegationError struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable,omitempty"`
	RemoteStatus string         `json:"remote_status,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// Error implements error using Message, then Code, then a stable fallback.
func (e *DelegationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "delegation failed"
}

// DelegationLifecycleHook observes one resolved delegation before remote I/O
// and after its terminal result. Returning an error fails that delegation.
type DelegationLifecycleHook interface {
	BeforeDelegate(ctx context.Context, req BeforeDelegation) error
	AfterDelegate(ctx context.Context, req AfterDelegation) error
}

// BeforeDelegation is the defensive hook payload supplied before remote I/O.
type BeforeDelegation struct {
	DelegationID string
	AgentSpec    RemoteAgentSpec
	Request      DelegationRequest
}

// AfterDelegation is the defensive hook payload supplied after execution.
type AfterDelegation struct {
	DelegationID string
	AgentSpec    RemoteAgentSpec
	Request      DelegationRequest
	Result       DelegationResult
	Err          error
}

// DelegationLifecycleHookFuncs adapts optional functions to
// DelegationLifecycleHook.
type DelegationLifecycleHookFuncs struct {
	BeforeFunc func(context.Context, BeforeDelegation) error
	AfterFunc  func(context.Context, AfterDelegation) error
}

// BeforeDelegate invokes BeforeFunc when configured.
func (h DelegationLifecycleHookFuncs) BeforeDelegate(ctx context.Context, req BeforeDelegation) error {
	if h.BeforeFunc == nil {
		return nil
	}
	return h.BeforeFunc(ctx, req)
}

// AfterDelegate invokes AfterFunc when configured.
func (h DelegationLifecycleHookFuncs) AfterDelegate(ctx context.Context, req AfterDelegation) error {
	if h.AfterFunc == nil {
		return nil
	}
	return h.AfterFunc(ctx, req)
}

func cloneRemoteAgentSpec(spec RemoteAgentSpec) RemoteAgentSpec {
	spec.TrustedAuthOrigins = append([]string(nil), spec.TrustedAuthOrigins...)
	spec.AcceptedOutputModes = append([]string(nil), spec.AcceptedOutputModes...)
	spec.PreferredTransports = append([]clienta2a.TransportProtocol(nil), spec.PreferredTransports...)
	if spec.AgentCard != nil {
		card := *spec.AgentCard
		card.DefaultInputModes = append([]string(nil), card.DefaultInputModes...)
		card.DefaultOutputModes = append([]string(nil), card.DefaultOutputModes...)
		card.Skills = append([]clienta2a.Skill(nil), card.Skills...)
		card.SupportedInterfaces = append([]clienta2a.AgentInterface(nil), card.SupportedInterfaces...)
		card.Raw = cloneAnyMap(card.Raw)
		spec.AgentCard = &card
	}
	return spec
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAnyValue(v)
	}
	return out
}

// cloneAnyValue copies the JSON-shaped values carried in public metadata and
// raw protocol fields. Unknown immutable/scalar values are safe to share;
// the mutable map, slice, and byte forms produced by encoding/json are copied
// recursively so accessors cannot mutate Service-owned records.
func cloneAnyValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = cloneAnyValue(value[i])
		}
		return out
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}
