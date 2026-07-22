package agentadaptor_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// TestStreamSubagentKindConstants verifies that the three subagent StreamKind
// constants have the exact string values required by the workstream spec.
func TestStreamSubagentKindConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  agentadaptor.StreamKind
		want string
	}{
		{"StreamSubagentStart", agentadaptor.StreamSubagentStart, "subagent.start"},
		{"StreamSubagentStatus", agentadaptor.StreamSubagentStatus, "subagent.status"},
		{"StreamSubagentEnd", agentadaptor.StreamSubagentEnd, "subagent.end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.got) != tc.want {
				t.Errorf("%s = %q; want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestSubagentRefZeroValueIsSafe verifies that a zero-value SubagentRef is
// safe to read: all fields are empty strings and the struct is comparable.
func TestSubagentRefZeroValueIsSafe(t *testing.T) {
	t.Parallel()
	var ref agentadaptor.SubagentRef
	if ref.ID != "" || ref.ParentID != "" || ref.Name != "" ||
		ref.Kind != "" || ref.Protocol != "" || ref.ToolCallID != "" {
		t.Errorf("zero-value SubagentRef has unexpected non-empty fields: %+v", ref)
	}
}

// TestStreamPayloadSubagentFieldNilByDefault verifies that a zero-value
// StreamPayload has Subagent == nil, preserving backward compatibility for
// all pre-existing payloads that omit the field.
func TestStreamPayloadSubagentFieldNilByDefault(t *testing.T) {
	t.Parallel()
	var p agentadaptor.StreamPayload
	if p.Subagent != nil {
		t.Errorf("zero-value StreamPayload.Subagent must be nil; got %+v", p.Subagent)
	}
}

// TestStreamPayloadSubagentFieldPassthrough verifies that a SubagentRef
// assigned to StreamPayload.Subagent is retrieved without mutation.
func TestStreamPayloadSubagentFieldPassthrough(t *testing.T) {
	t.Parallel()
	ref := &agentadaptor.SubagentRef{
		ID:         "child-abc",
		ParentID:   "",
		Name:       "readme-worker",
		Kind:       "native",
		Protocol:   "",
		ToolCallID: "call_xyz",
	}
	p := agentadaptor.StreamPayload{
		Kind:     agentadaptor.StreamSubagentStart,
		Subagent: ref,
	}
	if p.Subagent == nil {
		t.Fatal("Subagent is nil after assignment")
	}
	if p.Subagent.ID != "child-abc" {
		t.Errorf("ID: got %q want %q", p.Subagent.ID, "child-abc")
	}
	if p.Subagent.Name != "readme-worker" {
		t.Errorf("Name: got %q want %q", p.Subagent.Name, "readme-worker")
	}
	if p.Subagent.Kind != "native" {
		t.Errorf("Kind: got %q want %q", p.Subagent.Kind, "native")
	}
	if p.Subagent.ToolCallID != "call_xyz" {
		t.Errorf("ToolCallID: got %q want %q", p.Subagent.ToolCallID, "call_xyz")
	}
}

// TestStreamCapabilitySubagentFieldsZeroByDefault verifies that the four new
// StreamCapability subagent fields are false (zero value) by default, so
// existing adapters that do not fill them stay truthfully at "not supported".
func TestStreamCapabilitySubagentFieldsZeroByDefault(t *testing.T) {
	t.Parallel()
	var cap agentadaptor.StreamCapability
	if cap.Subagents {
		t.Error("StreamCapability.Subagents must default to false")
	}
	if cap.SubagentNesting {
		t.Error("StreamCapability.SubagentNesting must default to false")
	}
	if cap.SubagentToolLinkage {
		t.Error("StreamCapability.SubagentToolLinkage must default to false")
	}
	if cap.SubagentTextDelta {
		t.Error("StreamCapability.SubagentTextDelta must default to false")
	}
}

// TestStreamCapabilitySubagentFieldsAssignment verifies that the four new
// StreamCapability subagent fields can be set and read back independently.
func TestStreamCapabilitySubagentFieldsAssignment(t *testing.T) {
	t.Parallel()
	cap := agentadaptor.StreamCapability{
		Subagents:           true,
		SubagentNesting:     true,
		SubagentToolLinkage: true,
		SubagentTextDelta:   false, // honestly false; not all providers have token delta
	}
	if !cap.Subagents {
		t.Error("Subagents: expected true")
	}
	if !cap.SubagentNesting {
		t.Error("SubagentNesting: expected true")
	}
	if !cap.SubagentToolLinkage {
		t.Error("SubagentToolLinkage: expected true")
	}
	if cap.SubagentTextDelta {
		t.Error("SubagentTextDelta: expected false")
	}
}

// TestSubagentRefNilIsRootScope verifies that a nil *SubagentRef is the
// canonical signal for "parent/root scope", as stated in the workstream spec.
func TestSubagentRefNilIsRootScope(t *testing.T) {
	t.Parallel()
	p := agentadaptor.StreamPayload{
		Kind:     agentadaptor.StreamTextContent,
		Delta:    "hello",
		Subagent: nil, // root scope — adapter convention
	}
	isRootScope := p.Subagent == nil
	if !isRootScope {
		t.Error("nil Subagent should denote root scope")
	}
}

// TestSubagentStartEndLifecyclePayloads verifies that a start/end lifecycle
// pair can be constructed with the required fields and survives a round-trip
// through a slice (simulating a channel buffer), with no mutation of the
// SubagentRef pointer.
func TestSubagentStartEndLifecyclePayloads(t *testing.T) {
	t.Parallel()
	ref := &agentadaptor.SubagentRef{
		ID:       "agent-7f3a",
		Name:     "impl-worker",
		Kind:     "native",
		Protocol: "",
	}
	start := agentadaptor.StreamPayload{
		Kind:     agentadaptor.StreamSubagentStart,
		RunID:    "run-001",
		Subagent: ref,
	}
	end := agentadaptor.StreamPayload{
		Kind:     agentadaptor.StreamSubagentEnd,
		RunID:    "run-001",
		Subagent: ref,
		Result:   map[string]any{"status": "completed"},
	}

	payloads := []agentadaptor.StreamPayload{start, end}

	if payloads[0].Kind != agentadaptor.StreamSubagentStart {
		t.Errorf("first payload kind: got %q want %q", payloads[0].Kind, agentadaptor.StreamSubagentStart)
	}
	if payloads[1].Kind != agentadaptor.StreamSubagentEnd {
		t.Errorf("second payload kind: got %q want %q", payloads[1].Kind, agentadaptor.StreamSubagentEnd)
	}
	for i, p := range payloads {
		if p.Subagent == nil {
			t.Errorf("payload[%d].Subagent is nil", i)
			continue
		}
		if p.Subagent.ID != "agent-7f3a" {
			t.Errorf("payload[%d].Subagent.ID: got %q want %q", i, p.Subagent.ID, "agent-7f3a")
		}
	}
	status, _ := payloads[1].Result["status"].(string)
	if status != "completed" {
		t.Errorf("end payload Result[status]: got %q want %q", status, "completed")
	}
}

// TestSubagentRefDelegatedProtocol verifies the A2A delegation shape:
// Kind="delegated", Protocol="a2a".
func TestSubagentRefDelegatedProtocol(t *testing.T) {
	t.Parallel()
	ref := &agentadaptor.SubagentRef{
		ID:         "delegation-99",
		Name:       "review-agent",
		Kind:       "delegated",
		Protocol:   "a2a",
		ToolCallID: "call_a2a_abc",
	}
	p := agentadaptor.StreamPayload{
		Kind:     agentadaptor.StreamSubagentStart,
		Subagent: ref,
	}
	if p.Subagent.Kind != "delegated" {
		t.Errorf("Kind: got %q want delegated", p.Subagent.Kind)
	}
	if p.Subagent.Protocol != "a2a" {
		t.Errorf("Protocol: got %q want a2a", p.Subagent.Protocol)
	}
}

// TestExistingStreamPayloadFieldsUnchanged verifies that none of the
// pre-existing StreamPayload fields are missing after the Phase 0 addition.
func TestExistingStreamPayloadFieldsUnchanged(t *testing.T) {
	t.Parallel()
	// Construct a payload using every pre-existing field to ensure compilation
	// still succeeds; this guards against accidental removals.
	p := agentadaptor.StreamPayload{
		Kind:       agentadaptor.StreamTextContent,
		Sequence:   1,
		Seq:        1,
		RunID:      "run-x",
		ThreadID:   "thread-x",
		TurnID:     "turn-x",
		MessageID:  "msg-x",
		ToolCallID: "call-x",
		Name:       "some-tool",
		Delta:      "chunk",
		Args:       map[string]any{"k": "v"},
		Result:     map[string]any{"r": "v"},
		Role:       agentadaptor.RoleAssistant,
		Raw:        map[string]any{"extra": true},
		Subagent:   nil, // root scope
	}
	if p.Kind != agentadaptor.StreamTextContent {
		t.Errorf("Kind field changed: %v", p.Kind)
	}
}

func TestCoreDoesNotImportA2ADelegation(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(currentFile)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read repository root: %v", err)
	}

	const forbidden = "github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
				t.Fatalf("core file %s imports A2A delegation package %s", path, importPath)
			}
		}
	}
}
