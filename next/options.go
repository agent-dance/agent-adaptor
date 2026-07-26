package adaptor

import (
	"maps"
	"time"
)

// ============ Scope interfaces (decision D7, case A) ============
//
// One option vocabulary, two scopes: the same WithX used in New(...) is the
// agent-level default, used in Run/Stream(...) it is a per-call override.
// The merge rule is a single sentence: the nearer scope wins; skills append,
// everything else replaces. Scope-illegal combinations are compile errors in
// both directions. See docs/p0-option-scope-decision.md.

// Option is the full set New accepts. It writes agent-level defaults.
//
// Passing an Option-only value (WithThreadStore, WithEventBuffer, ...) to
// Run/Stream does not compile: "adaptor.Option does not implement
// adaptor.CallOption (missing method ApplyRun)" means the option is
// construction-scope only.
type Option interface {
	// ApplyNew writes the option into the agent-level default settings.
	ApplyNew(*AgentSettings)
}

// CallOption is the set Run/Stream accept. It writes the effective settings
// of one invocation (a clone of the agent defaults).
//
// CallOption intentionally does NOT embed Option: call-scope-only options
// passed to New fail to compile too ("missing method ApplyNew"), keeping the
// misuse feedback symmetric in both directions.
type CallOption interface {
	// ApplyRun writes the option into this call's effective settings.
	ApplyRun(*RunSettings)
}

// SharedOption is the return type of dual-scope options: used in New it is
// the Agent's default, used in Run/Stream it overrides this call only. Most
// of the ~24 core options return it.
type SharedOption interface {
	Option
	CallOption
}

// ============ Option write targets (the controlled ecosystem surface) ============

// RunSettings collects every setting that can be overridden at the call
// site. Fields are unexported; ecosystem packages write through the exported
// methods below, whose semantics encode the merge rule (Set* replaces,
// Add* appends). The root package's own options go through the same methods
// so the extension surface stays self-validating.
type RunSettings struct {
	model        string
	timeout      time.Duration
	instructions string
	workspace    string
	metadata     map[string]string
	identity     *Identity
	policy       *Policy

	// Merge-semantics anchor (P3.2): skills are the single append-merged
	// option family in the "nearer scope wins; skills append, everything
	// else replaces" rule. When WithSkills lands, this struct gains
	//
	//	skills []skill.Ref
	//	func (s *RunSettings) AddSkills(refs ...skill.Ref)   // appends
	//
	// and clone() must deep-copy the slice so per-call appends never
	// pollute the agent defaults.
}

// SetModel replaces the effective model for the target scope.
func (s *RunSettings) SetModel(m string) { s.model = m }

// SetTimeout replaces the wall-clock budget for one run. Zero means no
// SDK-imposed deadline.
func (s *RunSettings) SetTimeout(d time.Duration) { s.timeout = d }

// SetInstructions replaces the extra instruction text handed to the driver.
func (s *RunSettings) SetInstructions(text string) { s.instructions = text }

// SetWorkspace replaces the working directory for the target scope.
func (s *RunSettings) SetWorkspace(dir string) { s.workspace = dir }

// SetMetadata sets one audit metadata key. Keys merge per key: a call-site
// value overrides the same key from the agent defaults and leaves the other
// default keys intact.
func (s *RunSettings) SetMetadata(k, v string) {
	if s.metadata == nil {
		s.metadata = make(map[string]string)
	}
	s.metadata[k] = v
}

// SetIdentity replaces the caller identity propagated to host hooks and the
// driver.
func (s *RunSettings) SetIdentity(id Identity) { s.identity = &id }

// SetPolicy replaces the whole execution policy ("everything else replaces":
// a call-site policy substitutes the agent-default policy as one value, it
// does not merge field-wise).
func (s *RunSettings) SetPolicy(p Policy) { s.policy = &p }

// clone returns a deep copy so per-call overrides never leak back into the
// agent defaults (and one run never pollutes the next).
func (s RunSettings) clone() RunSettings {
	out := s
	out.metadata = maps.Clone(s.metadata)
	if s.identity != nil {
		id := *s.identity
		out.identity = &id
	}
	if s.policy != nil {
		p := *s.policy
		out.policy = &p
	}
	return out
}

// AgentSettings = RunSettings (dual-scope fields) + construction-scope-only
// fields. The subset relation is expressed by struct embedding: a CallOption
// receives *RunSettings, on which the construction-only fields simply do not
// exist — the writable field set is the scope boundary.
type AgentSettings struct {
	RunSettings

	// threadStore is stored as an opaque value in P0 and consumed in P2
	// when Thread/threadstore.Store land. TODO(P2): type it as
	// threadstore.Store and wire it into session coordination.
	threadStore any

	// eventBuffer is stored in P0 and consumed in P1 by the Stream event
	// pipeline (backpressure buffer size). TODO(P1): wire into the sink.
	eventBuffer int
}

// SetThreadStore injects the thread storage backend (stateful conversations).
func (s *AgentSettings) SetThreadStore(store any) { s.threadStore = store }

// SetEventBuffer sets the per-run event channel buffer size.
func (s *AgentSettings) SetEventBuffer(n int) { s.eventBuffer = n }

// ============ In-package function adapters (one per scope) ============

// sharedOptionFunc backs dual-scope options: one func, two forwarding methods.
type sharedOptionFunc func(*RunSettings)

func (f sharedOptionFunc) ApplyNew(s *AgentSettings) { f(&s.RunSettings) }
func (f sharedOptionFunc) ApplyRun(s *RunSettings)   { f(s) }

// newOptionFunc backs construction-scope-only options.
type newOptionFunc func(*AgentSettings)

func (f newOptionFunc) ApplyNew(s *AgentSettings) { f(s) }

// callOptionFunc backs call-scope-only options. The P0 subset has none;
// WithSchema[T] and WithoutTokenStream (P1/P3) will use it.
type callOptionFunc func(*RunSettings)

func (f callOptionFunc) ApplyRun(s *RunSettings) { f(s) }

// Keep the adapter referenced until the first call-scope-only option lands.
var _ CallOption = callOptionFunc(nil)

// ============ P0 option vocabulary ============

// WithModel selects the model. In New it is the Agent's default model; in
// Run/Stream it overrides this invocation only (delivered to the driver as
// the per-run model override).
func WithModel(m string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetModel(m) })
}

// WithTimeout bounds one run's wall-clock time. In New it is the default
// budget for every run; in Run/Stream it overrides this invocation only.
// The SDK enforces it via context deadline; a run that exceeds it fails with
// context.DeadlineExceeded.
func WithTimeout(d time.Duration) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetTimeout(d) })
}

// WithInstructions supplies extra instruction text alongside the prompt.
// Nearer scope replaces: a call-site value substitutes the agent default.
func WithInstructions(text string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetInstructions(text) })
}

// WithWorkspace sets the working directory the agent operates in.
func WithWorkspace(dir string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetWorkspace(dir) })
}

// WithMetadata attaches one audit metadata key/value to runs. Metadata
// merges per key: call-site keys override same-named default keys and leave
// the rest of the defaults intact.
func WithMetadata(k, v string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetMetadata(k, v) })
}

// WithIdentity sets the caller identity (tenant / user / profile / agent
// scoping) propagated to host hooks and the driver. See Identity.
func WithIdentity(id Identity) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetIdentity(id) })
}

// WithPolicy sets the execution policy (sandbox, optional feature levels,
// and — from P1 — approvals). The policy replaces as a whole value; it does
// not merge field-wise with the agent default.
func WithPolicy(p Policy) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetPolicy(p) })
}

// WithThreadStore injects the thread storage backend that enables stateful
// conversations. Construction scope only; passing it to Run/Stream is a
// compile error (missing method ApplyRun).
//
// TODO(P2): the parameter becomes threadstore.Store when the threadstore
// package lands; in P0 the value is stored but not yet consumed.
func WithThreadStore(store any) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetThreadStore(store) })
}

// WithEventBuffer sets the per-run event channel buffer size used by the
// streaming pipeline. Construction scope only.
//
// TODO(P1): consumed when Stream/Event land; in P0 the value is stored only.
func WithEventBuffer(n int) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetEventBuffer(n) })
}
