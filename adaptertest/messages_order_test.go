package adaptertest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// Deliberately violate the SPI by reversing an owned batch before presenting
// it to the reference fake provider. The original port still records the
// real admission/claim order; provider Evidence independently records reversal.
type reverseMessageDriver struct{ driver.Driver }
type reverseMessagePort struct{ driver.MessagePort }

func (p reverseMessagePort) Claim(b driver.Boundary) []driver.MessageItem {
	items := p.MessagePort.Claim(b)
	slices.Reverse(items)
	return items
}
func (d reverseMessageDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if req.Messages != nil {
		req.Messages = reverseMessagePort{req.Messages}
	}
	return d.Driver.Run(ctx, req, sink)
}

func TestMessageSuiteRejectsReorderedProviderInput(t *testing.T) {
	// Run the negative conformance suite in a child test process: a failing
	// t.Run marks its parent failed even when its boolean result is expected.
	const helperEnv = "ADAPTORTEST_REVERSE_MESSAGE_HELPER"
	if os.Getenv(helperEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMessageSuiteRejectsReorderedProviderInput$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		output, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || ctx.Err() != nil {
			t.Fatalf("broken Driver suite must fail: err=%v ctx=%v\n%s", err, ctx.Err(), output)
		}
		for _, name := range []string{"full_MSG_matrix/broken_reverse/success_batch_immediate", "full_MSG_matrix/broken_reverse/success_batch_tool", "full_MSG_matrix/broken_reverse/success_batch_stop", "two_message_success"} {
			if !strings.Contains(string(output), "--- FAIL: TestMessageSuiteRejectsReorderedProviderInput/"+name) {
				t.Errorf("broken Driver was not rejected in %s:\n%s", name, output)
			}
		}
		if !strings.Contains(string(output), "MSG-04:") || !strings.Contains(string(output), "provider input is out of claim order") {
			t.Fatalf("missing ordering violation:\n%s", output)
		}
		return
	}
	var evidence func() MessageEvidence
	factory := func(t *testing.T, scenario MessageScenario) MessageFixture {
		f := NewReferenceMessageFixture(t, scenario)
		f.Driver = reverseMessageDriver{f.Driver}
		evidence = f.Evidence
		return f
	}
	probe := factory(t, MessageScenario{Name: "probe"})
	cfg := &suiteConfig{}
	WithMessageFixture("broken_reverse", factory)(cfg)
	matrixAccepted := t.Run("full_MSG_matrix", func(t *testing.T) { checkMessages(t, probe.Driver, cfg) })
	scenario := MessageScenario{Name: "success", Boundary: driver.BoundaryImmediate, Items: []driver.MessageItem{
		{ID: "first", Text: "[human first] first guidance"},
		{ID: "second", Text: "[human second] second guidance"},
	}}
	batchAccepted := t.Run("two_message_success", func(t *testing.T) {
		runMessageScenario(t, namedMessageFixture{"reverse", factory}, referenceMessageCapability(), scenario)
	})
	if matrixAccepted && batchAccepted {
		t.Fatalf("MSG-01 through MSG-10 accepted deliberately broken reverse-delivery Driver, including a two-message success batch; independently observed provider input=%q", evidence().Received)
	}
}

func TestVerifyMessagePortProviderOrder(t *testing.T) {
	first := driver.MessageItem{ID: "first", Text: "[human first] earlier guidance"}
	second := driver.MessageItem{ID: "second", Text: "[human second] later guidance"}
	cases := []struct {
		name        string
		received    []string
		separate    bool
		sameText    bool
		reverseEcho bool
		repeatEcho  bool
		clause      string
	}{
		{name: "separate_payloads", received: []string{first.Text, second.Text}},
		{name: "confirmation_order_is_independent", received: []string{first.Text, second.Text}, reverseEcho: true},
		{name: "extra_confirmation_does_not_repeat_input", received: []string{first.Text, second.Text}, repeatEcho: true},
		{name: "combined_payload", received: []string{first.Text + "\n\n" + second.Text}},
		{name: "separate_claims", received: []string{first.Text, second.Text}, separate: true},
		{name: "reversed_payloads", received: []string{second.Text, first.Text}, clause: "MSG-04"},
		{name: "reversed_combined_payload", received: []string{second.Text + "\n\n" + first.Text}, clause: "MSG-04"},
		{name: "reversed_across_claims", received: []string{second.Text, first.Text}, separate: true, clause: "MSG-04"},
		{name: "later_duplicate_cannot_hide_reordering", received: []string{second.Text, first.Text, second.Text}, clause: "MSG-04"},
		{name: "identical_text_requires_two_receipts", received: []string{first.Text}, sameText: true, clause: "MSG-04"},
		{name: "identical_text_two_receipts", received: []string{first.Text, first.Text}, sameText: true},
		{name: "identical_text_combined_receipts", received: []string{first.Text + "\n\n" + first.Text}, sameText: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := []driver.MessageItem{first, second}
			if tc.sameText {
				items[1].Text = first.Text
			}
			batches := []MessageBatch{{Boundary: driver.BoundaryTool, Items: items}}
			if tc.separate {
				batches = []MessageBatch{{Boundary: driver.BoundaryTool, Items: items[:1]}, {Boundary: driver.BoundaryTool, Items: items[1:]}}
			}
			p := NewScriptedMessagePort(referenceMessageCapability(), batches...)
			p.Window(true)
			for range batches {
				p.Claim(driver.BoundaryTool)
			}
			// Settlement order is independent of provider delivery order.
			p.Ack(second.ID, driver.MessageAck{Delivered: true, Mechanism: "fixture"})
			p.Ack(first.ID, driver.MessageAck{Delivered: true, Mechanism: "fixture"})
			p.Window(false)
			p.Finish()
			confirmed := slices.Clone(tc.received)
			if tc.reverseEcho {
				slices.Reverse(confirmed)
			}
			if tc.repeatEcho {
				confirmed = append(confirmed, first.Text)
			}
			vs := p.Verify(MessageEvidence{Received: tc.received, Confirmed: confirmed})
			if tc.clause == "" && len(vs) != 0 || tc.clause != "" && !clauseSet(vs)[tc.clause] {
				t.Fatalf("want clause %q, got %v", tc.clause, vs)
			}
		})
	}
}
