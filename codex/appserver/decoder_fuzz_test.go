package appserver

import (
	"bytes"
	"testing"
)

var fuzzNotificationMethods = [...]string{
	NotifyThreadStarted,
	NotifyThreadStatusChanged,
	NotifyThreadTokenUsageUpdated,
	NotifyTurnStarted,
	NotifyTurnCompleted,
	NotifyItemStarted,
	NotifyItemCompleted,
	NotifyItemAgentMessageDelta,
	NotifyItemReasoningTextDelta,
	NotifyItemReasoningSummaryTextDelta,
	NotifyItemReasoningSummaryPartAdded,
	NotifyItemCommandExecutionOutputDelta,
	NotifyCommandExecOutputDelta,
	NotifyItemFileChangeOutputDelta,
	NotifyItemPlanDelta,
	NotifyError,
	"provider/futureNotification",
}

// FuzzCodexAppserverNotificationDecoder exercises the complete pure
// notification-to-run-state path without starting a process or using the
// network. In addition to totality over arbitrary payloads, it locks the
// app-server checkpoint gate to the process outcome.
func FuzzCodexAppserverNotificationDecoder(f *testing.F) {
	f.Add(uint8(0), []byte(`{"thread":{"id":"seed-thread"}}`), 1)
	f.Add(uint8(3), []byte(`{"threadId":"seed-thread","turn":{"id":"seed-turn","status":"inProgress"}}`), -1)
	f.Add(uint8(4), []byte(`{"threadId":"seed-thread","turn":{"id":"seed-turn","status":"completed","usage":{"inputTokens":1,"outputTokens":0}}}`), 2)
	f.Add(uint8(5), []byte(`{"threadId":"seed-thread","turnId":"seed-turn","item":{"id":"seed-item","type":"agentMessage","text":"hello"}}`), 255)
	f.Add(uint8(15), []byte(`{"error":{"message":"retry"},"willRetry":true}`), 1)
	f.Add(uint8(16), []byte(nil), -1)

	f.Fuzz(func(t *testing.T, selector uint8, params []byte, exitCode int) {
		if exitCode == 0 {
			exitCode = 1
		}
		method := fuzzNotificationMethods[int(selector)%len(fuzzNotificationMethods)]
		state := newRunState("fuzz-run", nil)
		state.onNotification(method, params)

		clean := state.snapshot(Options{}, "seed-thread", "", "", 0, "", false)
		if checkpoint := clean.Checkpoint; checkpoint != nil && (!checkpoint.Valid || checkpoint.State == nil || checkpoint.State.ResumeID == "") {
			t.Fatalf("decoder produced malformed checkpoint %#v", checkpoint)
		}
		failed := state.snapshot(Options{}, "seed-thread", "", "", exitCode, "", false)
		if checkpoint := failed.Checkpoint; checkpoint != nil {
			t.Fatalf("non-zero exit %d produced checkpoint %#v for %q", exitCode, checkpoint, method)
		}

		poisoned := newRunState("fuzz-poisoned", nil)
		poisoned.onNotification(NotifyThreadStarted, []byte("{"))
		poisoned.onNotification(method, params)
		poisonedResult := poisoned.snapshot(Options{}, "seed-thread", "", "", 0, "", false)
		if checkpoint := poisonedResult.Checkpoint; checkpoint != nil {
			t.Fatalf("malformed protocol produced checkpoint %#v for %q", checkpoint, method)
		}
	})
}

// FuzzDecodeThreadItem covers every currently supported tagged-union variant
// plus unknown and malformed input. Successful decodes must retain an exact
// copy of the official raw item for forward-compatible protocol auditing.
func FuzzDecodeThreadItem(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"id":"i1","type":"agentMessage","text":"hello","phase":"final_answer"}`),
		[]byte(`{"id":"i2","type":"reasoning","content":["step"],"summary":["ok"]}`),
		[]byte(`{"id":"i3","type":"commandExecution","command":"go test ./...","status":"completed","exitCode":0}`),
		[]byte(`{"id":"i4","type":"fileChange","status":"completed","changes":[{"path":"a.txt","diff":"+a"}]}`),
		[]byte(`{"id":"i5","type":"mcpToolCall","server":"s","tool":"t","status":"completed","arguments":{"q":"x"}}`),
		[]byte(`{"id":"i6","type":"webSearch","query":"agent adaptor"}`),
		[]byte(`{"id":"i7","type":"dynamicToolCall","tool":"t","status":"completed"}`),
		[]byte(`{"id":"i8","type":"providerFutureItem","future":true}`),
		[]byte(`{"id":`),
		nil,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		item, err := DecodeThreadItem(raw)
		if err != nil {
			if item != nil {
				t.Fatalf("failed decode returned partial item %#v", item)
			}
			return
		}
		if item == nil {
			t.Fatal("successful decode returned nil item")
		}
		if !bytes.Equal(item.Raw, raw) {
			t.Fatalf("raw payload changed: got %q want %q", item.Raw, raw)
		}
	})
}
