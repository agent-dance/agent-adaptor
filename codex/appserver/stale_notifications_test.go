package appserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestF2LateUsageKeepsTurnAttribution(t *testing.T) {
	port := newMessageTestPort()
	port.ends = [][]driver.MessageItem{{{ID: "next", Text: "next"}}}
	state, rpc, sink := setupMessageLoop(port)
	rpc.start = func(n int) {
		if n == 1 {
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-1", 102, 2))
			rpc.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn-1", "status": "completed"}})
			// These arrive after the official terminal and before the next turn ID
			// is available. The newer snapshot belongs to turn 1, the replay cannot
			// decrement it, and the other thread must not affect either baseline.
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-1", 105, 3))
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-1", 103, 1))
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("another-thread", "turn-1", 9000, 9000))
		} else {
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "old-resumed-turn", 100, 8))
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-2", 109, 4))
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-1", 105, 3))
			rpc.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn-2", "status": "completed"}})
		}
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: port}, "thread"); err != nil {
		t.Fatal(err)
	}
	if state.usageTurns["turn-1"].usage.InputTokens != 5 || state.usageTurns["turn-2"].usage.InputTokens != 4 {
		t.Fatalf("wrong attribution: %+v", state.usageTurns)
	}
	result := state.snapshot(Options{}, "thread", "audit", "", 0, "", false)
	if result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 9 {
		t.Fatalf("aggregate usage=%+v", result.Usage)
	}
	turns := completedTurns(sink)
	if len(turns) != 2 || turns[0].Usage.InputTokens != 2 || turns[1].Usage.InputTokens != 4 {
		t.Fatalf("completion snapshots=%+v", turns)
	}
}

func TestF2NotificationScopeStillRejectsInvalidProtocol(t *testing.T) {
	for _, raw := range []string{
		`{"threadId":"thread","tokenUsage":{"last":{},"total":{}}}`,
		`{"threadId":"thread","turnId":"turn","tokenUsage":{"last":{"inputTokens":-1},"total":{"inputTokens":1}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			state := newRunState("run", &recordingSink{})
			state.setThread("thread")
			state.setTurn("turn")
			state.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(raw))
			if state.protocolError() == nil {
				t.Fatal("invalid protocol was accepted")
			}
			if state.snapshot(Options{}, "thread", "", "", 0, "", false).Checkpoint != nil {
				t.Fatal("invalid protocol produced checkpoint")
			}
		})
	}
	state := newRunState("run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	done := json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)
	state.onNotification(NotifyTurnCompleted, done)
	state.onNotification(NotifyTurnCompleted, done)
	if state.protocolError() == nil {
		t.Fatal("duplicate terminal for current turn accepted")
	}
}

func usageFixture(thread, turn string, total, last int) map[string]any {
	return map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{
		"last": map[string]any{"inputTokens": last, "outputTokens": last}, "total": map[string]any{"inputTokens": total, "outputTokens": total},
	}}
}

func TestF2MixedTerminalAndCumulativeUsage(t *testing.T) {
	port := newMessageTestPort()
	port.ends = [][]driver.MessageItem{{{ID: "next", Text: "next"}}}
	state, rpc, _ := setupMessageLoop(port)
	rpc.start = func(n int) {
		if n == 1 {
			rpc.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn-1", "status": "completed", "usage": map[string]any{"inputTokens": 2, "outputTokens": 2}}})
		} else {
			rpc.notify(NotifyThreadTokenUsageUpdated, usageFixture("thread", "turn-2", 105, 3))
			rpc.notify(NotifyTurnCompleted, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn-2", "status": "completed"}})
		}
	}
	if err := runTurns(context.Background(), rpc, state, Options{Messages: port}, "thread"); err != nil {
		t.Fatal(err)
	}
	if usage := state.snapshot(Options{}, "thread", "", "", 0, "", false).Usage; usage.InputTokens != 5 || usage.OutputTokens != 5 {
		t.Fatalf("mixed usage double counted or lost: %+v", usage)
	}
}

func TestF2ResidentIdleUsageBaseline(t *testing.T) {
	process := &Process{threadID: "thread"}
	// An old-turn update received while idle is history for the next execution.
	historical, _ := json.Marshal(usageFixture("thread", "previous-run", 107, 7))
	process.onNotification(NotifyThreadTokenUsageUpdated, historical)
	foreign, _ := json.Marshal(usageFixture("another-thread", "foreign", 9000, 9000))
	process.onNotification(NotifyThreadTokenUsageUpdated, foreign)
	state := newRunState("next-run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	state.usageBaseline = cloneUsage(process.usageTotal)
	state.historicalUsage = cloneUsage(process.usageTotal)
	process.state = state
	current, _ := json.Marshal(usageFixture("thread", "turn", 111, 4))
	process.onNotification(NotifyThreadTokenUsageUpdated, current)
	process.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	result := state.snapshot(Options{}, "thread", "", "", 0, "", false)
	if result.Usage.InputTokens != 4 {
		t.Fatalf("prior execution counted again: %+v", result.Usage)
	}
}

// Port of upstream 6432e5e / PR #23: restore arrives after thread/resume,
// before either RPC identity is bound. v2 excludes resumed history from Usage.
func TestF2PR23RestoredUsageBeforeRPCIdentity(t *testing.T) {
	sink := &recordingSink{}
	state := newRunState("run-resumed-usage", sink)
	state.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(`{"threadId":"thread-resumed","turnId":"turn-previous","tokenUsage":{"last":{"inputTokens":1,"outputTokens":2,"cachedInputTokens":0,"reasoningOutputTokens":0},"total":{"inputTokens":101,"outputTokens":202,"cachedInputTokens":3,"reasoningOutputTokens":0}}}`))
	state.setThread("thread-resumed")
	state.setTurn("turn-current")
	if err := state.protocolError(); err != nil {
		t.Fatalf("restored usage caused protocol failure: %v", err)
	}
	if usage := state.snapshot(Options{}, "thread-resumed", "", "", 0, "", false).Usage; usage != nil {
		t.Fatalf("restored usage contaminated execution: %+v", usage)
	}
	for _, payload := range sink.streams {
		if payload.Name == NotifyThreadTokenUsageUpdated {
			t.Fatalf("historical usage escaped: %+v", payload)
		}
	}
	state.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(`{"threadId":"thread-resumed","turnId":"turn-current","tokenUsage":{"last":{"inputTokens":4,"outputTokens":5,"cachedInputTokens":1,"reasoningOutputTokens":0},"total":{"inputTokens":14,"outputTokens":25,"cachedInputTokens":6,"reasoningOutputTokens":0}}}`))
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-resumed","turn":{"id":"turn-current","status":"completed"}}`))
	result := state.snapshot(Options{}, "thread-resumed", "", "", 0, "", false)
	if result.Usage == nil || result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 5 || result.Usage.CachedInputTokens != 1 || result.Checkpoint == nil || !result.Checkpoint.Valid {
		t.Fatalf("resumed execution accounting/checkpoint: %+v", result)
	}
}

func TestResumeRetainsUsageReplayedBeforeRPCResponse(t *testing.T) {
	testUsageBaselineFixture(t, "resume-stale-usage-baseline", "thread-fake")
}

func TestFreshThreadCountsAllObservedInferenceUsage(t *testing.T) {
	testUsageBaselineFixture(t, "fresh-usage-baseline", "")
}

func testUsageBaselineFixture(t *testing.T, scenario, resumeID string) {
	t.Helper()
	fake := buildFakeAppserver(t)
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "resident"}[resident], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			opts := Options{Command: fake, ResumeThreadID: resumeID, Env: []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}}}
			sink := &recordingSink{}
			var result driver.Response
			var err error
			if resident {
				process, openErr := Open(ctx, opts, sink)
				if openErr != nil {
					t.Fatal(openErr)
				}
				t.Cleanup(func() { closeUsageTestProcess(t, process) })
				result, _, err = process.RunTurn(ctx, opts, sink)
			} else {
				result, err = Run(ctx, opts, sink)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Five tokens belong to this execution even though Last describes
			// only its final two-token inference. A fresh thread starts at zero;
			// a resumed thread excludes the replayed historical total.
			if result.Usage == nil || result.Usage.InputTokens != 5 || result.Usage.OutputTokens != 5 || result.Checkpoint == nil {
				t.Fatalf("resume usage/checkpoint = %+v / %+v", result.Usage, result.Checkpoint)
			}
		})
	}
}

func TestLateHistoricalUsageCorrectsEstimatedBaseline(t *testing.T) {
	state := newRunState("run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	current, _ := json.Marshal(usageFixture("thread", "turn", 105, 2))
	state.onNotification(NotifyThreadTokenUsageUpdated, current)
	historical, _ := json.Marshal(usageFixture("thread", "previous-run", 100, 7))
	state.onNotification(NotifyThreadTokenUsageUpdated, historical)
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	if usage := state.snapshot(Options{}, "thread", "", "", 0, "", false).Usage; usage == nil || usage.InputTokens != 5 || usage.OutputTokens != 5 {
		t.Fatalf("late history did not correct estimated usage: %+v", usage)
	}
}

func TestLateHistoricalUsagePreservesResidentBaseline(t *testing.T) {
	state := newRunState("run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	state.usageBaseline = &driver.Usage{InputTokens: 107, OutputTokens: 107}
	state.historicalUsage = cloneUsage(state.usageBaseline)
	for _, frame := range []map[string]any{
		usageFixture("thread", "turn", 109, 2),
		usageFixture("thread", "previous-run", 100, 7),
		usageFixture("thread", "turn", 111, 2),
	} {
		raw, _ := json.Marshal(frame)
		state.onNotification(NotifyThreadTokenUsageUpdated, raw)
	}
	state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	if usage := state.snapshot(Options{}, "thread", "", "", 0, "", false).Usage; usage == nil || usage.InputTokens != 4 || usage.OutputTokens != 4 {
		t.Fatalf("old replay rolled resident history backwards: %+v", usage)
	}
}

func TestFreshThreadIgnoresUnknownTurnUsage(t *testing.T) {
	state := newRunState("run", &recordingSink{})
	state.setThread("thread")
	state.setTurn("turn")
	state.freshThread = true
	state.usageBaseline = &driver.Usage{}
	state.historicalUsage = &driver.Usage{}
	process := &Process{threadID: "thread", freshThread: true, usageTotal: &driver.Usage{}, state: state}
	for _, frame := range []map[string]any{
		usageFixture("thread", "unknown-turn", 100, 7),
		usageFixture("thread", "turn", 5, 2),
	} {
		raw, _ := json.Marshal(frame)
		process.onNotification(NotifyThreadTokenUsageUpdated, raw)
	}
	process.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	if usage := state.snapshot(Options{}, "thread", "", "", 0, "", false).Usage; usage == nil || usage.InputTokens != 5 || usage.OutputTokens != 5 || process.usageTotal.InputTokens != 5 {
		t.Fatalf("unknown-turn usage contaminated fresh thread: result=%+v cumulative=%+v", usage, process.usageTotal)
	}
}

func TestResidentUsageArrivingBeforeTurnResponse(t *testing.T) {
	testResidentUsageSequence(t, "fresh-early-usage", []int{5, 5})
}

func TestForeignThreadMalformedNotificationIgnored(t *testing.T) {
	for _, raw := range []string{
		`{"threadId":"another-thread","tokenUsage":{"total":{}}}`,
		`{"threadId":"another-thread","turnId":7,"tokenUsage":{"total":{}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			state := newRunState("run", &recordingSink{})
			state.setThread("thread")
			state.setTurn("turn")
			state.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(raw))
			state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
			if err := state.protocolError(); err != nil {
				t.Fatalf("another thread's notification failed this run: %v", err)
			}
			if result := state.snapshot(Options{}, "thread", "", "", 0, "", false); result.Checkpoint == nil || result.Usage != nil {
				t.Fatalf("foreign malformed usage contaminated result: %+v", result)
			}
		})
	}
}

func TestResidentTerminalUsageKeepsKnownBaseline(t *testing.T) {
	testResidentUsageSequence(t, "terminal-usage-chain", []int{2, 3, 3})
}

func TestResidentMixedUsageKeepsKnownBaseline(t *testing.T) {
	testResidentUsageSequence(t, "mixed-usage-chain", []int{5, 3})
}

func TestResidentUnobservedTurnInvalidatesBaseline(t *testing.T) {
	testResidentUsageSequence(t, "missing-usage-chain", []int{2, 3})
}

func testResidentUsageSequence(t *testing.T, scenario string, tokens []int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := Options{Command: buildFakeAppserver(t), Env: []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}}}
	process, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeUsageTestProcess(t, process) })
	for index, want := range tokens {
		if (scenario == "mixed-usage-chain" || scenario == "missing-usage-chain") && index == 0 {
			port := newMessageTestPort()
			port.ends = [][]driver.MessageItem{{{ID: "next", Text: "next"}}}
			opts.Messages = port
		} else {
			opts.Messages = nil
		}
		result, _, err := process.RunTurn(ctx, opts, &recordingSink{})
		if err != nil || result.Usage == nil || result.Usage.InputTokens != want || result.Usage.OutputTokens != want || result.Checkpoint == nil {
			t.Fatalf("execution %d: usage=%+v checkpoint=%+v error=%v, want %d tokens", index+1, result.Usage, result.Checkpoint, err, want)
		}
	}
}

func closeUsageTestProcess(t *testing.T, process *Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := process.CloseGracefully(ctx, time.Second); err != nil {
		t.Errorf("close usage fixture: %v", err)
	}
}

func TestTerminalUsageRejectsNegativeCounters(t *testing.T) {
	for _, field := range []string{"inputTokens", "outputTokens", "cachedInputTokens"} {
		t.Run(field, func(t *testing.T) {
			state := newRunState("run", &recordingSink{})
			state.setThread("thread")
			state.setTurn("turn")
			usage, _ := json.Marshal(usageFixture("thread", "turn", 2, 2))
			state.onNotification(NotifyThreadTokenUsageUpdated, usage)
			terminal, _ := json.Marshal(map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed", "usage": map[string]int{field: -1}}})
			state.onNotification(NotifyTurnCompleted, terminal)
			result := state.snapshot(Options{}, "thread", "", "", 0, "", false)
			if state.protocolError() == nil || result.Checkpoint != nil || !nonnegativeUsage(result.Usage) || state.usageTurns["turn"].fixed != nil {
				t.Fatalf("negative terminal usage accepted: usage=%+v checkpoint=%+v protocolErr=%v", result.Usage, result.Checkpoint, state.protocolError())
			}
		})
	}
}
