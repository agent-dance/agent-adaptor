package sessionrecorder

import (
	"context"
	"errors"
	"sort"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Range returns the currently recorded events satisfying after < HostSeq <= to,
// in ascending HostSeq order. A reversed or empty interval returns no records.
// A bound beyond the current history is clamped; Range never waits for events.
// Like Since, it reports key-validation, storage and closed-recorder errors,
// including for an empty interval. Returned records are independent snapshots.
//
// The recorder's existing EventRecorder implementation is sufficient. The
// built-in recorder copies only the selected window; other implementations are
// queried once with Since(ctx, sessionKey, 0) to obtain a consistent snapshot.
func Range(ctx context.Context, recorder EventRecorder, sessionKey string, after, to HostSeq) ([]EventRecord, error) {
	return queryHistory(ctx, recorder, sessionKey, historyWindow{after: after, to: to})
}

// Tail returns the latest n recorded events, in ascending HostSeq order.
// n <= 0 returns no records; n beyond the history length returns all records.
// Error, snapshot and custom-recorder semantics are the same as Range.
func Tail(ctx context.Context, recorder EventRecorder, sessionKey string, n int) ([]EventRecord, error) {
	return queryHistory(ctx, recorder, sessionKey, historyWindow{tail: n, useTail: true})
}

// RangeFromRunStart selects the same raw window as Range, then extends only
// its lower bound to the first record's known start. An empty raw window stays
// empty, and the upper bound never moves. This does not promise a completed run
// or filter out interleaved events from other runs.
//
// Ordinary events, including approvals, align to RunStarted with the same
// authoritative EventMeta.RunID. A RunStarted is already its own boundary.
// User TextDelta events align to PhaseStart with the same MessageID and RunID.
// Empty run IDs never imply ownership by a nearby run. User text without a run
// ID can align within its message, but never across a run boundary. If the
// relevant start is absent, the original lower bound is retained. Provider
// IDs, source metadata and event bodies are not used to infer ownership.
// Replay remains observational: recorded approvals have no live responder.
func RangeFromRunStart(ctx context.Context, recorder EventRecorder, sessionKey string, after, to HostSeq) ([]EventRecord, error) {
	return queryHistory(ctx, recorder, sessionKey, historyWindow{after: after, to: to, fromRunStart: true})
}

// TailFromRunStart selects the same raw window as Tail and applies the
// lower-bound alignment documented by RangeFromRunStart. It may return more
// than n records. Missing boundaries do not cause additional records to be
// guessed or synthesized.
func TailFromRunStart(ctx context.Context, recorder EventRecorder, sessionKey string, n int) ([]EventRecord, error) {
	return queryHistory(ctx, recorder, sessionKey, historyWindow{tail: n, useTail: true, fromRunStart: true})
}

type historyWindow struct {
	after, to    HostSeq
	tail         int
	useTail      bool
	fromRunStart bool
}

func queryHistory(ctx context.Context, recorder EventRecorder, key string, window historyWindow) ([]EventRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if recorder == nil {
		return nil, errors.New("sessionrecorder: query requires a non-nil EventRecorder")
	}
	if native, ok := recorder.(*eventRecorder); ok {
		return native.query(ctx, key, window)
	}
	// The fallback preserves the original interface, including custom key
	// policies. One read prevents a range and its boundary from coming from
	// different snapshots while another goroutine records events.
	history, err := recorder.Since(ctx, key, 0)
	if err != nil {
		return nil, err
	}
	return window.records(ctx, history)
}

func (r *eventRecorder) query(ctx context.Context, key string, window historyWindow) ([]EventRecord, error) {
	if err := r.checkKey(key); err != nil {
		return nil, err
	}
	st, err := r.loadedSession(ctx, key)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return window.records(ctx, st.history)
}

func (w historyWindow) records(ctx context.Context, history []EventRecord) ([]EventRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lo, hi := w.bounds(history)
	if lo >= hi {
		return nil, nil
	}
	if w.fromRunStart {
		lo = historyStart(history, lo)
	}
	out := make([]EventRecord, hi-lo)
	for i, record := range history[lo:hi] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = cloneRecord(record)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (w historyWindow) bounds(history []EventRecord) (lo, hi int) {
	if w.useTail {
		if w.tail <= 0 {
			return 0, 0
		}
		if w.tail >= len(history) {
			return 0, len(history)
		}
		return len(history) - w.tail, len(history)
	}
	if w.to <= w.after {
		return 0, 0
	}
	lo = sort.Search(len(history), func(i int) bool { return history[i].HostSeq > w.after })
	hi = sort.Search(len(history), func(i int) bool { return history[i].HostSeq > w.to })
	return lo, hi
}

func historyStart(history []EventRecord, index int) int {
	ev := history[index].Event
	if text, ok := ev.(adaptor.TextDelta); ok && text.Role == adaptor.RoleUser {
		return userMessageStart(history, index, text)
	}
	if _, ok := ev.(adaptor.RunStarted); ok {
		return index
	}
	runID := recordedRunID(ev)
	if runID == "" {
		return index
	}
	for i := index - 1; i >= 0; i-- {
		if start, ok := history[i].Event.(adaptor.RunStarted); ok && start.Meta().RunID == runID {
			return i
		}
	}
	return index
}

func userMessageStart(history []EventRecord, index int, target adaptor.TextDelta) int {
	if target.MessageID == "" || target.Phase == adaptor.PhaseStart {
		return index
	}
	runID := target.Meta().RunID
	for i := index - 1; i >= 0; i-- {
		ev := history[i].Event
		switch ev.(type) {
		case adaptor.RunStarted, adaptor.RunFinished:
			if runID == "" || recordedRunID(ev) == runID {
				return index
			}
		}
		text, ok := ev.(adaptor.TextDelta)
		if !ok || text.Role != adaptor.RoleUser || text.MessageID != target.MessageID || text.Meta().RunID != runID {
			continue
		}
		switch text.Phase {
		case adaptor.PhaseStart:
			return i
		case adaptor.PhaseEnd:
			// Reusing a message ID cannot connect to a completed message.
			return index
		}
	}
	return index
}

func recordedRunID(ev adaptor.Event) string {
	if ev == nil {
		return ""
	}
	if request, ok := ev.(*adaptor.ApprovalRequest); ok && request == nil {
		return ""
	}
	return ev.Meta().RunID
}
