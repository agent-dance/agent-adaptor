package adaptor

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
)

// threadInvocationFingerprint covers every resolved value that can change
// resume correctness. The construction config is supplied by the driver via a
// stable, secret-safe contract; the remaining values are the concrete request
// handed to that same configured driver (including the acquired workspace and
// runtime-service attachment payloads).
func (a *Agent) threadInvocationFingerprint(identity driver.AgentIdentity, req driver.Request, contract threadDriverContract, mcpCompatibilityFingerprint string, profileSnapshot any) string {
	runtimeCompatibility := threadRuntimeCompatibility(req.Runtime, mcpCompatibilityFingerprint)
	a.normalizeHostedToolServiceCompatibility(&runtimeCompatibility)
	// Hosted Tool normalization changes one URL after the generic view was
	// sorted. Re-sort so an ephemeral port cannot indirectly change collection
	// order when other runtime services are present.
	sortThreadRuntimeCompatibility(&runtimeCompatibility)
	// Streaming is this turn's delivery choice. Incompatible checkpoint or
	// session environments remain guarded by the configured Driver/codec and
	// the resolved resource dimensions below, not by this per-turn boolean.
	base := engine.StableHash(
		"adaptor/thread-invocation/v1",
		a.driver.Descriptor().Type,
		contract.codecName,
		contract.configFingerprint,
		identity,
		req.ModelOverride,
		req.Workspace,
		runtimeCompatibility,
		profileSnapshot,
		req.ProfilePayload.SessionFingerprint(),
		req.Skills.Fingerprint,
		engine.InstructionFingerprint(req.Instructions),
	)
	if req.AppendSystemPrompt == "" {
		return base
	}
	return engine.StableHash("adaptor/thread-append-system-prompt/v1", base, systemprompt.Fingerprint(req.AppendSystemPrompt))
}

type threadDriverContract struct {
	codecName         string
	configFingerprint string
}

// validateThreadDriverContract is the Thread prelaunch gate. It runs before
// workspace/runtime/profile acquisition and before any store operation, so an
// incomplete resume declaration cannot acquire a lease or launch the Driver.
func validateThreadDriverContract(d driver.Driver) (contract threadDriverContract, err error) {
	codecName, err := engine.ValidateThreadSessionDriver(d)
	if err != nil {
		return threadDriverContract{}, err
	}
	fingerprinter, ok := d.(driver.SessionConfigFingerprinter)
	if !ok {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "resume-capable driver does not implement SessionConfigFingerprinter"}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			contract = threadDriverContract{}
			err = &engine.SessionIncompatibleError{Reason: fmt.Sprintf("driver config fingerprinter panicked (%T)", recovered)}
		}
	}()
	first, fpErr := fingerprinter.SessionConfigFingerprint()
	if fpErr != nil {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint failed: " + fpErr.Error()}
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver returned an empty config fingerprint"}
	}
	second, fpErr := fingerprinter.SessionConfigFingerprint()
	if fpErr != nil {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint stability check failed: " + fpErr.Error()}
	}
	if strings.TrimSpace(second) != first {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint is not stable"}
	}
	return threadDriverContract{codecName: strings.TrimSpace(codecName), configFingerprint: first}, nil
}

// threadRuntimeCompatibility is the stable, secret-free part of the resolved
// runtime environment that can affect whether a provider session is safe to
// resume. RuntimePayload itself is deliberately not hashable for this purpose:
// SecretEnv carries freshly issued credentials whose values must reach every
// driver invocation but must neither invalidate a healthy Thread nor enter a
// durable compatibility fingerprint.
//
// Requested declarations and normalized ensured refs retain the fields that
// describe service identity and the actual endpoint exposed to the driver.
// Status and Health are observations, not endpoint identity, and are omitted
// so a transient probe result does not split a conversation. MCP is represented
// by the already-normalized effective MCP fingerprint; this covers both host
// servers and runtime-published servers without duplicating raw attachment
// material. Secret environment variable names are compatibility-relevant and
// non-secret, while their values are intentionally absent.
func threadRuntimeCompatibility(runtime driver.RuntimePayload, mcpFingerprint string) threadRuntimeCompatibilityView {
	view := threadRuntimeCompatibilityView{
		MCPFingerprint: mcpFingerprint,
		Requested:      make([]threadRuntimeServiceSpecView, 0, len(runtime.Requested)),
		Ensured:        make([]threadRuntimeServiceRefView, 0, len(runtime.Ensured)),
		SecretEnvNames: make([]string, 0, len(runtime.SecretEnv)),
	}
	for _, spec := range runtime.Requested {
		view.Requested = append(view.Requested, threadRuntimeServiceSpecView{
			ID:          spec.ID,
			Name:        spec.Name,
			URL:         spec.URL,
			Description: spec.Description,
			Lifecycle:   spec.Lifecycle,
			ReuseKey:    spec.ReuseKey,
			Command:     spec.Command,
			CWD:         spec.CWD,
			Port:        spec.Port,
			Metadata:    maps.Clone(spec.Metadata),
		})
	}
	for _, ref := range runtime.Ensured {
		view.Ensured = append(view.Ensured, threadRuntimeServiceRefView{
			ID:           ref.ID,
			Name:         ref.Name,
			URL:          ref.URL,
			Lifecycle:    ref.Lifecycle,
			ReuseKey:     ref.ReuseKey,
			Command:      ref.Command,
			CWD:          ref.CWD,
			Port:         ref.Port,
			OwnerAgentID: ref.OwnerAgentID,
			Metadata:     maps.Clone(ref.Metadata),
		})
	}
	for _, binding := range runtime.SecretEnv {
		if name := strings.TrimSpace(binding.Name); name != "" {
			view.SecretEnvNames = append(view.SecretEnvNames, name)
		}
	}

	sortThreadRuntimeCompatibility(&view)
	return view
}

func sortThreadRuntimeCompatibility(view *threadRuntimeCompatibilityView) {
	if view == nil {
		return
	}
	// Service collection order is not semantic: managers may discover the
	// same endpoints in a different order on the next process or run.
	sortThreadServices(view.Requested)
	sortThreadServices(view.Ensured)
	slices.Sort(view.SecretEnvNames)
	view.SecretEnvNames = slices.Compact(view.SecretEnvNames)
}

// Hash each immutable service once. Hashing inside the comparator repeatedly
// serializes the same metadata maps while producing the same canonical order.
func sortThreadServices[T any](services []T) {
	if len(services) < 2 {
		return
	}
	type hashedService struct {
		value T
		key   string
	}
	ordered := make([]hashedService, len(services))
	for i, service := range services {
		ordered[i] = hashedService{value: service, key: engine.StableHash(service)}
	}
	slices.SortFunc(ordered, func(a, b hashedService) int {
		return strings.Compare(a.key, b.key)
	})
	for i, service := range ordered {
		services[i] = service.value
	}
}

type threadRuntimeCompatibilityView struct {
	Requested      []threadRuntimeServiceSpecView
	Ensured        []threadRuntimeServiceRefView
	MCPFingerprint string
	SecretEnvNames []string
}

type threadRuntimeServiceSpecView struct {
	ID          string
	Name        string
	URL         string
	Description string
	Lifecycle   driver.RuntimeServiceLifecycle
	ReuseKey    string
	Command     string
	CWD         string
	Port        int
	Metadata    map[string]string
}

type threadRuntimeServiceRefView struct {
	ID           string
	Name         string
	URL          string
	Lifecycle    driver.RuntimeServiceLifecycle
	ReuseKey     string
	Command      string
	CWD          string
	Port         int
	OwnerAgentID string
	Metadata     map[string]string
}
