package engine

import (
	"strings"
	"sync"
)

const (
	// DefaultAgentName is the reserved binding name used by SDK.Run/Start
	// and SDK.Default.
	DefaultAgentName = "default"

	// DefaultRunEventBuffer matches the legacy channelEventSink capacity.
	DefaultRunEventBuffer = 64
	// DefaultStreamEventBuffer is sized for interactive chat loads with
	// typical token cadence of 50–100 events per second.
	DefaultStreamEventBuffer = 1024
)

// EventBackpressure selects how the SDK reacts when a host cannot keep up
// with StreamPayload delivery. RunEvent delivery always falls back to the
// legacy drop-with-marker behaviour and is not affected by this setting.
type EventBackpressure int

const (
	// BackpressureDropStream drops StreamPayloads when the stream channel is
	// full and emits a single StreamDropped marker (carrying the lost count)
	// as soon as capacity returns. This is the default and guarantees that
	// adapter sub-processes never block on a slow host.
	BackpressureDropStream EventBackpressure = iota
	// BackpressureBlock blocks the adapter goroutine until the host consumes
	// a StreamPayload. Use this when the host cannot tolerate any gaps (for
	// example a strict AG-UI conformance client).
	BackpressureBlock
)

// Core is the execution pipeline extracted from the historical root-package
// sdkImpl. The root package wraps it in the public SDK facade; the
// behaviorally identical option merging, session coordination, skill
// resolution, runtime preparation, and admin probes all live here.
type Core struct {
	defaultBinding    AgentBinding
	namedBindings     map[string]AgentBinding
	sessionStore      SessionStore
	workspaceManager  WorkspaceManager
	skillProvider     SkillProvider
	skillMaterializer SkillMaterializer
	runtimeManager    RuntimeServiceManager
	mu                sync.RWMutex
	// skillSelections tracks process-local SetSelectedSkills overrides per
	// agent name. The value is the list of keys the admin picked; Required
	// skills from the SkillProvider are unioned in at Run time.
	skillSelections map[string][]string

	eventRunBuf    int
	eventStreamBuf int
	eventPolicy    EventBackpressure
}

// Build constructs the pipeline core and returns configuration errors to the
// caller. The root package's Build/New wrap it into the public SDK facade.
func Build(opts ...Option) (*Core, error) {
	s := &Core{
		namedBindings:     map[string]AgentBinding{},
		workspaceManager:  passthroughWorkspaceManager{},
		skillProvider:     emptySkillProvider{},
		skillMaterializer: newDefaultSkillMaterializer(),
		runtimeManager:    noopRuntimeManager{},
		skillSelections:   map[string][]string{},
		eventRunBuf:       DefaultRunEventBuffer,
		eventStreamBuf:    DefaultStreamEventBuffer,
		eventPolicy:       BackpressureDropStream,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(s); err != nil {
			return nil, err
		}
	}
	if s.defaultBinding == nil {
		return nil, ErrDefaultAgentMissing
	}
	return s, nil
}

// EventSinkSettings returns a snapshot of the runtime sink configuration used
// by the root package's Start()/Run() when building a dualSink. Values <= 0
// fall back to the SDK defaults.
func (s *Core) EventSinkSettings() (runBuf, streamBuf int, policy EventBackpressure) {
	runBuf = s.eventRunBuf
	if runBuf <= 0 {
		runBuf = DefaultRunEventBuffer
	}
	streamBuf = s.eventStreamBuf
	if streamBuf <= 0 {
		streamBuf = DefaultStreamEventBuffer
	}
	policy = s.eventPolicy
	return
}

// DefaultBinding returns the binding installed by WithDefaultAgent.
func (s *Core) DefaultBinding() AgentBinding { return s.defaultBinding }

// NamedBinding looks up a named binding registered via WithAgent.
func (s *Core) NamedBinding(name string) (AgentBinding, bool) {
	binding, ok := s.namedBindings[name]
	return binding, ok
}

// Admin returns the control-plane implementation over the same bindings.
func (s *Core) Admin() AdminAPI {
	return &adminImpl{sdk: s}
}

// selectedRefsFor returns the effective default SkillRef list for agent name.
// If SetSelectedSkills has installed a process-local override, the override
// keys are returned as SkillKey refs (the admin picks by key, not by full
// Skill value). Otherwise the binding's WithDefaultSkills values are cloned
// and returned.
func (s *Core) selectedRefsFor(name string, bindingDefaults []SkillRef) []SkillRef {
	s.mu.RLock()
	override, ok := s.skillSelections[name]
	s.mu.RUnlock()
	if ok {
		out := make([]SkillRef, 0, len(override))
		for _, key := range override {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			out = append(out, SkillKey(key))
		}
		return out
	}
	return cloneSkillRefs(bindingDefaults)
}

// setSelectedSkillsFor installs a process-local selection override for the
// given agent name. Callers must have already validated that each key
// exists in the SkillProvider.
func (s *Core) setSelectedSkillsFor(name string, selected []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skillSelections[name] = cloneStrings(selected)
}
