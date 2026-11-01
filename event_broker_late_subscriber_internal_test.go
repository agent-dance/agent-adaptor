package adaptor

import (
	"context"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// A host typically calls Send, prepares its transport (for example HTTP
// headers), and only then ranges over Events. A fast execution can finish in
// between; the late subscriber must still observe its ordinary events.
func TestLateSubscriberObservesFinishedExecution(t *testing.T) {
	d := reviewDriver{run: func(_ context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: "m"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m", Delta: "a"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: "m", Delta: "b"})
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: "m"})
		return driver.Response{Output: "ab"}, nil
	}}
	st := New(d).Send(context.Background(), "x").(*runStream)
	<-st.done // finalized (Done published, sink closed) before anyone subscribed
	var text string
	var dropped, done int
	for ev := range st.Events() {
		switch e := ev.(type) {
		case TextDelta:
			if e.Phase == PhaseContent {
				text += e.Text
			}
		case Dropped:
			dropped++
		case Done:
			done++
		}
	}
	if text != "ab" || dropped != 0 || done != 1 {
		t.Fatalf("late subscriber text=%q dropped=%d done=%d", text, dropped, done)
	}
}
