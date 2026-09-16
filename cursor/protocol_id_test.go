package cursor

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestCursorOfficialCompositeIDsAndQualifiedToolNames(t *testing.T) {
	body, err := os.ReadFile("testdata/live-20260723-capabilities.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	req := driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "agent-adaptor-tools"}}}, ProfilePayload: driver.ProfilePayload{Agents: driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "alignment/planner", RuntimeName: "alignment-planner"}}}}}
	for _, streaming := range []bool{false, true} {
		req.Streaming = streaming
		p, s := alignmentCursorParse(t, req, string(body), false)
		facts := s.facts()
		if len(facts) != 4 {
			t.Fatalf("facts=%+v", facts)
		}
		for i, rawID := range []string{"call_MCP\nfc_MCP", "call_TASK\nfc_TASK"} {
			want := cursorEncodedCallIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(rawID))
			if facts[2*i].InvocationID != want || facts[2*i+1].InvocationID != want || facts[2*i].Phase != capability.Started || facts[2*i+1].Phase != capability.Completed {
				t.Fatal("composite lifecycle not correlated")
			}
			for _, item := range p.transcript {
				if item.Kind == driver.TranscriptToolCall || item.Kind == driver.TranscriptToolResult {
					if item.ToolUseID != facts[0].InvocationID && item.ToolUseID != facts[2].InvocationID {
						t.Fatal("transcript and capability IDs diverged")
					}
				}
			}
		}
		if facts[0].Ref.Key != "agent-adaptor-tools" || facts[0].Ref.Operation != "alignment_probe" || facts[2].Ref.Key != "alignment/planner" {
			t.Fatal("qualified label replaced independent identity fields")
		}
	}
}

func TestCursorOpaqueIDEncodingHasReservedDomainAndBounds(t *testing.T) {
	original := "call_first\nfc_second"
	encoded, ok := cursorProtocolCallID(original)
	if !ok {
		t.Fatal("official ID rejected")
	}
	literal, ok := cursorProtocolCallID(encoded)
	if !ok || literal == encoded {
		t.Fatal("encoded ID collides with literal provider ID")
	}
	for _, opaque := range []string{"call_x\nfc_y\nfc_z", "call_x\x00fc_y", "call_x\r\nfc_y", "unknown\nfc_y", "call_x\nunknown", "\t"} {
		encoded, ok := cursorProtocolCallID(opaque)
		if !ok || !strings.HasPrefix(encoded, cursorEncodedCallIDPrefix) {
			t.Fatalf("opaque ID rejected %q", opaque)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, cursorEncodedCallIDPrefix))
		if err != nil || string(decoded) != opaque {
			t.Fatal("opaque ID was changed")
		}
	}
	for _, bad := range []string{"", string([]byte{0xff}), cursorEncodedCallIDPrefix + strings.Repeat("x", 1400)} {
		if _, ok := cursorProtocolCallID(bad); ok {
			t.Fatal("unbounded or invalid ID accepted")
		}
	}
	safe := strings.Repeat("a", 2048)
	if got, ok := cursorProtocolCallID(safe); !ok || got != safe {
		t.Fatal("changed existing valid safe ID")
	}
}

func TestCursorQualifiedOperationRedundancyIsExact(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		valid      bool
	}{
		{"current", `{"serverIdentifier":"知识__库","providerIdentifier":"知识__库","toolName":"read","name":"知识__库-read"}`, true},
		{"missing old alias", `{"serverIdentifier":"知识__库","toolName":"read","name":"知识__库-read"}`, true},
		{"wrong provider", `{"serverIdentifier":"知识__库","providerIdentifier":"other","toolName":"read","name":"知识__库-read"}`, false},
		{"wrong server", `{"serverIdentifier":"other","toolName":"read","name":"知识__库-read"}`, false},
		{"wrong label", `{"serverIdentifier":"知识__库","toolName":"read","name":"other-read"}`, false},
		{"empty operation", `{"serverIdentifier":"知识__库","toolName":"","name":"知识__库-read"}`, false},
		{"null operation", `{"serverIdentifier":"知识__库","toolName":null,"name":"知识__库-read"}`, false},
		{"numeric operation", `{"serverIdentifier":"知识__库","toolName":17,"name":"知识__库-read"}`, false},
		{"null name", `{"serverIdentifier":"知识__库","toolName":"read","name":null}`, false},
		{"invalid old alias", `{"serverIdentifier":"知识__库","providerIdentifier":17,"toolName":"read","name":"知识__库-read"}`, false},
		{"no authoritative server", `{"providerIdentifier":"知识__库","toolName":"read","name":"知识__库-read"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := alignmentCursorFrame("one", "started", "mcpToolCall", tc.args, "") + alignmentCursorFrame("one", "completed", "mcpToolCall", tc.args, `{"success":{}}`) + alignmentCursorTerminal
			_, s := alignmentCursorParse(t, alignmentCursorCatalog(), body, false)
			if got := len(s.facts()) == 2; got != tc.valid {
				t.Fatalf("facts=%+v", s.facts())
			}
		})
	}
}
