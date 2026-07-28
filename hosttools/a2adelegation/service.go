package a2adelegation

// Service is the one-stop delegation entry: Registry + EventBus + Delegator +
// authenticated per-run MCP sidecar + result recording. The separate
// components remain exported for lower-level host composition.
//
//	team, err := a2adelegation.NewService(a2adelegation.Config{
//	    Agents: []a2adelegation.AgentRef{
//	        a2adelegation.Local("plan", planner, a2adelegation.Policy{}),
//	        a2adelegation.Remote("review", reviewCardURL, a2adelegation.Policy{}),
//	    },
//	})
//	defer team.Close()
//	leader := adaptor.New(leaderDriver, team.Option())

import (
	"context"
	"strings"
	"sync"
	"time"

	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

// defaultReplayLimit is the per-run EventBus replay depth when Config leaves
// ReplayLimit unset: late subscribers (bridges attaching after the leader
// already delegated) still see the full recent event history.
const defaultReplayLimit = 256

// Config configures NewService. Agents is required; all other fields have
// conservative local defaults.
type Config struct {
	// Agents is the delegation table: Local, Remote, and RemoteAgent refs
	// mix freely. At least one entry is required.
	Agents []AgentRef

	// ToolTimeout is the default per-delegation wall clock: it becomes
	// Policy.MaxTimeout for every agent whose own policy does not set one,
	// and is surfaced on Sidecar.ToolTimeout so hosts can align the
	// driver-side MCP tool timeout. Zero means no default ceiling.
	ToolTimeout time.Duration

	// Observe, when set, receives every DelegationEvent of every run that
	// goes through the Service (subscription starts at EnsureSidecar /
	// Delegate time). Callbacks run on a Service-owned goroutine, one run
	// at a time per run ID; Close waits for them to drain.
	Observe func(Event)

	// ReplayLimit overrides the EventBus replay depth (default 256).
	ReplayLimit int

	// Tenant is passed to the per-run MCP sidecar and forwarded on
	// delegations that do not carry their own tenant.
	Tenant string

	// Hook, when set, is chained after the Service's own result-recording
	// lifecycle hook (Before runs before delegation, After runs after the
	// result is recorded).
	Hook DelegationLifecycleHook

	// NewClient overrides A2A client construction for non-local specs
	// (test seam; Local refs always use the in-process loopback).
	NewClient ClientFactory

	// StatusDecoders registers additional host-owned status DataPart
	// schema decoders on the Delegator (adapter.stream.v1 is built in).
	StatusDecoders []StatusPartDecoder

	// NewID overrides delegation-ID minting (test seam).
	NewID func() string
}

// Service is the consolidated delegation runtime. Construct with NewService;
// Close releases every sidecar and observer. All methods are safe for
// concurrent use.
type Service struct {
	cfg       Config
	registry  *Registry
	bus       *EventBus
	delegator *Delegator
	locals    map[string]*localClient

	mu       sync.Mutex
	closed   bool
	sidecars map[string]*runSidecar
	results  map[string]map[string]DelegationResult
	// delegations preserves the EventBus acceptance order of Started events.
	// results remains the convenient latest-result-by-agent projection.
	delegations       map[string][]DelegationResult
	delegationIndexes map[string]map[string]int
	observers         map[string]context.CancelFunc
	wg                sync.WaitGroup
}

// NewService validates the agent table, registers every ref (local refs get
// a synthetic in-process AgentCard and a Runner-backed loopback client), and
// wires Registry + EventBus + Delegator with the Service's result-recording
// lifecycle hook.
func NewService(cfg Config) (*Service, error) {
	if len(cfg.Agents) == 0 {
		return nil, &DelegationError{Code: "configuration_error", Message: "delegation service requires at least one agent"}
	}
	s := &Service{
		cfg:               cfg,
		locals:            map[string]*localClient{},
		sidecars:          map[string]*runSidecar{},
		results:           map[string]map[string]DelegationResult{},
		delegations:       map[string][]DelegationResult{},
		delegationIndexes: map[string]map[string]int{},
		observers:         map[string]context.CancelFunc{},
	}
	registry, err := NewRegistry()
	if err != nil {
		return nil, err
	}
	for _, ref := range cfg.Agents {
		switch {
		case ref.runner != nil:
			spec := RemoteAgentSpec{
				Key:       ref.key,
				Protocol:  ProtocolA2A,
				AgentCard: localAgentCard(ref.key),
				Policy:    s.effectivePolicy(ref.policy),
			}
			if err := registry.Register(spec); err != nil {
				return nil, err
			}
			s.locals[ref.key] = newLocalClient(ref.key, ref.runner)
		case ref.spec != nil:
			spec := cloneRemoteAgentSpec(*ref.spec)
			spec.Policy = s.effectivePolicy(spec.Policy)
			if err := registry.Register(spec); err != nil {
				return nil, err
			}
		default:
			return nil, &DelegationError{Code: "invalid_agent", Message: "agent ref requires a non-nil local runner or a remote spec"}
		}
	}
	replay := cfg.ReplayLimit
	if replay <= 0 {
		replay = defaultReplayLimit
	}
	var opts []DelegatorOption
	for _, decoder := range cfg.StatusDecoders {
		opts = append(opts, WithStatusPartDecoder(decoder))
	}
	s.registry = registry
	s.bus = NewEventBus(replay)
	s.delegator = NewDelegator(registry, s.bus, opts...)
	s.delegator.beforePublish = s.recordPublishedEvent
	s.delegator.LifecycleHook = &serviceHook{service: s}
	s.delegator.NewClient = s.clientFactory
	if cfg.NewID != nil {
		s.delegator.NewID = cfg.NewID
	}
	return s, nil
}

// effectivePolicy applies Config.ToolTimeout as the default MaxTimeout for
// policies that do not set their own.
func (s *Service) effectivePolicy(policy DelegationPolicy) DelegationPolicy {
	if policy.MaxTimeout <= 0 && s.cfg.ToolTimeout > 0 {
		policy.MaxTimeout = s.cfg.ToolTimeout
	}
	return policy
}

// clientFactory dispatches per key: Local refs get the in-process loopback,
// everything else gets Config.NewClient or the default clients/a2a-backed
// client (mirroring the Delegator's own default construction).
func (s *Service) clientFactory(spec RemoteAgentSpec) A2AClient {
	if lc, ok := s.locals[spec.Key]; ok {
		return lc
	}
	if s.cfg.NewClient != nil {
		return s.cfg.NewClient(spec)
	}
	if strings.TrimSpace(spec.AgentCardURL) == "" {
		return nil
	}
	return a2aClientAdapter{Client: clienta2a.New(clienta2a.Options{
		AgentCardURL:        spec.AgentCardURL,
		Auth:                spec.Auth,
		HTTPClient:          spec.HTTPClient,
		TrustedAuthOrigins:  spec.TrustedAuthOrigins,
		AcceptedOutputModes: spec.AcceptedOutputModes,
		PreferredTransports: spec.PreferredTransports,
	})}
}

// EnsureSidecar returns the per-run MCP endpoint for runID, starting it on
// first use. Idempotent per run: repeated calls return the same URL and
// token. The endpoint serves the delegate_to_agent tool authenticated by
// Sidecar.BearerToken. Agent integrations should prefer Option or
// adaptor.WithRunServices, which publishes the typed MCP declaration safely.
func (s *Service) EnsureSidecar(runID string) (Sidecar, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Sidecar{}, &DelegationError{Code: "configuration_error", Message: "run id is required for a delegation sidecar"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Sidecar{}, &DelegationError{Code: "configuration_error", Message: "delegation service is closed"}
	}
	if sc, ok := s.sidecars[runID]; ok {
		return sc.info, nil
	}
	sc, err := newRunSidecar(s.delegator, runID, s.cfg.Tenant, s.cfg.ToolTimeout)
	if err != nil {
		return Sidecar{}, err
	}
	s.sidecars[runID] = sc
	s.observeRunLocked(runID)
	return sc.info, nil
}

// Delegate runs one delegation directly (the programmatic path; the MCP
// sidecar is the driver path). Events publish to the shared bus and the
// result is recorded like any sidecar-initiated delegation.
func (s *Service) Delegate(ctx context.Context, req DelegationRequest) (DelegationResult, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return DelegationResult{}, &DelegationError{Code: "configuration_error", Message: "delegation service is closed"}
	}
	if req.Tenant == "" {
		req.Tenant = s.cfg.Tenant
	}
	s.observeRunLocked(strings.TrimSpace(req.RunID))
	s.mu.Unlock()
	return s.delegator.Delegate(ctx, req)
}

// Result returns the recorded final DelegationResult of the given run and
// agent key. The result is
// recorded by the Service's lifecycle hook before the terminal delegation
// event reaches the bus, so a consumer that just saw a terminal event can
// read the result without further synchronization. When the same agent was
// delegated to multiple times in one run, the latest result wins.
func (s *Service) Result(runID, key string) (DelegationResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.results[strings.TrimSpace(runID)][strings.TrimSpace(key)]
	if !ok {
		return DelegationResult{}, false
	}
	return cloneDelegationResult(res), true
}

// Results returns all recorded results of one run, keyed by agent key.
func (s *Service) Results(runID string) map[string]DelegationResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	byKey := s.results[strings.TrimSpace(runID)]
	if len(byKey) == 0 {
		return nil
	}
	out := make(map[string]DelegationResult, len(byKey))
	for key, res := range byKey {
		out[key] = cloneDelegationResult(res)
	}
	return out
}

// Delegations returns every delegation accepted for runID in the exact order
// its DelegationStarted event was accepted by the Service's EventBus. Unlike
// Results, it preserves repeated delegations to the same agent. An in-flight
// entry has Status "running" until its final result is recorded. The returned
// slice and every result in it are defensive copies and remain available after
// ReleaseRun and Close, matching the existing result-recording lifecycle.
func (s *Service) Delegations(runID string) []DelegationResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	recorded := s.delegations[strings.TrimSpace(runID)]
	if len(recorded) == 0 {
		return nil
	}
	out := make([]DelegationResult, len(recorded))
	for i := range recorded {
		out[i] = cloneDelegationResult(recorded[i])
	}
	return out
}

// Bus exposes the shared EventBus for component-level subscribers. Ordinary
// Agent integration should use Option or adaptor.WithRunServices, which folds
// these events into the leader's existing Event stream.
func (s *Service) Bus() *EventBus { return s.bus }

// Registry exposes the delegation registry (read-only usage expected).
func (s *Service) Registry() *Registry { return s.registry }

// Delegator exposes the underlying Delegator for hosts that need the
// component-level API; its lifecycle hook and client factory are owned by
// the Service and must not be replaced.
func (s *Service) Delegator() *Delegator { return s.delegator }

// ReleaseRun shuts down the run's sidecar and observer and clears the run's
// bus state. Recorded results are kept so hosts can read them after the run
// ends; they are released with the Service.
func (s *Service) ReleaseRun(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	s.mu.Lock()
	sc := s.sidecars[runID]
	delete(s.sidecars, runID)
	cancel := s.observers[runID]
	delete(s.observers, runID)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.bus.ClearRun(runID)
	if sc != nil {
		return sc.close()
	}
	return nil
}

// Close shuts down every sidecar, stops every observer, clears bus state,
// and waits for observer callbacks to drain. Idempotent; safe to defer
// immediately after NewService. Recorded results remain readable.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	sidecars := s.sidecars
	s.sidecars = map[string]*runSidecar{}
	observers := s.observers
	s.observers = map[string]context.CancelFunc{}
	s.mu.Unlock()

	var firstErr error
	for runID, sc := range sidecars {
		if err := sc.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.bus.ClearRun(runID)
	}
	for runID, cancel := range observers {
		cancel()
		s.bus.ClearRun(runID)
	}
	s.wg.Wait()
	return firstErr
}

// recordResult stores the final result of one delegation, keyed by run and
// agent. Called from the lifecycle hook before the terminal event flushes.
func (s *Service) recordResult(runID, key string, result DelegationResult) {
	runID = strings.TrimSpace(runID)
	key = strings.TrimSpace(key)
	if runID == "" || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.results[runID] == nil {
		s.results[runID] = map[string]DelegationResult{}
	}
	result = cloneDelegationResult(result)
	if indexByID := s.delegationIndexes[runID]; indexByID != nil {
		if index, ok := indexByID[result.DelegationID]; ok {
			// Keep identity observed at Started when a failure result omits a
			// transport-assigned field, while treating the final result as the
			// authority for all fields it does carry.
			started := s.delegations[runID][index]
			if result.Agent == "" {
				result.Agent = started.Agent
			}
			if result.RemoteProtocol == "" {
				result.RemoteProtocol = started.RemoteProtocol
			}
			if result.RemoteTaskID == "" {
				result.RemoteTaskID = started.RemoteTaskID
			}
			if result.RemoteContextID == "" {
				result.RemoteContextID = started.RemoteContextID
			}
			s.delegations[runID][index] = result
		}
	}
	s.results[runID][key] = result
}

// recordPublishedEvent is called by Delegator while holding its publication
// mutex and before EventBus.Publish makes an event visible. This gives
// concurrent delegation producers and readers one authoritative Started
// order without reconstructing it from a map or a lossy replay buffer.
func (s *Service) recordPublishedEvent(event DelegationEvent) {
	if event.Kind != DelegationStarted {
		return
	}
	runID := strings.TrimSpace(event.RunID)
	if runID == "" || event.DelegationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delegationIndexes[runID] == nil {
		s.delegationIndexes[runID] = map[string]int{}
	}
	if _, exists := s.delegationIndexes[runID][event.DelegationID]; exists {
		return
	}
	s.delegationIndexes[runID][event.DelegationID] = len(s.delegations[runID])
	s.delegations[runID] = append(s.delegations[runID], DelegationResult{
		DelegationID:    event.DelegationID,
		Agent:           event.AgentKey,
		RemoteProtocol:  event.Protocol,
		RemoteTaskID:    event.RemoteTaskID,
		RemoteContextID: event.RemoteContextID,
		Status:          "running",
	})
}

// observeRunLocked starts the Observe forwarder for one run. Caller holds
// s.mu.
func (s *Service) observeRunLocked(runID string) {
	if s.cfg.Observe == nil || runID == "" || s.closed {
		return
	}
	if _, ok := s.observers[runID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := s.bus.SubscribeRun(ctx, runID)
	s.observers[runID] = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for ev := range ch {
			s.cfg.Observe(ev)
		}
	}()
}

// serviceHook is the Service-owned lifecycle hook: it records results and
// chains the host's Config.Hook. AfterDelegate runs before the Delegator
// flushes the buffered terminal event, which is what makes Result()
// immediately consistent with terminal events on the bus.
type serviceHook struct {
	service *Service
}

var _ DelegationLifecycleHook = (*serviceHook)(nil)

func (h *serviceHook) BeforeDelegate(ctx context.Context, before BeforeDelegation) error {
	if hook := h.service.cfg.Hook; hook != nil {
		return hook.BeforeDelegate(ctx, before)
	}
	return nil
}

func (h *serviceHook) AfterDelegate(ctx context.Context, after AfterDelegation) error {
	h.service.recordResult(after.Request.RunID, after.AgentSpec.Key, after.Result)
	if hook := h.service.cfg.Hook; hook != nil {
		return hook.AfterDelegate(ctx, after)
	}
	return nil
}
