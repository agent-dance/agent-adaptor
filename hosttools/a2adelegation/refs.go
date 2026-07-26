package a2adelegation

// P4.6 vocabulary for the delegation service (design doc §9.7/§9.8).
//
// The design doc imports this package under the alias "delegation", so the
// consumer-facing spellings are delegation.Local / delegation.Remote /
// delegation.Policy / delegation.Event. This file adds only new exported
// names; the pre-existing component types (Registry, EventBus, Delegator,
// MCPServer) stay untouched and remain usable on their own.

import (
	"strings"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// Runner is the next-gen SDK execution contract (adaptor.Runner): both
// *adaptor.Agent and *adaptor.Thread satisfy it, as does any host decorator
// that wraps one. Local delegation targets execute through this interface
// in-process — no A2A server, no HTTP hop.
type Runner = adaptor.Runner

// Event is the consumer-facing alias for DelegationEvent, matching the
// design-doc spelling delegation.Event (§9.7 Observe callback).
type Event = DelegationEvent

// Policy is the consumer-facing alias for DelegationPolicy, matching the
// design-doc spelling delegation.Policy (§9.7/§9.8).
type Policy = DelegationPolicy

// AgentRef is one delegatable role in a Service configuration: either a
// local in-process Runner (Local) or a remote A2A agent (Remote /
// RemoteAgent). Local and remote refs mix freely in one Config.Agents table
// and are indistinguishable to the leader: both are reached through the
// same delegate_to_agent tool, the same Delegator pipeline, and the same
// DelegationEvent stream.
type AgentRef struct {
	key    string
	policy DelegationPolicy
	runner Runner
	spec   *RemoteAgentSpec
}

// Local registers an in-process Runner as a delegatable role (design doc
// §9.8 / decision D5). The runner's event stream is consumed directly and
// projected through the adapter.stream.v1 profile, so text, reasoning, and
// tool-call fidelity survives without any A2A server or network hop.
func Local(key string, runner Runner, policy Policy) AgentRef {
	return AgentRef{key: strings.TrimSpace(key), policy: policy, runner: runner}
}

// Remote registers a remote A2A agent by its agent-card URL (design doc
// §9.7). The card is fetched and the task executed through clients/a2a.
func Remote(key, cardURL string, policy Policy) AgentRef {
	spec := &RemoteAgentSpec{
		Key:          strings.TrimSpace(key),
		AgentCardURL: strings.TrimSpace(cardURL),
		Policy:       policy,
	}
	return AgentRef{key: spec.Key, policy: policy, spec: spec}
}

// RemoteAgent registers a remote A2A agent from a full RemoteAgentSpec for
// hosts that need auth, tenant, transport, or accepted-output-mode control
// beyond what Remote(key, cardURL, policy) covers.
func RemoteAgent(spec RemoteAgentSpec) AgentRef {
	spec.Key = strings.TrimSpace(spec.Key)
	copySpec := cloneRemoteAgentSpec(spec)
	return AgentRef{key: copySpec.Key, policy: copySpec.Policy, spec: &copySpec}
}

// HasLine reports whether the result carries the given line — matched
// against the trimmed lines of Summary and of every result message — so
// hosts can gate a workflow on a sentinel (§9.7: review approval line)
// without re-parsing transport payloads.
func (r DelegationResult) HasLine(line string) bool {
	target := strings.TrimSpace(line)
	if target == "" {
		return false
	}
	if textHasLine(r.Summary, target) {
		return true
	}
	for _, msg := range r.Messages {
		if textHasLine(msg.Text, target) {
			return true
		}
	}
	return false
}

func textHasLine(text, target string) bool {
	if text == "" {
		return false
	}
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) == target {
			return true
		}
	}
	return false
}
