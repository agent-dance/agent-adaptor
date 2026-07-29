package a2adelegation

// Public convenience vocabulary for configuring a delegation Service. The
// lower-level Registry, EventBus, Delegator, and MCPServer remain available to
// hosts that need component-level composition.

import (
	"strings"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Runner is adaptor.Runner. Agent, Thread, and host decorators can all be Local
// delegation targets and execute in-process without an A2A server or HTTP hop.
type Runner = adaptor.Runner

// Event is the concise consumer-facing spelling of DelegationEvent.
type Event = DelegationEvent

// Policy is the concise consumer-facing spelling of DelegationPolicy.
type Policy = DelegationPolicy

// AgentRef is one delegatable role in a Service configuration: either a
// local in-process Runner (Local) or a remote A2A agent (Remote /
// RemoteAgent). Local and remote refs mix freely in one Config.Agents table
// and are indistinguishable to the leader: both are reached through the
// same delegate_to_agent tool, the same Delegator pipeline, and the same
// DelegationEvent stream.
type AgentRef struct {
	key         string
	displayName string
	policy      DelegationPolicy
	runner      Runner
	spec        *RemoteAgentSpec
}

// Local registers an in-process Runner as a delegatable target. Its Event
// stream is projected through the intentional adapter.stream.v1 wire schema,
// preserving text, reasoning, tool, approval, and drop semantics without a
// network hop.
func Local(key string, runner Runner, policy Policy) AgentRef {
	return AgentRef{key: strings.TrimSpace(key), policy: policy, runner: runner}
}

// LocalNamed registers an in-process Runner with a separate model-facing key
// and human-facing display name. The display name is carried by delegation
// events and is useful for UIs that need to distinguish a workflow role from
// its underlying provider, for example key "plan" and name "Claude Code
// Planner". A blank displayName falls back to key, matching Local.
func LocalNamed(key, displayName string, runner Runner, policy Policy) AgentRef {
	return AgentRef{
		key: strings.TrimSpace(key), displayName: strings.TrimSpace(displayName),
		policy: policy, runner: runner,
	}
}

// Remote registers a remote A2A target by Agent Card URL. Discovery and task
// execution use clients/a2a.
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
// hosts can gate a workflow on an exact sentinel without re-parsing transport
// payloads.
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
