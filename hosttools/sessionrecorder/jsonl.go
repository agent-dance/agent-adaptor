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
	"time"
)

// JSONLOption configures NewJSONLBackend.
type JSONLOption func(*jsonlBackend)

// WithJSONLKeyValidator overrides the validator the JSONL backend runs
// on every session key before reading or writing. The validator
// protects the filesystem layout from path-traversal in session keys
// and is applied independently of any validator installed on the
// wrapping Recorder.
//
// The default is DefaultKeyValidator.
func WithJSONLKeyValidator(v KeyValidator) JSONLOption {
	return func(b *jsonlBackend) {
		if v != nil {
			b.validate = v
		}
	}
}

// WithJSONLFileMode overrides the mode bits OpenFile uses when creating
// a new history file. Defaults to 0o644.
func WithJSONLFileMode(mode os.FileMode) JSONLOption {
	return func(b *jsonlBackend) {
		if mode != 0 {
			b.fileMode = mode
		}
	}
}

// WithJSONLDirMode overrides the mode bits MkdirAll uses when creating
// the storage directory. Defaults to 0o755.
func WithJSONLDirMode(mode os.FileMode) JSONLOption {
	return func(b *jsonlBackend) {
		if mode != 0 {
			b.dirMode = mode
		}
	}
}

// WithJSONLBadLineHandler installs a callback that is invoked for every
// malformed line encountered during Load. The default handler swallows
// the error so a single half-written tail does not poison the whole
// session history; hosts that prefer fail-hard can install a handler
// that returns the error.
//
// If the handler returns a non-nil error, Load aborts and returns that
// error. If the handler returns nil, the line is skipped and the scan
// continues.
func WithJSONLBadLineHandler(fn func(sessionKey string, lineNo int, raw []byte, err error) error) JSONLOption {
	return func(b *jsonlBackend) {
		if fn != nil {
			b.onBadLine = fn
		}
	}
}

// NewJSONLBackend returns a Backend that persists one Record per line
// under <dir>/<sessionKey>.jsonl. It is append-only, safe for
// concurrent use within one process, and lossless in the sense that a
// process restart followed by a fresh Load returns every record that
// was ever successfully Append'ed.
//
// Multi-writer fan-in against the same session file from different
// processes is out of scope: use OS-level sticky routing or a shared
// Backend (Redis/Postgres) instead.
func NewJSONLBackend(dir string, opts ...JSONLOption) (Backend, error) {
	b := &jsonlBackend{
		dir:       dir,
		writers:   map[string]*jsonlWriter{},
		validate:  DefaultKeyValidator,
		fileMode:  0o644,
		dirMode:   0o755,
		onBadLine: swallowBadLine,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	if err := os.MkdirAll(dir, b.dirMode); err != nil {
		return nil, fmt.Errorf("sessionrecorder: mkdir %q: %w", dir, err)
	}
	return b, nil
}

const jsonlExt = ".jsonl"

type jsonlBackend struct {
	dir       string
	validate  KeyValidator
	fileMode  os.FileMode
	dirMode   os.FileMode
	onBadLine func(sessionKey string, lineNo int, raw []byte, err error) error

	mu      sync.Mutex
	writers map[string]*jsonlWriter
	closed  bool
}

type jsonlWriter struct {
	mu sync.Mutex
	f  *os.File
}

func (b *jsonlBackend) path(key string) string {
	return filepath.Join(b.dir, key+jsonlExt)
}

func (b *jsonlBackend) Load(_ context.Context, key string) ([]Record, error) {
	if err := b.validate(key); err != nil {
		return nil, err
	}
	f, err := os.Open(b.path(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	out := make([]Record, 0, 64)
	reader := bufio.NewReaderSize(f, 64*1024)
	var lineNo int
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := strings.TrimRight(string(line), "\r\n")
			if trimmed != "" {
				var rec Record
				if jerr := json.Unmarshal([]byte(trimmed), &rec); jerr != nil {
					if b.onBadLine == nil {
						return out, fmt.Errorf("sessionrecorder: jsonl: parse %s line %d: %w", key, lineNo, jerr)
					}
					if herr := b.onBadLine(key, lineNo, []byte(trimmed), jerr); herr != nil {
						return out, herr
					}
				} else {
					out = append(out, rec)
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return out, rerr
		}
	}
	// Ensure records are sorted by HostSeq: normal append-order matches
	// HostSeq ordering, but a concurrent misuse could produce out-of-order
	// files. Sorting on Load keeps downstream invariants.
	sort.SliceStable(out, func(i, j int) bool { return out[i].HostSeq < out[j].HostSeq })
	return out, nil
}

func (b *jsonlBackend) Append(_ context.Context, key string, r Record) error {
	if err := b.validate(key); err != nil {
		return err
	}
	w, err := b.writer(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(data); err != nil {
		return err
	}
	return nil
}

func (b *jsonlBackend) writer(key string) (*jsonlWriter, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("sessionrecorder: jsonl backend closed")
	}
	if w, ok := b.writers[key]; ok {
		return w, nil
	}
	f, err := os.OpenFile(b.path(key), os.O_APPEND|os.O_CREATE|os.O_WRONLY, b.fileMode)
	if err != nil {
		return nil, err
	}
	w := &jsonlWriter{f: f}
	b.writers[key] = w
	return w, nil
}

func (b *jsonlBackend) Sessions(ctx context.Context) ([]SessionInfo, error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, jsonlExt) {
			continue
		}
		key := strings.TrimSuffix(name, jsonlExt)
		if err := b.validate(key); err != nil {
			// Skip files that do not match the key policy; they are
			// likely unrelated and listing them would surface keys the
			// Recorder would refuse to read anyway.
			continue
		}
		info, err := e.Info()
		if err != nil {
			return out, err
		}
		last, recAt, lerr := b.tailMeta(ctx, key)
		if lerr != nil {
			return out, lerr
		}
		if recAt.IsZero() {
			recAt = info.ModTime()
		}
		out = append(out, SessionInfo{Key: key, LastSeq: last, RecordedAt: recAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	return out, nil
}

// tailMeta returns the last HostSeq and RecordedAt of a session file by
// loading its records. Conservative for a reference impl: correct over
// clever. Backends that need O(1) Sessions should replace this with a
// sidecar index or use a database Backend.
func (b *jsonlBackend) tailMeta(ctx context.Context, key string) (HostSeq, time.Time, error) {
	records, err := b.Load(ctx, key)
	if err != nil {
		return 0, time.Time{}, err
	}
	if n := len(records); n > 0 {
		return records[n-1].HostSeq, records[n-1].RecordedAt, nil
	}
	return 0, time.Time{}, nil
}

func (b *jsonlBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var firstErr error
	for k, w := range b.writers {
		w.mu.Lock()
		if err := w.f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.mu.Unlock()
		delete(b.writers, k)
	}
	return firstErr
}

func swallowBadLine(_ string, _ int, _ []byte, _ error) error {
	return nil
}
