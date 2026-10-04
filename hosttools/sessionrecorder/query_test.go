package sessionrecorder_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// Keep both the original interface and constructor signature usable by hosts.
var _ sessionrecorder.EventRecorder = (*queryLegacyRecorder)(nil)
var _ func(sessionrecorder.EventBackend, ...sessionrecorder.EventOption) sessionrecorder.EventRecorder = sessionrecorder.NewEventRecorder

type queryLegacyRecorder struct {
	read func(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error)
}

func (r *queryLegacyRecorder) Record(context.Context, string, adaptor.Event) (sessionrecorder.EventRecord, error) {
	return sessionrecorder.EventRecord{}, errors.New("read-only test recorder")
}
func (r *queryLegacyRecorder) Since(ctx context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
	return r.read(ctx, key, after)
}
func (*queryLegacyRecorder) Sessions(context.Context) ([]sessionrecorder.SessionInfo, error) {
	return nil, nil
}
func (*queryLegacyRecorder) Close() error { return nil }

type historyQuery struct {
	after, to uint64
	n         int
	tail      bool
	align     bool
}

func (q historyQuery) run(ctx context.Context, r sessionrecorder.EventRecorder, key string) ([]sessionrecorder.EventRecord, error) {
	if q.tail {
		if q.align {
			return sessionrecorder.TailFromRunStart(ctx, r, key, q.n)
		}
		return sessionrecorder.Tail(ctx, r, key, q.n)
	}
	if q.align {
		return sessionrecorder.RangeFromRunStart(ctx, r, key, q.after, q.to)
	}
	return sessionrecorder.Range(ctx, r, key, q.after, q.to)
}

func queryEvent(ev adaptor.Event, run string) adaptor.Event {
	return adaptor.WithEventMeta(ev, adaptor.EventMeta{RunID: run, Sequence: 1})
}

func queryRecorder(t *testing.T, storage string, events []adaptor.Event) sessionrecorder.EventRecorder {
	t.Helper()
	var backend sessionrecorder.EventBackend = sessionrecorder.NewMemoryEventBackend()
	var path string
	if storage == "jsonl-reopened" {
		path = t.TempDir()
		var err error
		backend, err = sessionrecorder.NewJSONLEventBackend(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := sessionrecorder.NewEventRecorder(backend)
	for _, ev := range events {
		if _, err := r.Record(context.Background(), "history", ev); err != nil {
			t.Fatal(err)
		}
	}
	if path != "" {
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		backend, err = sessionrecorder.NewJSONLEventBackend(path)
		if err != nil {
			t.Fatal(err)
		}
		r = sessionrecorder.NewEventRecorder(backend)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func requireQuerySeqs(t *testing.T, records []sessionrecorder.EventRecord, err error, want ...uint64) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, record := range records {
		got = append(got, record.HostSeq)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HostSeq = %v, want %v", got, want)
	}
	if len(want) == 0 && records != nil {
		t.Fatal("empty query should return nil records")
	}
}

func TestHistoryQueryWindowsAndReopen(t *testing.T) {
	for _, storage := range []string{"memory", "jsonl-reopened"} {
		t.Run(storage, func(t *testing.T) {
			events := []adaptor.Event{
				queryEvent(adaptor.RunStarted{}, "a"),
				queryEvent(adaptor.TextDelta{Text: "a"}, "a"),
				queryEvent(adaptor.RunFinished{}, "a"),
				queryEvent(adaptor.RunStarted{}, "b"),
				queryEvent(adaptor.ToolCall{ID: "tool"}, "b"),
				queryEvent(adaptor.Dropped{Count: 7}, "b"),
				queryEvent(adaptor.RunFinished{Failed: true, Reason: adaptor.ReasonAgentError}, "b"),
			}
			r := queryRecorder(t, storage, events)
			cases := []struct {
				name string
				q    historyQuery
				want []uint64
			}{
				{"range", historyQuery{after: 2, to: 5}, []uint64{3, 4, 5}},
				{"upper-clamped", historyQuery{after: 5, to: math.MaxUint64}, []uint64{6, 7}},
				{"empty", historyQuery{after: 3, to: 3}, nil},
				{"reversed", historyQuery{after: 7, to: 2}, nil},
				{"past-last", historyQuery{after: 7, to: math.MaxUint64}, nil},
				{"tail", historyQuery{tail: true, n: 2}, []uint64{6, 7}},
				{"tail-all", historyQuery{tail: true, n: math.MaxInt}, []uint64{1, 2, 3, 4, 5, 6, 7}},
				{"tail-zero", historyQuery{tail: true}, nil},
				{"tail-negative", historyQuery{tail: true, n: -1}, nil},
				{"align-lower-only", historyQuery{after: 4, to: 6, align: true}, []uint64{4, 5, 6}},
				{"align-terminal", historyQuery{after: 2, to: 3, align: true}, []uint64{1, 2, 3}},
				{"align-tail", historyQuery{tail: true, n: 1, align: true}, []uint64{4, 5, 6, 7}},
				{"start-stays-self-range-aaf95bd", historyQuery{after: 3, to: 5, align: true}, []uint64{4, 5}},
				{"start-stays-self-tail-aaf95bd", historyQuery{tail: true, n: 4, align: true}, []uint64{4, 5, 6, 7}},
				{"align-empty", historyQuery{after: 4, to: 4, align: true}, nil},
				{"align-empty-tail", historyQuery{tail: true, n: -1, align: true}, nil},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					records, err := tc.q.run(context.Background(), r, "history")
					requireQuerySeqs(t, records, err, tc.want...)
					for _, record := range records {
						if !reflect.DeepEqual(record.Event, events[record.HostSeq-1]) {
							t.Fatalf("query altered event at HostSeq %d", record.HostSeq)
						}
					}
				})
			}
			for _, q := range []historyQuery{{to: 1}, {tail: true, n: 1}, {to: 1, align: true}, {tail: true, n: 1, align: true}} {
				records, err := q.run(context.Background(), r, "missing")
				requireQuerySeqs(t, records, err)
			}
			// Reads neither renumber nor change the existing Since contract.
			records, err := r.Since(context.Background(), "history", 4)
			requireQuerySeqs(t, records, err, 5, 6, 7)
			record, err := r.Record(context.Background(), "history", queryEvent(adaptor.Notice{Text: "next"}, "c"))
			if err != nil || record.HostSeq != 8 {
				t.Fatalf("write after queries = %d, %v", record.HostSeq, err)
			}
		})
	}
}

func TestHistoryQueryRunOwnership(t *testing.T) {
	events := []adaptor.Event{
		queryEvent(adaptor.RunStarted{RunID: "provider-a"}, "a"),              // 1
		queryEvent(adaptor.RunStarted{RunID: "provider-b"}, "b"),              // 2
		queryEvent(adaptor.ToolCall{ID: "a-tool"}, "a"),                       // 3, interleaved
		queryEvent(&adaptor.ApprovalRequest{ID: "approval", RunID: "b"}, "a"), // 4
		queryEvent(adaptor.RunFinished{Failed: true}, "b"),                    // 5
		queryEvent(adaptor.TextDelta{Text: "unidentified"}, ""),               // 6
		adaptor.WithEventMeta(adaptor.TextDelta{Text: "missing"}, adaptor.EventMeta{RunID: "missing", Source: &adaptor.EventSourceMeta{RunID: "a"}}), // 7
	}
	r := queryRecorder(t, "memory", events)
	for _, tc := range []struct {
		name      string
		after, to uint64
		want      []uint64
	}{
		{"interleaved-same-run", 2, 3, []uint64{1, 2, 3}},
		{"approval-authoritative-meta", 3, 4, []uint64{1, 2, 3, 4}},
		{"failed-terminal", 4, 5, []uint64{2, 3, 4, 5}},
		{"empty-run-is-not-nearest", 5, 6, []uint64{6}},
		{"source-is-not-owner", 6, 7, []uint64{7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, err := sessionrecorder.RangeFromRunStart(context.Background(), r, "history", tc.after, tc.to)
			requireQuerySeqs(t, records, err, tc.want...)
		})
	}
	// A provider RunID on an unstamped boundary cannot supply a core RunID.
	r = queryRecorder(t, "memory", []adaptor.Event{adaptor.RunStarted{RunID: "a"}, queryEvent(adaptor.TextDelta{}, "a")})
	records, err := sessionrecorder.TailFromRunStart(context.Background(), r, "history", 1)
	requireQuerySeqs(t, records, err, 2)
}

func TestHistoryQueryUserMessageBoundaries(t *testing.T) {
	user := func(run, id string, phase adaptor.Phase) adaptor.Event {
		return queryEvent(adaptor.TextDelta{MessageID: id, Role: adaptor.RoleUser, Phase: phase}, run)
	}
	for _, tc := range []struct {
		name   string
		events []adaptor.Event
		want   []uint64
	}{
		{"message", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseContent), user("a", "m", adaptor.PhaseEnd)}, []uint64{1, 2, 3}},
		{"message-without-run", []adaptor.Event{user("", "m", adaptor.PhaseStart), user("", "m", adaptor.PhaseContent)}, []uint64{1, 2}},
		{"interleaved-runs", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("b", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseContent)}, []uint64{1, 2, 3}},
		{"wrong-run", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("b", "m", adaptor.PhaseContent)}, []uint64{2}},
		{"wrong-message", []adaptor.Event{user("a", "other", adaptor.PhaseStart), user("a", "m", adaptor.PhaseContent)}, []uint64{2}},
		{"wrong-role", []adaptor.Event{queryEvent(adaptor.TextDelta{MessageID: "m", Phase: adaptor.PhaseStart}, "a"), user("a", "m", adaptor.PhaseContent)}, []uint64{2}},
		{"empty-message", []adaptor.Event{user("a", "", adaptor.PhaseStart), user("a", "", adaptor.PhaseContent)}, []uint64{2}},
		{"no-boundary", []adaptor.Event{user("a", "m", adaptor.PhaseContent), user("a", "m", adaptor.PhaseEnd)}, []uint64{2}},
		{"reused-completed-message", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), user("a", "m", adaptor.PhaseContent)}, []uint64{3}},
		{"unidentified-crosses-run", []adaptor.Event{user("", "m", adaptor.PhaseStart), queryEvent(adaptor.RunStarted{}, "a"), user("", "m", adaptor.PhaseContent)}, []uint64{3}},
		{"same-run-boundary", []adaptor.Event{user("a", "m", adaptor.PhaseStart), queryEvent(adaptor.RunStarted{}, "a"), user("a", "m", adaptor.PhaseContent)}, []uint64{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := queryRecorder(t, "memory", tc.events)
			for _, q := range []historyQuery{{after: uint64(len(tc.events) - 1), to: uint64(len(tc.events)), align: true}, {tail: true, n: 1, align: true}} {
				records, err := q.run(context.Background(), r, "history")
				requireQuerySeqs(t, records, err, tc.want...)
			}
		})
	}
}

func TestHistoryQueryUserImagesRestoreMessageStart(t *testing.T) {
	user := func(run, id string, phase adaptor.Phase) adaptor.Event {
		return queryEvent(adaptor.TextDelta{MessageID: id, Role: adaptor.RoleUser, Phase: phase}, run)
	}
	image := func(run, id string, role adaptor.Role) adaptor.Event {
		return queryEvent(adaptor.ImageContent{MessageID: id, Type: "binary", MIMEType: "image/png", URL: "/image", Role: role}, run)
	}
	for _, storage := range []string{"memory", "jsonl-reopened"} {
		t.Run(storage, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				events []adaptor.Event
				want   []uint64
			}{
				{"closed-user-text", []adaptor.Event{queryEvent(adaptor.RunStarted{}, "a"), user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseContent), user("a", "m", adaptor.PhaseEnd), image("a", "m", adaptor.RoleUser)}, []uint64{2, 3, 4, 5}},
				{"user-message-without-run-start", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), image("a", "m", adaptor.RoleUser)}, []uint64{1, 2, 3}},
				{"empty-run-known-message", []adaptor.Event{user("", "m", adaptor.PhaseStart), user("", "m", adaptor.PhaseEnd), image("", "m", adaptor.RoleUser)}, []uint64{1, 2, 3}},
				{"latest-same-message-start", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), image("a", "m", adaptor.RoleUser)}, []uint64{3, 4, 5}},
				{"interleaved-other-run", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), user("b", "m", adaptor.PhaseStart), image("a", "m", adaptor.RoleUser)}, []uint64{1, 2, 3, 4}},
				{"different-message", []adaptor.Event{user("a", "other", adaptor.PhaseStart), user("a", "other", adaptor.PhaseEnd), image("a", "m", adaptor.RoleUser)}, []uint64{3}},
				{"different-run", []adaptor.Event{user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), image("b", "m", adaptor.RoleUser)}, []uint64{3}},
				{"missing-start", []adaptor.Event{user("a", "m", adaptor.PhaseContent), user("a", "m", adaptor.PhaseEnd), image("a", "m", adaptor.RoleUser)}, []uint64{3}},
				{"empty-run-boundary", []adaptor.Event{user("", "m", adaptor.PhaseStart), user("", "m", adaptor.PhaseEnd), queryEvent(adaptor.RunStarted{}, "a"), image("", "m", adaptor.RoleUser)}, []uint64{4}},
				{"assistant-keeps-run-boundary", []adaptor.Event{queryEvent(adaptor.RunStarted{}, "a"), user("a", "m", adaptor.PhaseStart), user("a", "m", adaptor.PhaseEnd), image("a", "m", adaptor.RoleAssistant)}, []uint64{1, 2, 3, 4}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					r := queryRecorder(t, storage, tc.events)
					for _, q := range []historyQuery{{after: uint64(len(tc.events) - 1), to: uint64(len(tc.events)), align: true}, {tail: true, n: 1, align: true}} {
						records, err := q.run(context.Background(), r, "history")
						requireQuerySeqs(t, records, err, tc.want...)
					}
				})
			}
		})
	}
}

func TestHistoryQueryLegacyRecorderSnapshotAndUint64(t *testing.T) {
	calls := 0
	r := &queryLegacyRecorder{read: func(_ context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
		calls++
		if key != "custom/key" || after != 0 {
			t.Fatalf("Since(%q, %d), want custom key and one whole snapshot", key, after)
		}
		return []sessionrecorder.EventRecord{
			{HostSeq: math.MaxUint64 - 3, Event: queryEvent(adaptor.RunStarted{}, "a")},
			{HostSeq: math.MaxUint64 - 1, Event: queryEvent(adaptor.TextDelta{}, "a")},
			{HostSeq: math.MaxUint64, Event: queryEvent(adaptor.RunFinished{}, "a")},
		}, nil
	}}
	records, err := sessionrecorder.Range(context.Background(), r, "custom/key", math.MaxUint64-2, math.MaxUint64)
	requireQuerySeqs(t, records, err, math.MaxUint64-1, math.MaxUint64)
	if calls != 1 {
		t.Fatalf("Since called %d times", calls)
	}
	records, err = sessionrecorder.RangeFromRunStart(context.Background(), r, "custom/key", math.MaxUint64-2, math.MaxUint64-1)
	requireQuerySeqs(t, records, err, math.MaxUint64-3, math.MaxUint64-1)
	if calls != 2 {
		t.Fatalf("Since called %d times", calls)
	}
}

func TestHistoryQueryCustomPointerBoundariesAndNilValues(t *testing.T) {
	start := queryEvent(adaptor.RunStarted{}, "run").(adaptor.RunStarted)
	textStart := queryEvent(adaptor.TextDelta{Role: adaptor.RoleUser, MessageID: "m", Phase: adaptor.PhaseStart}, "run").(adaptor.TextDelta)
	text := queryEvent(adaptor.TextDelta{Role: adaptor.RoleUser, MessageID: "m"}, "run").(adaptor.TextDelta)
	textEnd := queryEvent(adaptor.TextDelta{Role: adaptor.RoleUser, MessageID: "m", Phase: adaptor.PhaseEnd}, "run").(adaptor.TextDelta)
	image := queryEvent(adaptor.ImageContent{Role: adaptor.RoleUser, MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/image"}, "run").(adaptor.ImageContent)
	finish := queryEvent(adaptor.RunFinished{}, "run").(adaptor.RunFinished)
	for _, tc := range []struct {
		name   string
		events []adaptor.Event
		want   []uint64
	}{
		{"run-pointer", []adaptor.Event{&start, queryEvent(adaptor.Notice{}, "run")}, []uint64{1, 2}},
		{"user-pointers", []adaptor.Event{&start, &textStart, &text}, []uint64{2, 3}},
		{"image-pointers", []adaptor.Event{&start, &textStart, &textEnd, &image}, []uint64{2, 3, 4}},
		{"terminal-pointer-blocks", []adaptor.Event{textStart, &finish, text}, []uint64{3}},
		{"text-end-pointer-blocks", []adaptor.Event{textStart, &textEnd, text}, []uint64{3}},
		{"nil-boundary", []adaptor.Event{(*adaptor.RunStarted)(nil), text}, []uint64{2}},
		{"nil-target", []adaptor.Event{start, (*adaptor.ImageContent)(nil)}, []uint64{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := make([]sessionrecorder.EventRecord, len(tc.events))
			for i, event := range tc.events {
				history[i] = sessionrecorder.EventRecord{HostSeq: uint64(i + 1), Event: event}
			}
			calls := 0
			r := &queryLegacyRecorder{read: func(_ context.Context, key string, after uint64) ([]sessionrecorder.EventRecord, error) {
				calls++
				if key != "custom/key" || after != 0 {
					t.Fatal("custom snapshot scope changed")
				}
				return history, nil
			}}
			for i, q := range []historyQuery{{after: uint64(len(history) - 1), to: uint64(len(history)), align: true}, {tail: true, n: 1, align: true}} {
				records, err := q.run(context.Background(), r, "custom/key")
				requireQuerySeqs(t, records, err, tc.want...)
				if calls != i+1 {
					t.Fatal("query read more than one snapshot")
				}
				if tc.name == "nil-target" && records[0].Event != nil {
					t.Fatal("typed nil event did not normalize to nil")
				}
			}
		})
	}
}

func TestHistoryQueryOwnsReturnedValues(t *testing.T) {
	for _, storage := range []string{"memory", "jsonl-reopened", "legacy"} {
		t.Run(storage, func(t *testing.T) {
			events := []adaptor.Event{
				queryEvent(adaptor.RunStarted{}, "a"),
				queryEvent(adaptor.ToolCall{ID: "t", Args: map[string]any{"nested": []any{"original"}}}, "a"),
				queryEvent(&adaptor.ApprovalRequest{ID: "approval", Kind: adaptor.ApprovalQuestion, Choices: []adaptor.Choice{{Key: "k", Label: "original"}}, Details: map[string]any{"nested": []any{"original"}}}, "a"),
			}
			var r sessionrecorder.EventRecorder
			if storage == "legacy" {
				history := make([]sessionrecorder.EventRecord, len(events))
				for i, ev := range events {
					history[i] = sessionrecorder.EventRecord{HostSeq: uint64(i + 1), Event: ev}
				}
				r = &queryLegacyRecorder{read: func(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error) { return history, nil }}
			} else {
				r = queryRecorder(t, storage, events)
			}
			for _, q := range []historyQuery{{to: 3}, {tail: true, n: 3}, {after: 2, to: 3, align: true}, {tail: true, n: 1, align: true}} {
				records, err := q.run(context.Background(), r, "history")
				requireQuerySeqs(t, records, err, 1, 2, 3)
				tool := records[1].Event.(adaptor.ToolCall)
				request := records[2].Event.(*adaptor.ApprovalRequest)
				if tool.Args["nested"].([]any)[0] != "original" || request.Choices[0].Label != "original" || request.Details["nested"].([]any)[0] != "original" {
					t.Fatal("earlier query mutated recorded history")
				}
				tool.Args["nested"].([]any)[0] = "changed"
				request.Choices[0].Label = "changed"
				request.Details["nested"].([]any)[0] = "changed"
				if err := request.Answer(context.Background(), "k"); !errors.Is(err, adaptor.ErrApprovalUnavailable) {
					t.Fatalf("historical approval Answer = %v", err)
				}
			}
		})
	}
}

type queryFailingBackend struct {
	sessionrecorder.EventBackend
	err error
}

func (b queryFailingBackend) Load(context.Context, string) ([]sessionrecorder.EventRecord, error) {
	return nil, b.err
}

func TestHistoryQueryErrorsAndCancellation(t *testing.T) {
	storageErr := errors.New("storage unavailable")
	failed := sessionrecorder.NewEventRecorder(queryFailingBackend{EventBackend: sessionrecorder.NewMemoryEventBackend(), err: storageErr})
	defer failed.Close()
	closed := queryRecorder(t, "memory", nil)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []historyQuery{{to: 1}, {tail: true, n: 1}, {to: 1, align: true}, {tail: true, n: 1, align: true}, {}, {tail: true}, {align: true}, {tail: true, align: true}} {
		if _, err := q.run(context.Background(), failed, "history"); !errors.Is(err, storageErr) {
			t.Fatalf("query hid storage error: %v", err)
		}
		if _, err := q.run(context.Background(), failed, "../invalid"); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
			t.Fatalf("query hid invalid key: %v", err)
		}
		if _, err := q.run(context.Background(), closed, "history"); err == nil {
			t.Fatal("query hid closed recorder")
		}
		if _, err := q.run(context.Background(), nil, "history"); err == nil {
			t.Fatal("nil recorder accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		neverRead := &queryLegacyRecorder{read: func(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error) {
			t.Fatal("cancelled query accessed recorder")
			return nil, nil
		}}
		if _, err := q.run(ctx, neverRead, "history"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled query = %v", err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		lateRead := &queryLegacyRecorder{read: func(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error) {
			cancel()
			return []sessionrecorder.EventRecord{{HostSeq: 1, Event: adaptor.TextDelta{}}}, nil
		}}
		if records, err := q.run(ctx, lateRead, "history"); !errors.Is(err, context.Canceled) || records != nil {
			t.Fatalf("cancelled after read = %v, %v", records, err)
		}
		legacyError := &queryLegacyRecorder{read: func(context.Context, string, uint64) ([]sessionrecorder.EventRecord, error) { return nil, storageErr }}
		if _, err := q.run(context.Background(), legacyError, "history"); !errors.Is(err, storageErr) {
			t.Fatalf("custom recorder error = %v", err)
		}
	}
}

func TestHistoryQueryConcurrentRecord(t *testing.T) {
	r := queryRecorder(t, "memory", []adaptor.Event{queryEvent(adaptor.RunStarted{}, "a")})
	var workers sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 2)
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 100; i++ {
			if _, err := r.Record(context.Background(), "history", queryEvent(adaptor.TextDelta{}, "a")); err != nil {
				failures <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 100; i++ {
			records, err := sessionrecorder.TailFromRunStart(context.Background(), r, "history", 1)
			if err != nil {
				failures <- err
				return
			}
			for j, record := range records {
				if record.HostSeq != uint64(j+1) {
					failures <- fmt.Errorf("inconsistent window: record %d has HostSeq %d", j, record.HostSeq)
					return
				}
			}
		}
	}()
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	records, err := sessionrecorder.Tail(context.Background(), r, "history", 2)
	requireQuerySeqs(t, records, err, 100, 101)
}
