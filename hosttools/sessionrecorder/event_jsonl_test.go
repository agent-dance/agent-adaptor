package sessionrecorder_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestJSONLEventBackendPersistsStableTypedEnvelopeAcrossReopen(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "missing", "parents", "events")
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	recorder := sessionrecorder.NewEventRecorder(backend)
	ctx := context.Background()
	eventTime := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	eventMeta := func(sequence uint64) adaptor.EventMeta {
		return adaptor.EventMeta{
			RunID:     "run-1",
			ThreadKey: "thread-1",
			Sequence:  sequence,
			Time:      eventTime.Add(time.Duration(sequence) * time.Millisecond),
			TurnID:    "turn-1",
			Source: &adaptor.EventSourceMeta{
				RunID: "provider-run", ThreadID: "provider-thread", TurnID: "provider-turn",
				Sequence: sequence + 40, Timestamp: eventTime,
			},
		}
	}
	events := []adaptor.Event{
		adaptor.WithEventMeta(adaptor.TextDelta{MessageID: "message-1", Text: "hello", Phase: adaptor.PhaseContent}, eventMeta(1)),
		adaptor.WithEventMeta(adaptor.ToolResult{ID: "tool-1", Result: map[string]any{"ok": true}}, eventMeta(2)),
		adaptor.WithEventMeta(adaptor.RunFinished{RunID: "run-1", ThreadID: "thread-1"}, eventMeta(3)),
	}
	for _, event := range events {
		if _, err := recorder.Record(ctx, "thread-1", event); err != nil {
			t.Fatalf("Record(%T): %v", event, err)
		}
	}
	if err := backend.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "thread-1.jsonl"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("JSONL does not end in newline: %q", raw)
	}
	if runtime.GOOS != "windows" {
		fileInfo, err := os.Stat(filepath.Join(dir, "thread-1.jsonl"))
		if err != nil {
			t.Fatalf("Stat event log: %v", err)
		}
		if got := fileInfo.Mode().Perm() & 0o077; got != 0 {
			t.Fatalf("event log grants group/other permissions: %04o", fileInfo.Mode().Perm())
		}
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat event directory: %v", err)
		}
		if got := dirInfo.Mode().Perm() & 0o077; got != 0 {
			t.Fatalf("event directory grants group/other permissions: %04o", dirInfo.Mode().Perm())
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("line count = %d, want %d", len(lines), len(events))
	}
	var first map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first envelope: %v", err)
	}
	for _, field := range []string{"host_seq", "recorded_at", "kind", "meta", "event"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("stable envelope missing %q: %s", field, lines[0])
		}
	}
	if _, legacy := first["payload"]; legacy {
		t.Fatalf("typed event envelope contains legacy payload field: %s", lines[0])
	}

	reopened, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatalf("reopen backend: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("reopened Close: %v", err)
		}
	}()
	replayed, err := reopened.Load(ctx, "thread-1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(replayed) != len(events) {
		t.Fatalf("Load returned %d records, want %d", len(replayed), len(events))
	}
	for i, record := range replayed {
		if record.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Errorf("record %d HostSeq = %d", i, record.HostSeq)
		}
		if !reflect.DeepEqual(record.Event, events[i]) {
			t.Errorf("record %d Event = %#v, want %#v", i, record.Event, events[i])
		}
		if !reflect.DeepEqual(record.Event.Meta(), events[i].Meta()) {
			t.Errorf("record %d Meta = %#v, want %#v", i, record.Event.Meta(), events[i].Meta())
		}
	}
}

func TestJSONLEventBackendConcurrentRecorderWritesAreCompleteAndOrdered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir, sessionrecorder.WithoutJSONLEventSyncOnAppend())
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	recorder := sessionrecorder.NewEventRecorder(backend)
	ctx := context.Background()

	const count = 80
	seqs := make(chan sessionrecorder.HostSeq, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			record, err := recorder.Record(ctx, "concurrent", adaptor.TextDelta{
				MessageID: "message",
				Text:      string(rune('a' + index%26)),
			})
			if err != nil {
				errs <- err
				return
			}
			seqs <- record.HostSeq
		}(i)
	}
	wg.Wait()
	close(seqs)
	close(errs)
	for err := range errs {
		t.Errorf("Record: %v", err)
	}
	if t.Failed() {
		_ = recorder.Close()
		return
	}
	if err := backend.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	gotSeqs := make([]int, 0, count)
	for seq := range seqs {
		gotSeqs = append(gotSeqs, int(seq))
	}
	sort.Ints(gotSeqs)
	for i, seq := range gotSeqs {
		if seq != i+1 {
			t.Fatalf("assigned seq[%d] = %d, want %d", i, seq, i+1)
		}
	}

	reopened, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	records, err := reopened.Load(ctx, "concurrent")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != count {
		t.Fatalf("record count = %d, want %d", len(records), count)
	}
	for i, record := range records {
		if record.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Fatalf("persisted seq[%d] = %d, want %d", i, record.HostSeq, i+1)
		}
	}
}

func TestJSONLEventBackendRejectsEncodingFailureWithoutCreatingRecord(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	defer backend.Close()
	record := sessionrecorder.EventRecord{
		HostSeq:    1,
		RecordedAt: time.Now().UTC(),
		Event: adaptor.ToolCall{
			ID:   "bad-tool",
			Name: "bad",
			Args: map[string]any{"not_json": make(chan int)},
		},
	}
	if err := backend.Append(context.Background(), "encoding", record); err == nil {
		t.Fatal("Append unexpectedly accepted an unencodable event")
	}
	if _, err := os.Stat(filepath.Join(dir, "encoding.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("encoding failure created a log file: %v", err)
	}
	invalidHeader := sessionrecorder.EventRecord{
		HostSeq: 0, RecordedAt: time.Now().UTC(), Event: adaptor.Dropped{Count: 1},
	}
	if err := backend.Append(context.Background(), "invalid-header", invalidHeader); err == nil {
		t.Fatal("Append unexpectedly accepted HostSeq zero")
	}
	if _, err := os.Stat(filepath.Join(dir, "invalid-header.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid header created a log file: %v", err)
	}
}

func TestJSONLEventBackendFailsClosedOperationsAndCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	backend, err := sessionrecorder.NewJSONLEventBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	ctx := context.Background()
	record := sessionrecorder.EventRecord{
		HostSeq: 1, RecordedAt: time.Now().UTC(), Event: adaptor.Dropped{Count: 1},
	}
	checks := []struct {
		name string
		err  error
	}{
		{"Append", backend.Append(ctx, "closed", record)},
		{"Flush", backend.Flush()},
	}
	_, loadErr := backend.Load(ctx, "closed")
	checks = append(checks, struct {
		name string
		err  error
	}{"Load", loadErr})
	_, sessionsErr := backend.Sessions(ctx)
	checks = append(checks, struct {
		name string
		err  error
	}{"Sessions", sessionsErr})
	for _, check := range checks {
		if !errors.Is(check.err, sessionrecorder.ErrJSONLEventBackendClosed) {
			t.Errorf("%s error = %v, want ErrJSONLEventBackendClosed", check.name, check.err)
		}
	}
}

func TestJSONLEventBackendRejectsCorruptAndUnterminatedRecords(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"invalid-json": `{not-json}` + "\n",
		"unterminated": `{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"dropped","event":{"Count":1}}`,
		"sequence-gap": strings.Join([]string{
			`{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"dropped","event":{"Count":1}}`,
			`{"host_seq":3,"recorded_at":"2026-07-01T12:00:01Z","kind":"dropped","event":{"Count":1}}`,
			"",
		}, "\n"),
	}
	for name, contents := range cases {
		name, contents := name, contents
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "corrupt.jsonl"), []byte(contents), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			backend, err := sessionrecorder.NewJSONLEventBackend(dir)
			if err != nil {
				t.Fatalf("NewJSONLEventBackend: %v", err)
			}
			defer backend.Close()
			_, err = backend.Load(context.Background(), "corrupt")
			if !errors.Is(err, sessionrecorder.ErrJSONLEventLogCorrupt) {
				t.Fatalf("Load error = %v, want ErrJSONLEventLogCorrupt", err)
			}
		})
	}
}

func TestJSONLEventBackendCreationAndPathFailuresAreExplicit(t *testing.T) {
	t.Parallel()

	if backend, err := sessionrecorder.NewJSONLEventBackend(""); err == nil || backend != nil {
		t.Fatalf("empty directory = (%T, %v), want nil error result", backend, err)
	}
	root := t.TempDir()
	notDirectory := filepath.Join(root, "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if backend, err := sessionrecorder.NewJSONLEventBackend(notDirectory); err == nil || backend != nil {
		t.Fatalf("file directory = (%T, %v), want explicit failure", backend, err)
	}

	dir := filepath.Join(root, "events")
	backend, err := sessionrecorder.NewJSONLEventBackend(
		dir,
		sessionrecorder.WithJSONLEventKeyValidator(func(string) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	defer backend.Close()
	record := sessionrecorder.EventRecord{
		HostSeq: 1, RecordedAt: time.Now().UTC(), Event: adaptor.Dropped{Count: 1},
	}
	for _, key := range []string{"../escape", `nested\escape`, ""} {
		if err := backend.Append(context.Background(), key, record); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
			t.Errorf("Append(%q) error = %v, want ErrInvalidSessionKey", key, err)
		}
	}
}
