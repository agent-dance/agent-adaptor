package codebuddy

import (
	"context"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"testing"
)

func TestAlignmentReviewT15PartialParentMustNotSupplyRootArgs(t *testing.T) {
	for _, tc := range []struct {
		name, parent string
		want         int
	}{{"root", "null", 1}, {"foreign", `"unproved-parent"`, 0}, {"malformed", "42", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(context.Background(), alignmentObservationRequest())
			p.enableStreaming(p.runID)
			alignmentFeed(t, p,
				`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"root-call","name":"Skill","input":{}}}}`,
				`{"type":"stream_event","parent_tool_use_id":`+tc.parent+`,"event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\" review \"}"}}}`,
				`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_stop","index":0}}`)
			facts := alignmentCapabilities(rec)
			t.Logf("facts=%+v", facts)
			if len(facts) != tc.want {
				t.Fatalf("unproved partial parent supplied root capability arguments: got %d want %d", len(facts), tc.want)
			}
			var argsObserved bool
			for _, event := range rec.StreamSnapshot() {
				if event.Kind == driver.StreamToolCallArgs && event.Delta == `{"command":" review "}` {
					argsObserved = true
				}
			}
			if !argsObserved {
				t.Fatal("original argument delta was discarded")
			}
			alignmentFeed(t, p, alignmentCall("Skill", "root-call", `{"command":" review "}`), alignmentResult("root-call", `"ok"`, false, ""))
			if got := len(alignmentCapabilities(rec)); got != tc.want*2 {
				t.Fatalf("full wrapper bypassed unproved partial attribution: got %d want %d", got, tc.want*2)
			}
		})
	}
}
