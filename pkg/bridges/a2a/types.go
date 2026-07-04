// Package a2a exposes an agent-adaptor Runner as an A2A-compatible agent.
//
// The bridge is deliberately protocol-local: it uses the official
// github.com/a2aproject/a2a-go/v2 SDK for A2A wire behavior, but it does not
// add A2A concepts to the agent-adaptor core SDK and it never imports concrete
// provider adapters such as codex, claude, or cursor.
package a2a

import (
	"context"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type AgentCard struct {
	Name               string
	Description        string
	URL                string
	Version            string
	DocumentationURL   string
	IconURL            string
	Provider           *Provider
	Capabilities       Capabilities
	DefaultInputModes  []string
	DefaultOutputModes []string
	Skills             []Skill
	Interfaces         []AgentInterface
	SecuritySchemes    []SecurityScheme
	Security           []SecurityRequirement
}

type Provider struct {
	Organization string
	URL          string
}

type CapabilityMode uint8

const (
	CapabilityDefault CapabilityMode = iota
	CapabilityEnabled
	CapabilityDisabled
)

type Capabilities struct {
	Streaming         CapabilityMode
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
	ProtocolBinding string
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

type SecurityScheme struct {
	Name         string
	Type         SecuritySchemeType
	Description  string
	Scheme       string
	BearerFormat string
	In           string
	ParamName    string
}

type SecuritySchemeType string

const (
	SecurityHTTP      SecuritySchemeType = "http"
	SecurityAPIKey    SecuritySchemeType = "apiKey"
	SecurityMutualTLS SecuritySchemeType = "mutualTLS"
)

type SecurityRequirement struct {
	Schemes map[string][]string
}

const (
	DefaultEphemeralTaskLimit = 256
	DefaultEphemeralTaskTTL   = time.Hour
)

const (
	// ArtifactAssistantOutput is the bridge-owned A2A artifact name used for
	// streamed assistant-facing text deltas.
	ArtifactAssistantOutput = "assistant-output"
	// ArtifactAgentAdaptorResult is the bridge-owned A2A artifact name used
	// for the terminal structured SDK result summary and opt-in diagnostics.
	ArtifactAgentAdaptorResult = "agent-adaptor-result"
)

type EphemeralTaskStoreOptions struct {
	MaxTasks int
	TTL      time.Duration
}

type TaskLifecycleOptions struct {
	Store     taskstore.Store
	Ephemeral *EphemeralTaskStoreOptions
}

type PushNotificationSupport struct {
	Store  push.ConfigStore
	Sender push.Sender
}

type ExtendedAgentCardRequest struct {
	Tenant string
}

type ExtendedAgentCardProvider interface {
	ExtendedCard(ctx context.Context, req ExtendedAgentCardRequest) (AgentCard, error)
}

type ExtendedAgentCardProviderFunc func(context.Context, ExtendedAgentCardRequest) (AgentCard, error)

func (fn ExtendedAgentCardProviderFunc) ExtendedCard(ctx context.Context, req ExtendedAgentCardRequest) (AgentCard, error) {
	return fn(ctx, req)
}

type ExtendedAgentCardSupport struct {
	Static   *AgentCard
	Provider ExtendedAgentCardProvider
}

type ServerOptions struct {
	AgentCard AgentCard
	Session   SessionMapper
	Prompt    PromptBuilder

	RunOptions        []agentadaptor.RunOption
	TaskLifecycle     TaskLifecycleOptions
	PushNotifications *PushNotificationSupport
	ExtendedAgentCard *ExtendedAgentCardSupport
	Exposure          ExposurePolicy
}

// ExposurePolicy controls which non-user-facing bridge artifacts are exposed
// to remote A2A callers.
//
// The zero value is intentionally conservative:
//   - assistant-facing Output still flows through the task status message
//   - terminal summary still flows through the agent-adaptor-result artifact
//   - reasoning, tool-call internals, HITL events, and diagnostics stay hidden
type ExposurePolicy struct {
	IncludeReasoning bool
	IncludeToolCalls bool
	IncludeHITL      bool

	Diagnostics DiagnosticsPolicy
}

// DiagnosticsPolicy controls opt-in exposure of internal execution details.
//
// All enabled fields are sanitized before they leave the bridge.
type DiagnosticsPolicy struct {
	IncludeMetadata       bool
	IncludeUsage          bool
	IncludeProviderResult bool
	IncludeTranscript     bool
	IncludeRawStreams     bool
	IncludeHITLPayloads   bool
	IncludeHITLRaw        bool
}

type InboundRequest struct {
	TaskID    string
	ContextID string
	Message   Message
	Metadata  map[string]any
}

type Message struct {
	ID             string
	Role           string
	TaskID         string
	ContextID      string
	Parts          []Part
	ReferenceTasks []string
	Extensions     []string
	Metadata       map[string]any
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

type SessionMapper interface {
	RunOptions(ctx context.Context, req InboundRequest) ([]agentadaptor.RunOption, error)
}

type PromptBuilder interface {
	BuildPrompt(ctx context.Context, req InboundRequest) (prompt string, opts []agentadaptor.RunOption, err error)
}

type SessionMapperFunc func(context.Context, InboundRequest) ([]agentadaptor.RunOption, error)

func (fn SessionMapperFunc) RunOptions(ctx context.Context, req InboundRequest) ([]agentadaptor.RunOption, error) {
	return fn(ctx, req)
}

type PromptBuilderFunc func(context.Context, InboundRequest) (string, []agentadaptor.RunOption, error)

func (fn PromptBuilderFunc) BuildPrompt(ctx context.Context, req InboundRequest) (string, []agentadaptor.RunOption, error) {
	return fn(ctx, req)
}

func Stateless() SessionMapper {
	return SessionMapperFunc(func(context.Context, InboundRequest) ([]agentadaptor.RunOption, error) {
		return nil, nil
	})
}

func SessionByContextID(namespace string) SessionMapper {
	return SessionMapperFunc(func(_ context.Context, req InboundRequest) ([]agentadaptor.RunOption, error) {
		if req.ContextID == "" {
			return nil, nil
		}
		return []agentadaptor.RunOption{agentadaptor.WithSessionKey(namespace, req.ContextID)}, nil
	})
}

func SessionByTaskID(namespace string) SessionMapper {
	return SessionMapperFunc(func(_ context.Context, req InboundRequest) ([]agentadaptor.RunOption, error) {
		if req.TaskID == "" {
			return nil, nil
		}
		return []agentadaptor.RunOption{agentadaptor.WithSessionKey(namespace, req.TaskID)}, nil
	})
}
