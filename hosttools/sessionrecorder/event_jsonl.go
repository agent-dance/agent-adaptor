package sessionrecorder

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ErrJSONLEventBackendClosed is returned when an operation is attempted after
// a JSONLEventBackend has been closed.
var ErrJSONLEventBackendClosed = errors.New("sessionrecorder: jsonl event backend closed")

// ErrJSONLEventLogCorrupt identifies a malformed, truncated, or internally
// inconsistent event log. Load never silently skips these records: an audit
// history that cannot be replayed faithfully is an error, not a shorter
// successful history.
var ErrJSONLEventLogCorrupt = errors.New("sessionrecorder: corrupt jsonl event log")

// JSONLEventBackend is the durable, typed-Event implementation of
// EventBackend. Flush synchronizes every dirty session file with its storage
// device. Close is idempotent and flushes before closing files.
//
// Append synchronizes its record before returning by default, so a successful
// EventRecorder.Record is durable without a separate Flush call. Hosts that
// deliberately choose buffered durability can opt out with
// WithoutJSONLEventSyncOnAppend and establish their own Flush boundaries.
type JSONLEventBackend interface {
	EventBackend
	Flush() error
}

// JSONLEventOption configures NewJSONLEventBackend.
type JSONLEventOption func(*jsonlEventBackend)

// WithJSONLEventKeyValidator replaces the business key validator. The
// backend's cross-platform single-file-component check remains mandatory, so
// a custom validator can tighten accepted keys but cannot enable path
// traversal or nested paths.
func WithJSONLEventKeyValidator(v KeyValidator) JSONLEventOption {
	return func(b *jsonlEventBackend) {
		if v != nil {
			b.validate = v
		}
	}
}

// WithJSONLEventFileMode changes the creation mode for new log files. The
// default is 0o600 because events may contain prompts, tool arguments, and
// process output. The process umask still applies; existing files are not
// chmod'ed.
func WithJSONLEventFileMode(mode os.FileMode) JSONLEventOption {
	return func(b *jsonlEventBackend) {
		if mode != 0 {
			b.fileMode = mode.Perm()
		}
	}
}

// WithJSONLEventDirMode changes the creation mode for the storage directory
// and any missing parents. The default is 0o700. The process umask still
// applies; existing directories are not chmod'ed.
func WithJSONLEventDirMode(mode os.FileMode) JSONLEventOption {
	return func(b *jsonlEventBackend) {
		if mode != 0 {
			b.dirMode = mode.Perm()
		}
	}
}

// WithoutJSONLEventSyncOnAppend chooses buffered durability. Append still
// performs one complete JSONL write and reports encoding/write errors, but the
// caller must use Flush (or check Close's error) to observe storage sync
// failures. The default synchronizes every append.
func WithoutJSONLEventSyncOnAppend() JSONLEventOption {
	return func(b *jsonlEventBackend) {
		b.syncOnAppend = false
	}
}

// NewJSONLEventBackend creates a typed Event JSONL backend rooted at dir.
// Each session is stored as <dir>/<sessionKey>.jsonl with one stable
// EventRecord envelope per line.
//
// The constructor rejects an empty path, resolves dir to an absolute path,
// and creates dir plus missing parents. Creation errors are returned; it never
// substitutes an in-memory backend. Missing parents use the configured
// directory mode, while existing directory permissions are left untouched.
// Opening or writing an individual session file happens in Append and any
// resulting error is returned to the caller.
//
// The backend coordinates concurrent callers within one process. Concurrent
// writers in different processes are intentionally unsupported; use a
// coordinator-aware EventBackend for that deployment model.
func NewJSONLEventBackend(dir string, opts ...JSONLEventOption) (JSONLEventBackend, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("sessionrecorder: jsonl event directory is empty")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("sessionrecorder: resolve jsonl event directory %q: %w", dir, err)
	}
	b := &jsonlEventBackend{
		dir:          filepath.Clean(absDir),
		validate:     DefaultKeyValidator,
		fileMode:     0o600,
		dirMode:      0o700,
		syncOnAppend: true,
		entries:      make(map[string]*jsonlEventEntry),
		openAppend:   openJSONLEventAppendFile,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	if err := os.MkdirAll(b.dir, b.dirMode); err != nil {
		return nil, fmt.Errorf("sessionrecorder: create jsonl event directory %q: %w", b.dir, err)
	}
	info, err := os.Stat(b.dir)
	if err != nil {
		return nil, fmt.Errorf("sessionrecorder: inspect jsonl event directory %q: %w", b.dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sessionrecorder: jsonl event path %q is not a directory", b.dir)
	}
	return b, nil
}

type jsonlEventBackend struct {
	dir          string
	validate     KeyValidator
	fileMode     os.FileMode
	dirMode      os.FileMode
	syncOnAppend bool
	openAppend   func(string, os.FileMode) (jsonlEventFile, error)

	mu      sync.Mutex
	entries map[string]*jsonlEventEntry
	closed  bool

	closeOnce sync.Once
	closeErr  error
}

type jsonlEventFile interface {
	Write([]byte) (int, error)
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Sync() error
	Close() error
}

type jsonlEventEntry struct {
	mu       sync.Mutex
	file     jsonlEventFile
	dirty    bool
	poisoned error
}

func openJSONLEventAppendFile(path string, mode os.FileMode) (jsonlEventFile, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, mode)
}

func (b *jsonlEventBackend) Load(ctx context.Context, key string) ([]EventRecord, error) {
	path, err := b.path(key)
	if err != nil {
		return nil, err
	}
	entry, err := b.entry(key)
	if err != nil {
		return nil, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if entry.poisoned != nil {
		return nil, fmt.Errorf("sessionrecorder: load %q: %w", key, entry.poisoned)
	}
	return loadJSONLEventFile(ctx, path, key)
}

func (b *jsonlEventBackend) Append(ctx context.Context, key string, record EventRecord) error {
	path, err := b.path(key)
	if err != nil {
		return err
	}
	if err := b.checkOpen(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateJSONLEventRecordHeader(record); err != nil {
		return fmt.Errorf("sessionrecorder: invalid event record for %q: %w", key, err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("sessionrecorder: encode event record for %q: %w", key, err)
	}
	data = append(data, '\n')

	entry, err := b.entry(key)
	if err != nil {
		return err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.checkOpen(); err != nil {
		return err
	}
	if entry.poisoned != nil {
		return fmt.Errorf("sessionrecorder: append %q: %w", key, entry.poisoned)
	}
	if entry.file == nil {
		entry.file, err = b.openAppend(path, b.fileMode)
		if err != nil {
			return fmt.Errorf("sessionrecorder: open event log %q: %w", path, err)
		}
	}
	if err := appendJSONLEventLine(entry, data, b.syncOnAppend); err != nil {
		return fmt.Errorf("sessionrecorder: append event log %q: %w", path, err)
	}
	return nil
}

func (b *jsonlEventBackend) Sessions(ctx context.Context) ([]SessionInfo, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, fmt.Errorf("sessionrecorder: list jsonl event directory %q: %w", b.dir, err)
	}
	result := make([]SessionInfo, 0, len(entries))
	for _, dirEntry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), jsonlExt) {
			continue
		}
		key := strings.TrimSuffix(dirEntry.Name(), jsonlExt)
		if _, err := b.path(key); err != nil {
			// Files that cannot be produced through this backend are not
			// sessions and are intentionally ignored.
			continue
		}
		records, err := b.Load(ctx, key)
		if err != nil {
			return nil, err
		}
		if len(records) == 0 {
			continue
		}
		last := records[len(records)-1]
		result = append(result, SessionInfo{
			Key:        key,
			LastSeq:    last.HostSeq,
			RecordedAt: last.RecordedAt,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].RecordedAt.After(result[j].RecordedAt)
	})
	return result, nil
}

func (b *jsonlEventBackend) Flush() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrJSONLEventBackendClosed
	}
	entries := sortedJSONLEventEntries(b.entries)
	b.mu.Unlock()

	var errs []error
	for _, named := range entries {
		named.entry.mu.Lock()
		if named.entry.poisoned != nil {
			errs = append(errs, fmt.Errorf("sessionrecorder: flush event log %q: %w", named.key, named.entry.poisoned))
		}
		if err := flushJSONLEventEntry(named.entry); err != nil {
			errs = append(errs, fmt.Errorf("sessionrecorder: flush event log %q: %w", named.key, err))
		}
		named.entry.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (b *jsonlEventBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.close()
	})
	return b.closeErr
}

func (b *jsonlEventBackend) close() error {
	b.mu.Lock()
	b.closed = true
	entries := sortedJSONLEventEntries(b.entries)
	b.mu.Unlock()

	var errs []error
	for _, named := range entries {
		named.entry.mu.Lock()
		if named.entry.poisoned != nil {
			errs = append(errs, fmt.Errorf("sessionrecorder: close event log %q: %w", named.key, named.entry.poisoned))
		}
		if named.entry.file != nil {
			if err := flushJSONLEventEntry(named.entry); err != nil {
				errs = append(errs, fmt.Errorf("sessionrecorder: flush event log %q during close: %w", named.key, err))
			}
			if err := named.entry.file.Close(); err != nil {
				errs = append(errs, fmt.Errorf("sessionrecorder: close event log %q: %w", named.key, err))
			}
			named.entry.file = nil
		}
		named.entry.mu.Unlock()
	}
	return errors.Join(errs...)
}

type namedJSONLEventEntry struct {
	key   string
	entry *jsonlEventEntry
}

func sortedJSONLEventEntries(entries map[string]*jsonlEventEntry) []namedJSONLEventEntry {
	result := make([]namedJSONLEventEntry, 0, len(entries))
	for key, entry := range entries {
		result = append(result, namedJSONLEventEntry{key: key, entry: entry})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key < result[j].key })
	return result
}

func (b *jsonlEventBackend) entry(key string) (*jsonlEventEntry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrJSONLEventBackendClosed
	}
	entry := b.entries[key]
	if entry == nil {
		entry = &jsonlEventEntry{}
		b.entries[key] = entry
	}
	return entry, nil
}

func (b *jsonlEventBackend) checkOpen() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrJSONLEventBackendClosed
	}
	return nil
}

func (b *jsonlEventBackend) path(key string) (string, error) {
	if b.validate != nil {
		if err := b.validate(key); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidSessionKey, err)
		}
	}
	// This invariant is deliberately independent from the replaceable
	// validator. It keeps the on-disk layout portable and contained.
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key ||
		strings.ContainsAny(key, `/\`) || filepath.VolumeName(key) != "" {
		return "", fmt.Errorf("%w: session key %q is not a portable file name", ErrInvalidSessionKey, key)
	}
	return filepath.Join(b.dir, key+jsonlExt), nil
}

func appendJSONLEventLine(entry *jsonlEventEntry, data []byte, syncOnAppend bool) error {
	info, err := entry.file.Stat()
	if err != nil {
		return err
	}
	before := info.Size()
	written, writeErr := entry.file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return rollbackJSONLEventAppend(entry, before, fmt.Errorf("write %d of %d bytes: %w", written, len(data), writeErr))
	}
	entry.dirty = true
	if !syncOnAppend {
		return nil
	}
	if err := entry.file.Sync(); err != nil {
		return rollbackJSONLEventAppend(entry, before, fmt.Errorf("sync: %w", err))
	}
	entry.dirty = false
	return nil
}

func rollbackJSONLEventAppend(entry *jsonlEventEntry, size int64, cause error) error {
	truncateErr := entry.file.Truncate(size)
	var syncErr error
	if truncateErr == nil {
		syncErr = entry.file.Sync()
	}
	if truncateErr == nil && syncErr == nil {
		entry.dirty = false
		return cause
	}
	entry.poisoned = errors.Join(
		cause,
		fmt.Errorf("rollback partial append to %d bytes: %w", size, errors.Join(truncateErr, syncErr)),
	)
	entry.dirty = true
	return entry.poisoned
}

func flushJSONLEventEntry(entry *jsonlEventEntry) error {
	if entry.file == nil || !entry.dirty {
		return nil
	}
	if err := entry.file.Sync(); err != nil {
		return err
	}
	entry.dirty = false
	return nil
}

func loadJSONLEventFile(ctx context.Context, path, key string) ([]EventRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sessionrecorder: open event log %q: %w", path, err)
	}
	records, readErr := readJSONLEventRecords(ctx, file, key)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return records, nil
}

func readJSONLEventRecords(ctx context.Context, reader io.Reader, key string) ([]EventRecord, error) {
	buffered := bufio.NewReaderSize(reader, 64*1024)
	records := make([]EventRecord, 0, 64)
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if line[len(line)-1] != '\n' {
				return nil, corruptJSONLEventLine(key, lineNo, errors.New("unterminated final record"))
			}
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) == 0 {
				return nil, corruptJSONLEventLine(key, lineNo, errors.New("empty record"))
			}
			var record EventRecord
			if err := json.Unmarshal(line, &record); err != nil {
				return nil, corruptJSONLEventLine(key, lineNo, err)
			}
			if err := validateLoadedJSONLEventRecord(records, record); err != nil {
				return nil, corruptJSONLEventLine(key, lineNo, err)
			}
			records = append(records, record)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return records, nil
			}
			return nil, fmt.Errorf("sessionrecorder: read event log for %q: %w", key, readErr)
		}
	}
}

func validateLoadedJSONLEventRecord(previous []EventRecord, record EventRecord) error {
	if err := validateJSONLEventRecordHeader(record); err != nil {
		return err
	}
	want := HostSeq(1)
	if len(previous) > 0 {
		want = previous[len(previous)-1].HostSeq + 1
	}
	if record.HostSeq != want {
		return fmt.Errorf("host_seq is %d, want %d", record.HostSeq, want)
	}
	return nil
}

func validateJSONLEventRecordHeader(record EventRecord) error {
	if record.HostSeq == 0 {
		return errors.New("host_seq must be greater than zero")
	}
	if record.RecordedAt.IsZero() {
		return errors.New("recorded_at must be set")
	}
	return nil
}

func corruptJSONLEventLine(key string, lineNo int, cause error) error {
	return fmt.Errorf("%w: session %q line %d: %v", ErrJSONLEventLogCorrupt, key, lineNo, cause)
}

// Compile-time assertions keep the public backend contract and the concrete
// implementation aligned as either evolves.
var _ JSONLEventBackend = (*jsonlEventBackend)(nil)
