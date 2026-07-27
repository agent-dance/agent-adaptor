package sessionrecorder

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestJSONLEventAppendDetectsShortWriteAndRollsBack(t *testing.T) {
	t.Parallel()

	fake := &fakeJSONLEventFile{writeN: 3}
	backend := newFakeJSONLEventBackend(t, fake)
	record := testJSONLEventRecord()
	err := backend.Append(context.Background(), "partial", record)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Append error = %v, want io.ErrShortWrite", err)
	}
	if fake.truncateTo != 0 {
		t.Fatalf("rollback truncate size = %d, want 0", fake.truncateTo)
	}
	if fake.syncCalls != 1 {
		t.Fatalf("rollback sync calls = %d, want 1", fake.syncCalls)
	}
	// A successful rollback leaves the writer usable, rather than reporting
	// success or silently appending after a partial JSON object.
	fake.writeN = -1
	if err := backend.Append(context.Background(), "partial", record); err != nil {
		t.Fatalf("Append after successful rollback: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestJSONLEventAppendPoisonsWriterWhenPartialWriteCannotRollback(t *testing.T) {
	t.Parallel()

	rollbackErr := errors.New("truncate unavailable")
	fake := &fakeJSONLEventFile{writeN: 2, truncateErr: rollbackErr}
	backend := newFakeJSONLEventBackend(t, fake)
	record := testJSONLEventRecord()
	firstErr := backend.Append(context.Background(), "poisoned", record)
	if !errors.Is(firstErr, io.ErrShortWrite) || !errors.Is(firstErr, rollbackErr) {
		t.Fatalf("Append error = %v, want short-write and rollback errors", firstErr)
	}
	if err := backend.Append(context.Background(), "poisoned", record); !errors.Is(err, rollbackErr) {
		t.Fatalf("second Append error = %v, want poisoned rollback error", err)
	}
	if err := backend.Flush(); !errors.Is(err, rollbackErr) {
		t.Fatalf("Flush error = %v, want poisoned rollback error", err)
	}
	if err := backend.Close(); !errors.Is(err, rollbackErr) {
		t.Fatalf("Close error = %v, want poisoned rollback error", err)
	}
}

func TestJSONLEventSyncAndCloseFailuresAreObservable(t *testing.T) {
	t.Parallel()

	syncErr := errors.New("sync failed")
	closeErr := errors.New("close failed")
	fake := &fakeJSONLEventFile{writeN: -1, syncErr: syncErr, closeErr: closeErr}
	backend := newFakeJSONLEventBackend(t, fake, WithoutJSONLEventSyncOnAppend())
	if err := backend.Append(context.Background(), "durability", testJSONLEventRecord()); err != nil {
		t.Fatalf("buffered Append: %v", err)
	}
	if fake.syncCalls != 0 {
		t.Fatalf("buffered Append sync calls = %d, want 0", fake.syncCalls)
	}
	if err := backend.Flush(); !errors.Is(err, syncErr) {
		t.Fatalf("Flush error = %v, want sync error", err)
	}
	if err := backend.Close(); !errors.Is(err, syncErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want joined sync and close errors", err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("file close calls = %d, want 1", fake.closeCalls)
	}
	if err := backend.Close(); !errors.Is(err, syncErr) || !errors.Is(err, closeErr) {
		t.Fatalf("idempotent second Close error = %v, want same joined result", err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("file close calls after second Close = %d, want 1", fake.closeCalls)
	}
}

func TestJSONLEventDefaultAppendSynchronizesBeforeSuccess(t *testing.T) {
	t.Parallel()

	fake := &fakeJSONLEventFile{writeN: -1}
	backend := newFakeJSONLEventBackend(t, fake)
	if err := backend.Append(context.Background(), "synced", testJSONLEventRecord()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if fake.syncCalls != 1 {
		t.Fatalf("sync calls = %d, want 1", fake.syncCalls)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.syncCalls != 1 {
		t.Fatalf("clean Close issued an extra sync: calls = %d", fake.syncCalls)
	}
}

func TestJSONLEventConcurrentCloseReturnsOneStableResult(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("stable close failure")
	fake := &fakeJSONLEventFile{writeN: -1, closeErr: closeErr}
	backend := newFakeJSONLEventBackend(t, fake)
	if err := backend.Append(context.Background(), "close-race", testJSONLEventRecord()); err != nil {
		t.Fatalf("Append: %v", err)
	}

	const callers = 24
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- backend.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, closeErr) {
			t.Errorf("concurrent Close error = %v, want stable close failure", err)
		}
	}
	if fake.closeCalls != 1 {
		t.Fatalf("file close calls = %d, want exactly 1", fake.closeCalls)
	}
}

func newFakeJSONLEventBackend(t *testing.T, fake *fakeJSONLEventFile, opts ...JSONLEventOption) JSONLEventBackend {
	t.Helper()
	backend, err := NewJSONLEventBackend(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("NewJSONLEventBackend: %v", err)
	}
	concrete := backend.(*jsonlEventBackend)
	concrete.openAppend = func(string, os.FileMode) (jsonlEventFile, error) {
		return fake, nil
	}
	return backend
}

func testJSONLEventRecord() EventRecord {
	return EventRecord{
		HostSeq:    1,
		RecordedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Event:      adaptor.Dropped{Count: 1},
	}
}

type fakeJSONLEventFile struct {
	size        int64
	writeN      int
	writeErr    error
	statErr     error
	truncateErr error
	syncErr     error
	closeErr    error

	truncateTo int64
	syncCalls  int
	closeCalls int
}

func (f *fakeJSONLEventFile) Write(data []byte) (int, error) {
	written := f.writeN
	if written < 0 || written > len(data) {
		written = len(data)
	}
	f.size += int64(written)
	return written, f.writeErr
}

func (f *fakeJSONLEventFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return fakeJSONLEventFileInfo{size: f.size}, nil
}

func (f *fakeJSONLEventFile) Truncate(size int64) error {
	f.truncateTo = size
	if f.truncateErr == nil {
		f.size = size
	}
	return f.truncateErr
}

func (f *fakeJSONLEventFile) Sync() error {
	f.syncCalls++
	return f.syncErr
}

func (f *fakeJSONLEventFile) Close() error {
	f.closeCalls++
	return f.closeErr
}

type fakeJSONLEventFileInfo struct {
	size int64
}

func (f fakeJSONLEventFileInfo) Name() string       { return "events.jsonl" }
func (f fakeJSONLEventFileInfo) Size() int64        { return f.size }
func (f fakeJSONLEventFileInfo) Mode() os.FileMode  { return 0o600 }
func (f fakeJSONLEventFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeJSONLEventFileInfo) IsDir() bool        { return false }
func (f fakeJSONLEventFileInfo) Sys() any           { return nil }
