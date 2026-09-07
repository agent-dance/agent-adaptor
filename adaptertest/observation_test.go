package adaptertest

import (
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/todo"
)

func alignmentObservationPayloads() []driver.StreamPayload {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	start := capability.Invocation{InvocationID: "call", Ref: capability.Ref{Kind: capability.MCP, Key: "knowledge", Operation: "search"}, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: at}
	end := start
	end.Phase = capability.Completed
	zero := time.Duration(0)
	end.Duration = &zero
	return []driver.StreamPayload{
		{Kind: driver.StreamRunStarted},
		{Kind: driver.StreamCapabilityInvocation, Capability: &start},
		{Kind: driver.StreamTodoUpdated, Todo: &todo.Snapshot{Items: []todo.Item{{ID: "91", Content: "中文 task", Status: todo.Pending}}, Source: todo.ToolResult, Revision: 1, OccurredAt: at}},
		{Kind: driver.StreamTodoUpdated, Todo: &todo.Snapshot{Items: []todo.Item{}, Source: todo.ToolResult, Revision: 2, OccurredAt: at}},
		{Kind: driver.StreamCapabilityInvocation, Capability: &end},
		{Kind: driver.StreamRunFinished},
	}
}
func TestAlignmentObservationValidSequenceAndClear(t *testing.T) {
	payloads := alignmentObservationPayloads()
	if v := VerifyStreamSequence(payloads); len(v) != 0 {
		t.Fatal(v)
	}
	if v := verifyObservationSupport(driver.ObservationSupport{MCP: true, Todos: true}, payloads); len(v) != 0 {
		t.Fatal(v)
	}
}
func TestAlignmentObservationRejectsContractBreaches(t *testing.T) {
	for _, tc := range []struct {
		name, clause string
		mutate       func([]driver.StreamPayload) []driver.StreamPayload
	}{
		{"missing payload", "OBS-02", func(p []driver.StreamPayload) []driver.StreamPayload { p[1].Capability = nil; return p }},
		{"second semantics", "OBS-02", func(p []driver.StreamPayload) []driver.StreamPayload {
			p[1].Raw = map[string]any{"secret": "never allowed"}
			return p
		}},
		{"wrong kind", "OBS-02", func(p []driver.StreamPayload) []driver.StreamPayload { p[0].Capability = p[1].Capability; return p }},
		{"role", "EVT-09", func(p []driver.StreamPayload) []driver.StreamPayload { p[1].Role = driver.RoleUser; return p }},
		{"driver sequence", "EVT-10", func(p []driver.StreamPayload) []driver.StreamPayload { p[1].Seq = 1; return p }},
		{"host evidence", "OBS-03", func(p []driver.StreamPayload) []driver.StreamPayload {
			p[1].Capability.Source = capability.Host
			p[1].Capability.Evidence = capability.HostLifecycle
			return p
		}},
		{"unknown phase", "OBS-03", func(p []driver.StreamPayload) []driver.StreamPayload { p[1].Capability.Phase = "almost_done"; return p }},
		{"negative duration", "OBS-03", func(p []driver.StreamPayload) []driver.StreamPayload {
			d := -time.Second
			p[4].Capability.Duration = &d
			return p
		}},
		{"invalid utf8", "OBS-03", func(p []driver.StreamPayload) []driver.StreamPayload { p[1].Capability.Ref.Key = "\xff"; return p }},
		{"start duration", "OBS-03", func(p []driver.StreamPayload) []driver.StreamPayload {
			d := time.Duration(0)
			p[1].Capability.Duration = &d
			return p
		}},
		{"changed identity", "OBS-04", func(p []driver.StreamPayload) []driver.StreamPayload { p[4].Capability.Ref.Key = "other"; return p }},
		{"missing start", "OBS-04", func(p []driver.StreamPayload) []driver.StreamPayload { return append(p[:1], p[2:]...) }},
		{"missing terminal", "OBS-04", func(p []driver.StreamPayload) []driver.StreamPayload { return append(p[:4], p[5:]...) }},
		{"duplicate start", "OBS-04", func(p []driver.StreamPayload) []driver.StreamPayload {
			return append(p[:2], append([]driver.StreamPayload{p[1]}, p[2:]...)...)
		}},
		{"nil is not clear", "OBS-05", func(p []driver.StreamPayload) []driver.StreamPayload { p[3].Todo.Items = nil; return p }},
		{"duplicate item", "OBS-05", func(p []driver.StreamPayload) []driver.StreamPayload {
			p[2].Todo.Items = append(p[2].Todo.Items, p[2].Todo.Items[0])
			return p
		}},
		{"unknown status", "OBS-05", func(p []driver.StreamPayload) []driver.StreamPayload { p[2].Todo.Items[0].Status = "unknown"; return p }},
		{"revision gap", "OBS-06", func(p []driver.StreamPayload) []driver.StreamPayload { p[3].Todo.Revision = 3; return p }},
		{"unchanged snapshot", "OBS-06", func(p []driver.StreamPayload) []driver.StreamPayload { p[3].Todo.Items = p[2].Todo.Items; return p }},
		{"after terminal", "EVT-02", func(p []driver.StreamPayload) []driver.StreamPayload { return append(p, p[3]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := VerifyStreamSequence(tc.mutate(alignmentObservationPayloads()))
			for _, item := range v {
				if item.Clause == tc.clause {
					return
				}
			}
			t.Fatalf("want %s violation, got %v", tc.clause, v)
		})
	}
}
func TestAlignmentObservationTransportNegatives(t *testing.T) {
	for _, kind := range []capability.Kind{capability.Skill, capability.MCP, capability.Subagent} {
		t.Run(string(kind), func(t *testing.T) {
			p := alignmentObservationPayloads()
			p[1].Capability.Ref.Kind = kind
			if got := verifyObservationSupport(driver.ObservationSupport{}, p[:2]); len(got) != 1 || got[0].Clause != "OBS-01" {
				t.Fatal(got)
			}
		})
	}
	if got := verifyObservationSupport(driver.ObservationSupport{MCP: true}, alignmentObservationPayloads()); len(got) != 2 {
		t.Fatal(got)
	}
	// A declaration is not proof of use. Empty observations are allowed.
	if got := verifyObservationSupport(driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}, nil); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestAlignmentScopedToolLifecycles(t *testing.T) {
	p := []driver.StreamPayload{{Kind: driver.StreamRunStarted}}
	for _, scope := range []string{"root/child", "子"} {
		p = append(p, driver.StreamPayload{Kind: driver.StreamToolCallStart, ScopeID: scope, ToolCallID: "same", Name: "Read"}, driver.StreamPayload{Kind: driver.StreamToolCallEnd, ScopeID: scope, ToolCallID: "same"}, driver.StreamPayload{Kind: driver.StreamToolCallResult, ScopeID: scope, ToolCallID: "same"})
	}
	p = append(p, driver.StreamPayload{Kind: driver.StreamRunFinished})
	if v := VerifyStreamSequence(p); len(v) != 0 {
		t.Fatal(v)
	}
}
func TestAlignmentToolSnapshotAndParentViolation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		follow driver.StreamPayload
	}{
		{"args after complete snapshot", driver.StreamPayload{Kind: driver.StreamToolCallArgs, ToolCallID: "x", Delta: "{}", ParentToolCallID: "parent"}},
		{"parent changes", driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "x", ParentToolCallID: "different"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := []driver.StreamPayload{{Kind: driver.StreamRunStarted}, {Kind: driver.StreamToolCallStart, ToolCallID: "x", Name: "Read", Args: map[string]any{}, ParentToolCallID: "parent"}, tc.follow, {Kind: driver.StreamToolCallEnd, ToolCallID: "x", ParentToolCallID: "parent"}, {Kind: driver.StreamRunFinished}}
			for _, v := range VerifyStreamSequence(p) {
				if v.Clause == "EVT-14" {
					return
				}
			}
			t.Fatal("missing EVT-14")
		})
	}
}

type alignmentSharedDescriptor struct {
	driver.Driver
	descriptor driver.Descriptor
}

func (d alignmentSharedDescriptor) Descriptor() driver.Descriptor { return d.descriptor }
func TestAlignmentDescriptorSnapshotOracle(t *testing.T) {
	shared := alignmentSharedDescriptor{Driver: NewReferenceDriver(ReferenceConfig{}), descriptor: driver.Descriptor{StructuredOutput: driver.StructuredOutputCapability{NativeHITL: &driver.StructuredOutputHITLCapability{Question: true}}}}
	if v := verifyDescriptorSnapshot(shared); len(v) != 1 || v[0].Clause != "DRV-03" {
		t.Fatalf("shared descriptor matrix accepted: %v", v)
	}
}
