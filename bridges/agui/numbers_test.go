package agui

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
)

// Exercise every numeric projection consumer, not only its shared converter.
func TestNumericProjectionConsumers(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int64
		valid bool
	}{
		{int(2), 2, true}, {uint(2), 2, true}, {int64(2), 2, true}, {float64(2), 2, true},
		{json.Number("2"), 2, true}, {json.Number("2.0"), 2, true}, {json.Number("2e0"), 2, true},
		{json.Number("9007199254740993"), 9007199254740993, true},
		{json.Number("2.00000000000000000001"), 0, false}, {float64(2.5), 0, false},
		{json.Number("9223372036854775808"), 0, false}, {uint64(1 << 63), 0, false},
	} {
		t.Run(fmt.Sprintf("%T/%v", tc.value, tc.value), func(t *testing.T) {
			if got, valid := exitCode(map[string]any{"exit_code": tc.value}); got != tc.want || valid != tc.valid {
				t.Errorf("exitCode = %d, %v; want %d, %v", got, valid, tc.want, tc.valid)
			}
			for _, kind := range []string{adaptor.NoticeApprovalRequested, adaptor.NoticeApprovalResolved} {
				tr := NewEventTranslator()
				tr.Translate(adaptor.RunStarted{})
				projected := tr.Translate(adaptor.Notice{Kind: kind, Data: map[string]any{
					"request_id": "q", "kind": "permission", "attempt": tc.value, "latency": tc.value,
				}})
				found := 0
				for _, ev := range projected {
					var raw string
					switch ev := ev.(type) {
					case *events.ToolCallArgsEvent:
						raw = ev.Delta
					case *events.ToolCallResultEvent:
						raw = ev.Content
					default:
						continue
					}
					found++
					var payload struct {
						Attempt int   `json:"retry_attempt"`
						Latency int64 `json:"latency_ms"`
					}
					if err := json.Unmarshal([]byte(raw), &payload); err != nil {
						t.Fatal(err)
					}
					wantAttempt := int(tc.want)
					if int64(wantAttempt) != tc.want {
						wantAttempt = 0
					}
					if payload.Attempt != intFromAny(tc.value) || payload.Attempt != wantAttempt ||
						payload.Latency != time.Duration(tc.want).Milliseconds() && kind == adaptor.NoticeApprovalResolved {
						t.Errorf("%s payload = %s; want attempt %d, latency %d", kind, raw, tc.want, time.Duration(tc.want).Milliseconds())
					}
				}
				if found != 1 {
					t.Fatalf("%s numeric projections = %d", kind, found)
				}
			}
		})
	}
}
