// Package a2adelegation contains host-owned tools for exposing curated remote
// A2A agents as visual subagents of a local parent run.
//
// The package deliberately stays above the core SDK: it consumes
// pkg/clients/a2a DTOs, emits UI-facing delegation events, and leaves concrete
// local adapters unaware of A2A.
package a2adelegation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

const ProtocolA2A = "a2a"

const DelegateToolName = "delegate_to_agent"

type DelegationEventKind string

const (
	DelegationStarted         DelegationEventKind = "subagent.started"
	DelegationStatus          DelegationEventKind = "subagent.status"
	DelegationTextStart       DelegationEventKind = "subagent.text.start"
	DelegationTextDelta       DelegationEventKind = "subagent.text.delta"
	DelegationTextEnd         DelegationEventKind = "subagent.text.end"
	DelegationReasoningStart  DelegationEventKind = "subagent.reasoning.start"
	DelegationReasoningDelta  DelegationEventKind = "subagent.reasoning.delta"
	DelegationReasoningEnd    DelegationEventKind = "subagent.reasoning.end"
	DelegationToolCallStart   DelegationEventKind = "subagent.tool_call.start"
	DelegationToolCallArgs    DelegationEventKind = "subagent.tool_call.args"
	DelegationToolCallResult  DelegationEventKind = "subagent.tool_call.result"
	DelegationToolCallEnd     DelegationEventKind = "subagent.tool_call.end"
	DelegationArtifactCreated DelegationEventKind = "subagent.artifact"
	DelegationCustom          DelegationEventKind = "subagent.custom"
	DelegationStreamDropped   DelegationEventKind = "subagent.stream.dropped"
	DelegationInputRequired   DelegationEventKind = "subagent.input_required"
	DelegationFinished        DelegationEventKind = "subagent.finished"
	DelegationFailed          DelegationEventKind = "subagent.failed"
	DelegationCancelled       DelegationEventKind = "subagent.cancelled"
)

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

type DelegationPolicy struct {
	MaxTimeout         time.Duration
	AllowInputRequired bool
	RequireStreaming   bool
	PollInterval       time.Duration
	MaxPolls           int
	MaxArtifactBytes   int64
}

type Registry struct {
	agents map[string]RemoteAgentSpec
}

func NewRegistry(specs ...RemoteAgentSpec) (*Registry, error) {
	r := &Registry{agents: map[string]RemoteAgentSpec{}}
	for _, spec := range specs {
		if err := r.Register(spec); err != nil {
			return nil, err
		}
	}
	return r, nil
}

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

func (r *Registry) Lookup(key string) (RemoteAgentSpec, bool) {
	if r == nil || len(r.agents) == 0 {
		return RemoteAgentSpec{}, false
	}
	spec, ok := r.agents[strings.TrimSpace(key)]
	return cloneRemoteAgentSpec(spec), ok
}

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

type InputArtifact struct {
	Name      string `json:"name,omitempty"`
	URI       string `json:"uri,omitempty"`
	MediaType string `json:"mime_type,omitempty"`
}

type DelegationStageContext struct {
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	Stage         string `json:"stage,omitempty"`
	StepID        string `json:"step_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
}

type DelegationArtifact struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	URI         string         `json:"uri,omitempty"`
	MediaType   string         `json:"mime_type,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type RemoteArtifact struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []RemotePart   `json:"parts,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

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

// StatusPartDecoder 把一种 Status DataPart schema 直接转换为 DelegationEvent。
// decoder 只填写事件语义字段；当前 delegation 的身份、A2A 上下文和 profile
// 由 mapper 统一补齐。matched=false 表示 data 不属于当前 decoder，允许后续 decoder 继续尝试。
type StatusPartDecoder interface {
	Profile() string
	DecodeStatusPart(data any) (events []DelegationEvent, matched bool, err error)
}

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

type DelegationMessage struct {
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`
}

type DelegationError struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable,omitempty"`
	RemoteStatus string         `json:"remote_status,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

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

type DelegationLifecycleHook interface {
	BeforeDelegate(ctx context.Context, req BeforeDelegation) error
	AfterDelegate(ctx context.Context, req AfterDelegation) error
}

type BeforeDelegation struct {
	DelegationID string
	AgentSpec    RemoteAgentSpec
	Request      DelegationRequest
}

type AfterDelegation struct {
	DelegationID string
	AgentSpec    RemoteAgentSpec
	Request      DelegationRequest
	Result       DelegationResult
	Err          error
}

type DelegationLifecycleHookFuncs struct {
	BeforeFunc func(context.Context, BeforeDelegation) error
	AfterFunc  func(context.Context, AfterDelegation) error
}

func (h DelegationLifecycleHookFuncs) BeforeDelegate(ctx context.Context, req BeforeDelegation) error {
	if h.BeforeFunc == nil {
		return nil
	}
	return h.BeforeFunc(ctx, req)
}

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
