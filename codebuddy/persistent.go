package codebuddy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// errPersistentFallback is returned only while it is still safe to execute
// the prompt through the historical one-shot route. Once any byte of a user
// frame reaches CodeBuddy, a channel failure has an unknown outcome and must
// be surfaced instead of replayed.
var errPersistentFallback = errors.New("codebuddy: persistent process unavailable before prompt delivery")

var errPersistentPoolClosed = errors.New("codebuddy: persistent process pool closed")

var codeBuddyPersistentIdleTimeout = 5 * time.Minute

const defaultPersistentGracePeriod = 2 * time.Second

type persistentSpec struct {
	command   string
	model     string
	effort    string
	extraArgs []string
	cwd       string
	env       []driver.EnvBinding

	resumeID         string
	engineSessionID  string
	previousEngineID string
	prompt           string

	profileFingerprint  string
	settingsFingerprint string
	commandFingerprint  string
	gracePeriod         time.Duration
}

func (s persistentSpec) spawnArgs() []string {
	args := []string{"--input-format=stream-json", "--output-format=stream-json", "--verbose", "--include-partial-messages"}
	if s.resumeID != "" {
		args = append(args, "--resume", s.resumeID)
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}
	return append(args, codeBuddySafeExtraArgs(s.extraArgs, true)...)
}

// sig covers every value CodeBuddy may consume at process startup. resumeID
// is deliberately excluded: it identifies the same conversation after the
// first turn and must not force an immediate rebuild of the newly registered
// process. Full effective env values are hashed and never emitted.
func (s persistentSpec) sig() string {
	return persistentHashStrings(
		"codebuddy_persistent_v1",
		s.command,
		s.model,
		s.effort,
		strings.Join(codeBuddySafeExtraArgs(s.extraArgs, true), "\x00"),
		s.cwd,
		persistentHashStrings(codeBuddyPersistentEnv(s.env)...),
		s.profileFingerprint,
		s.settingsFingerprint,
		s.commandFingerprint,
	)
}

type persistentPool struct {
	mu      sync.Mutex
	live    map[string]*liveProcess
	engines map[string]*liveProcess
	all     map[*liveProcess]struct{}
	locks   map[string]*writerLock
	closed  bool

	spawnProcess func(persistentSpec, driver.EventSink) (*liveProcess, error)
}

type writerLock struct {
	mu   sync.Mutex
	refs int
}

func newPersistentPool() *persistentPool {
	return &persistentPool{
		live:    map[string]*liveProcess{},
		engines: map[string]*liveProcess{},
		all:     map[*liveProcess]struct{}{},
		locks:   map[string]*writerLock{},
	}
}

type persistentWriter struct {
	pool *persistentPool
	key  string
	lock *writerLock
	once sync.Once
}

func (p *persistentPool) lockWriter(key string) *persistentWriter {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	p.mu.Lock()
	l := p.locks[key]
	if l == nil {
		l = &writerLock{}
		p.locks[key] = l
	}
	l.refs++
	p.mu.Unlock()
	l.mu.Lock()
	return &persistentWriter{pool: p, key: key, lock: l}
}

func (w *persistentWriter) release() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		w.lock.mu.Unlock()
		w.pool.mu.Lock()
		w.lock.refs--
		if w.lock.refs == 0 && w.pool.locks[w.key] == w.lock {
			delete(w.pool.locks, w.key)
		}
		w.pool.mu.Unlock()
	})
}

func (w *persistentWriter) run(ctx context.Context, spec persistentSpec, sink driver.EventSink, p *parser) (driver.RawStreams, error) {
	if w == nil {
		return driver.RawStreams{}, errPersistentFallback
	}
	emitPersistentInvocation(sink, spec)
	if previous := w.pool.lookupEngine(spec.previousEngineID); previous != nil {
		w.pool.detach(previous)
		_ = previous.terminateAndWait(context.Background())
	}

	var lp *liveProcess
	if spec.resumeID != "" {
		lp = w.pool.lookup(spec.resumeID)
	} else if stale := w.pool.lookupEngine(spec.engineSessionID); stale != nil {
		// The v1 Thread coordinator deliberately omitted a resume selector, so
		// this is a new provider conversation even when the same host Thread is
		// being rebound. Stop its previous writer before launching the new one.
		w.pool.detach(stale)
		_ = stale.terminateAndWait(context.Background())
	}
	if lp != nil && (lp.sig != spec.sig() || lp.isClosed()) {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		lp = nil
	}
	if lp == nil {
		var err error
		lp, err = w.pool.spawnLive(spec, sink)
		if err != nil {
			return driver.RawStreams{}, errPersistentFallback
		}
	}

	lp.stopIdle()
	raw, sent, err := lp.turn(ctx, spec.prompt, sink, p)
	if err != nil {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		if !sent && ctx.Err() == nil {
			return raw, errPersistentFallback
		}
		return raw, err
	}

	// A provider-classified failed run must not leave a process whose head is
	// newer than the checkpoint the SDK will retain. Discard that writer so a
	// future run resumes through the persisted session state.
	if p.pendingFailure != nil || strings.TrimSpace(p.errorMessage) != "" {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		return raw, nil
	}
	if p.sessionID == "" {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		return raw, nil
	}
	for _, stale := range w.pool.register(lp, p.sessionID, spec.engineSessionID) {
		_ = stale.terminateAndWait(context.Background())
	}
	lp.startIdle()
	return raw, nil
}

// suspendAndWait implements the single-writer handoff: detach the live
// channel, close and terminate its whole process group, and wait for cmd.Wait
// before the caller is allowed to spawn a temporary writer.
func (w *persistentWriter) suspendAndWait(resumeID, engineSessionID, previousEngineID string) error {
	if w == nil {
		return nil
	}
	lp := w.pool.lookup(resumeID)
	if lp == nil {
		lp = w.pool.lookupEngine(engineSessionID)
	}
	if lp == nil {
		lp = w.pool.lookupEngine(previousEngineID)
	}
	if lp == nil {
		return nil
	}
	w.pool.detach(lp)
	return lp.terminateAndWait(context.Background())
}

func (w *persistentWriter) preWarm(spec persistentSpec, sink driver.EventSink) error {
	if w == nil || spec.resumeID == "" {
		return nil
	}
	if current := w.pool.lookup(spec.resumeID); current != nil {
		w.pool.detach(current)
		if err := current.terminateAndWait(context.Background()); err != nil {
			return err
		}
	}
	lp, err := w.pool.spawnLive(spec, sink)
	if err != nil {
		return err
	}
	for _, stale := range w.pool.register(lp, spec.resumeID, spec.engineSessionID) {
		_ = stale.terminateAndWait(context.Background())
	}
	lp.startIdle()
	return nil
}

func (p *persistentPool) spawnLive(spec persistentSpec, sink driver.EventSink) (*liveProcess, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPersistentPoolClosed
	}
	p.mu.Unlock()

	var (
		lp  *liveProcess
		err error
	)
	if p.spawnProcess != nil {
		lp, err = p.spawnProcess(spec, sink)
	} else {
		lp, err = p.spawn(spec, sink)
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = lp.terminateAndWait(context.Background())
		return nil, errPersistentPoolClosed
	}
	p.all[lp] = struct{}{}
	p.mu.Unlock()
	return lp, nil
}

func (p *persistentPool) lookup(resumeID string) *liveProcess {
	if resumeID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[resumeID]
}

func (p *persistentPool) lookupEngine(engineSessionID string) *liveProcess {
	if engineSessionID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.engines[engineSessionID]
}

func (p *persistentPool) register(lp *liveProcess, resumeID, engineSessionID string) []*liveProcess {
	if lp == nil || resumeID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if lp.key != "" && lp.key != resumeID && p.live[lp.key] == lp {
		delete(p.live, lp.key)
	}
	staleSet := map[*liveProcess]struct{}{}
	if stale := p.live[resumeID]; stale != nil && stale != lp {
		staleSet[stale] = struct{}{}
	}
	if engineSessionID != "" {
		if stale := p.engines[engineSessionID]; stale != nil && stale != lp {
			staleSet[stale] = struct{}{}
		}
		lp.engineKey = engineSessionID
		p.engines[engineSessionID] = lp
	}
	lp.key = resumeID
	p.live[resumeID] = lp
	stale := make([]*liveProcess, 0, len(staleSet))
	for process := range staleSet {
		stale = append(stale, process)
	}
	return stale
}

func (p *persistentPool) detach(lp *liveProcess) {
	if lp == nil {
		return
	}
	p.mu.Lock()
	if lp.key != "" && p.live[lp.key] == lp {
		delete(p.live, lp.key)
	}
	if lp.engineKey != "" && p.engines[lp.engineKey] == lp {
		delete(p.engines, lp.engineKey)
	}
	p.mu.Unlock()
}

func (p *persistentPool) close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	processes := make([]*liveProcess, 0, len(p.all))
	for lp := range p.all {
		processes = append(processes, lp)
	}
	p.live = map[string]*liveProcess{}
	p.engines = map[string]*liveProcess{}
	p.mu.Unlock()
	var errs []error
	for _, lp := range processes {
		if err := lp.gracefulStop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *persistentPool) evictIdle(lp *liveProcess) {
	if lp == nil {
		return
	}
	key := lp.key
	w := p.lockWriter(persistentResumeWriterKey(key))
	if w == nil {
		return
	}
	defer w.release()
	if current := p.lookup(key); current != lp {
		return
	}
	p.detach(lp)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = lp.terminateAndWait(ctx)
}

func (p *persistentPool) spawn(spec persistentSpec, sink driver.EventSink) (*liveProcess, error) {
	if runtime.GOOS == "windows" {
		return nil, errPersistentFallback
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, spec.command, spec.spawnArgs()...)
	processx.ConfigureCancellation(cmd)
	if spec.cwd != "" {
		cmd.Dir = spec.cwd
	}
	cmd.Env = codeBuddyPersistentEnv(spec.env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	lp := &liveProcess{
		pool:   p,
		sig:    spec.sig(),
		key:    spec.resumeID,
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 1<<20),
		stderr: &lockedBuffer{},
		grace:  spec.gracePeriod,
		waitCh: make(chan struct{}),
	}
	cmd.Stderr = persistentStderr{lp: lp}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	if sink != nil {
		_ = sink.Emit(driver.RunEvent{
			Type:      driver.RunEventSpawn,
			Text:      "persistent CodeBuddy spawned",
			Timestamp: time.Now().UTC(),
			Data: map[string]any{
				"pid":        cmd.Process.Pid,
				"persistent": true,
			},
		})
	}
	return lp, nil
}

type liveProcess struct {
	pool      *persistentPool
	sig       string
	key       string
	engineKey string

	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *lockedBuffer

	writeMu sync.Mutex

	activeMu sync.RWMutex
	active   *persistentTurnObserver

	stateMu     sync.Mutex
	closed      bool
	initialized bool
	idle        *time.Timer
	waitOnce    sync.Once
	waitCh      chan struct{}
	waitErr     error
	grace       time.Duration
}

type persistentTurnObserver struct {
	sink   driver.EventSink
	parser *parser
}

type persistentStderr struct{ lp *liveProcess }

func (s persistentStderr) Write(chunk []byte) (int, error) {
	if s.lp == nil {
		return len(chunk), nil
	}
	n, err := s.lp.stderr.Write(chunk)
	s.lp.activeMu.RLock()
	active := s.lp.active
	s.lp.activeMu.RUnlock()
	if active != nil && n > 0 {
		ts := time.Now().UTC()
		emitPersistentChunk(active.sink, "stderr", chunk[:n], ts)
		_ = active.parser.onChunk("stderr", chunk[:n], ts)
	}
	return n, err
}

type persistentControlStdin struct {
	lp *liveProcess

	mu         sync.Mutex
	promptSent bool
	writeErr   error
}

func (s *persistentControlStdin) Write(frame []byte) error {
	s.lp.writeMu.Lock()
	n, err := s.lp.stdin.Write(frame)
	s.lp.writeMu.Unlock()
	s.mu.Lock()
	if isControlUserFrame(frame) && n > 0 {
		s.promptSent = true
	}
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil && s.writeErr == nil {
		s.writeErr = err
	}
	s.mu.Unlock()
	return err
}

func (*persistentControlStdin) Close() error { return nil }

func (s *persistentControlStdin) status() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptSent, s.writeErr
}

func (lp *liveProcess) turn(ctx context.Context, prompt string, sink driver.EventSink, p *parser) (driver.RawStreams, bool, error) {
	if err := ctx.Err(); err != nil {
		return driver.RawStreams{}, false, err
	}
	if p == nil || p.control == nil {
		return driver.RawStreams{}, false, errors.New("codebuddy: persistent turn requires control parser")
	}
	ctrl := &persistentControlStdin{lp: lp}
	p.control.stdin = ctrl

	stderrStart := lp.stderr.len()
	lp.activeMu.Lock()
	lp.active = &persistentTurnObserver{sink: sink, parser: p}
	lp.activeMu.Unlock()
	defer func() {
		lp.activeMu.Lock()
		lp.active = nil
		lp.activeMu.Unlock()
	}()

	lp.stateMu.Lock()
	initialized := lp.initialized
	lp.stateMu.Unlock()
	if initialized {
		p.control.userStarted = true
		if err := ctrl.Write(encodeControlUser(prompt)); err != nil {
			sent, _ := ctrl.status()
			return driver.RawStreams{Stderr: lp.stderr.since(stderrStart)}, sent, err
		}
	} else if err := ctrl.Write(mustEncodeControlInitialize()); err != nil {
		return driver.RawStreams{Stderr: lp.stderr.since(stderrStart)}, false, err
	}

	type readResult struct {
		stdout string
		err    error
		result bool
	}
	done := make(chan readResult, 1)
	go func() {
		var raw strings.Builder
		for {
			line, readErr := lp.stdout.ReadString('\n')
			if len(line) > 0 {
				raw.WriteString(line)
				ts := time.Now().UTC()
				emitPersistentChunk(sink, "stdout", []byte(line), ts)
				if err := p.onChunk("stdout", []byte(line), ts); err != nil {
					done <- readResult{stdout: raw.String(), err: err}
					return
				}
				if _, writeErr := ctrl.status(); writeErr != nil {
					done <- readResult{stdout: raw.String(), err: writeErr}
					return
				}
				if isResultLine(line) {
					done <- readResult{stdout: raw.String(), result: true}
					return
				}
			}
			if readErr != nil {
				done <- readResult{stdout: raw.String(), err: readErr}
				return
			}
		}
	}()

	var rr readResult
	select {
	case rr = <-done:
	case <-ctx.Done():
		lp.signalTerminate()
		rr = <-done
		rr.err = ctx.Err()
	}
	p.finalize()
	sent, writeErr := ctrl.status()
	if rr.err == nil {
		rr.err = writeErr
	}
	raw := driver.RawStreams{Stdout: rr.stdout, Stderr: lp.stderr.since(stderrStart)}
	if !rr.result {
		if rr.err == nil {
			rr.err = io.ErrUnexpectedEOF
		}
		return raw, sent, rr.err
	}
	if rr.err != nil {
		return raw, sent, rr.err
	}
	if !sent {
		return raw, false, errors.New("codebuddy: terminal result arrived before user prompt delivery")
	}
	lp.stateMu.Lock()
	lp.initialized = true
	lp.stateMu.Unlock()
	return raw, true, nil
}

func (lp *liveProcess) startIdle() {
	lp.stateMu.Lock()
	defer lp.stateMu.Unlock()
	if lp.closed {
		return
	}
	if lp.idle != nil {
		lp.idle.Stop()
	}
	lp.idle = time.AfterFunc(codeBuddyPersistentIdleTimeout, func() { lp.pool.evictIdle(lp) })
}

func (lp *liveProcess) stopIdle() {
	lp.stateMu.Lock()
	defer lp.stateMu.Unlock()
	if lp.idle != nil {
		lp.idle.Stop()
		lp.idle = nil
	}
}

func (lp *liveProcess) isClosed() bool {
	lp.stateMu.Lock()
	defer lp.stateMu.Unlock()
	return lp.closed
}

func (lp *liveProcess) signalTerminate() {
	lp.beginClose()
	if lp.cancel != nil {
		lp.cancel()
	}
}

func (lp *liveProcess) beginClose() {
	lp.stateMu.Lock()
	if lp.closed {
		lp.stateMu.Unlock()
		return
	}
	lp.closed = true
	if lp.idle != nil {
		lp.idle.Stop()
		lp.idle = nil
	}
	lp.stateMu.Unlock()
	_ = lp.stdin.Close()
}

func (lp *liveProcess) startWait() {
	lp.waitOnce.Do(func() {
		go func() {
			lp.waitErr = lp.cmd.Wait()
			lp.pool.mu.Lock()
			delete(lp.pool.all, lp)
			lp.pool.mu.Unlock()
			close(lp.waitCh)
		}()
	})
}

func (lp *liveProcess) terminateAndWait(ctx context.Context) error {
	if lp == nil {
		return nil
	}
	lp.signalTerminate()
	lp.startWait()
	select {
	case <-lp.waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (lp *liveProcess) gracefulStop(ctx context.Context) error {
	if lp == nil {
		return nil
	}
	lp.beginClose()
	lp.startWait()
	grace := lp.grace
	if grace <= 0 {
		grace = defaultPersistentGracePeriod
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-lp.waitCh:
		return nil
	case <-ctx.Done():
		if lp.cancel != nil {
			lp.cancel()
		}
		return ctx.Err()
	case <-timer.C:
		if lp.cancel != nil {
			lp.cancel()
		}
	}
	select {
	case <-lp.waitCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func persistentSessionKey(req driver.Request) string {
	if req.Session == nil {
		return ""
	}
	return strings.TrimSpace(req.Session.EngineSessionID)
}

func persistentPreviousSessionKey(req driver.Request) string {
	if req.Session == nil {
		return ""
	}
	return strings.TrimSpace(req.Session.PreviousID)
}

func persistentWriterKey(req driver.Request) string {
	if resumeID := codeBuddyResumeID(req); resumeID != "" {
		return persistentResumeWriterKey(resumeID)
	}
	if engineID := persistentSessionKey(req); engineID != "" {
		return "engine:" + engineID
	}
	return ""
}

func persistentResumeWriterKey(resumeID string) string {
	if strings.TrimSpace(resumeID) == "" {
		return ""
	}
	return "resume:" + strings.TrimSpace(resumeID)
}

func codeBuddyResumeID(req driver.Request) string {
	if req.Session == nil || req.Session.State == nil {
		return ""
	}
	return strings.TrimSpace(req.Session.State.ResumeID)
}

func persistentEligible(cfg Config, req driver.Request) bool {
	if persistentSessionKey(req) == "" || cfg.MaxTurnsPerRun > 0 {
		return false
	}
	if req.OutputSchema != nil && req.OutputSchema.Mode != driver.StructuredOutputPromptValidate {
		return false
	}
	for _, spec := range req.Runtime.Requested {
		if spec.Lifecycle != driver.RuntimeLifecycleShared {
			return false
		}
	}
	for _, ref := range req.Runtime.Ensured {
		if ref.Lifecycle != driver.RuntimeLifecycleShared {
			return false
		}
	}
	return true
}

func persistentPreWarmEligible(cfg Config, req driver.Request) bool {
	copyReq := req
	copyReq.OutputSchema = nil
	return persistentEligible(cfg, copyReq)
}

func appendCodeBuddyEntrypoint(env []driver.EnvBinding) []driver.EnvBinding {
	out := append([]driver.EnvBinding(nil), env...)
	return append(out, driver.EnvBinding{Name: "CODEBUDDY_CODE_ENTRYPOINT", Value: "sdk-py"})
}

func emitPersistentInvocation(sink driver.EventSink, spec persistentSpec) {
	if sink == nil {
		return
	}
	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventInvocation,
		Text:      "starting persistent CodeBuddy turn",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"command":    spec.command,
			"args":       append([]string(nil), spec.spawnArgs()...),
			"cwd":        spec.cwd,
			"env_keys":   persistentEnvKeys(spec.env),
			"persistent": true,
		},
	})
}

func emitPersistentChunk(sink driver.EventSink, stream string, chunk []byte, ts time.Time) {
	if sink == nil || len(chunk) == 0 {
		return
	}
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventChunk, Timestamp: ts, Stream: stream, Bytes: append([]byte(nil), chunk...)})
}

func isResultLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	var probe struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(trimmed), &probe) == nil && strings.EqualFold(probe.Type, "result")
}

func isControlUserFrame(frame []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(frame, &probe) == nil && strings.EqualFold(probe.Type, "user")
}

func codeBuddyPersistentEnv(bindings []driver.EnvBinding) []string {
	values := map[string]string{}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	for _, binding := range bindings {
		values[binding.Name] = binding.Value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func persistentEnvKeys(bindings []driver.EnvBinding) []string {
	keys := make([]string, 0, len(bindings))
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		if binding.Name == "" {
			continue
		}
		if _, ok := seen[binding.Name]; ok {
			continue
		}
		seen[binding.Name] = struct{}{}
		keys = append(keys, binding.Name)
	}
	sort.Strings(keys)
	return keys
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *lockedBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

func (b *lockedBuffer) since(start int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if start < 0 || start > len(b.buf) {
		start = len(b.buf)
	}
	return string(b.buf[start:])
}

func persistentHashStrings(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func commandFileFingerprint(command string) string {
	resolved, err := exec.LookPath(command)
	if err != nil {
		return persistentHashStrings("lookpath-error", command, err.Error())
	}
	if target, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = target
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return persistentHashStrings("stat-error", resolved, err.Error())
	}
	h := sha256.New()
	_, _ = io.WriteString(h, resolved)
	_, _ = io.WriteString(h, fmt.Sprintf("\x00%d\x00%d\x00%s", info.Size(), info.ModTime().UnixNano(), info.Mode()))
	file, err := os.Open(resolved)
	if err != nil {
		_, _ = io.WriteString(h, "\x00open-error:"+err.Error())
	} else {
		_, _ = io.Copy(h, file)
		_ = file.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

// codeBuddySettingsFingerprint covers provider/profile/project inputs that
// are read when the control process starts. Desired resource fingerprints are
// folded into persistentSpec separately; this hashes the materialized and
// ambient files, including symlink targets, so out-of-band edits also rebuild.
func codeBuddySettingsFingerprint(bindings []driver.EnvBinding, cwd, profileDir string, extraArgs []string) string {
	paths := []string{
		filepath.Join(profileDir, "settings.json"),
		filepath.Join(profileDir, "settings.local.json"),
		filepath.Join(profileDir, ".mcp.json"),
		filepath.Join(profileDir, "mcp.json"),
		filepath.Join(profileDir, "CODEBUDDY.md"),
		filepath.Join(profileDir, ".credentials.json"),
		filepath.Join(profileDir, "credentials.json"),
	}
	// plugins is intentionally excluded: CodeBuddy populates and refreshes its
	// marketplace cache after startup. Hashing that self-managed cache would
	// rebuild an otherwise healthy channel after every cold first turn. SDK-
	// managed skill/MCP/config content remains covered here and by the desired
	// profile fingerprint.
	for _, name := range []string{"skills", "commands", "agents", "hooks"} {
		paths = append(paths, filepath.Join(profileDir, name))
	}
	if cwd != "" {
		current := filepath.Clean(cwd)
		for {
			paths = append(paths,
				filepath.Join(current, "CODEBUDDY.md"),
				filepath.Join(current, ".codebuddy", "settings.json"),
				filepath.Join(current, ".codebuddy", "settings.local.json"),
				filepath.Join(current, ".codebuddy", ".mcp.json"),
				filepath.Join(current, ".codebuddy", "mcp.json"),
				filepath.Join(current, ".codebuddy", "skills"),
				filepath.Join(current, ".codebuddy", "commands"),
				filepath.Join(current, ".codebuddy", "agents"),
			)
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	paths = append(paths, referencedArgPaths(cwd, extraArgs)...)
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		path = filepath.Clean(path)
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	h := sha256.New()
	_, _ = io.WriteString(h, "binding-profile:"+resolveConfigDir(bindings)+"\x00")
	seen := map[string]bool{}
	for _, path := range unique {
		writePathFingerprint(h, path, seen)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func referencedArgPaths(cwd string, args []string) []string {
	paths := make([]string, 0)
	for _, arg := range args {
		candidate := arg
		if _, value, ok := strings.Cut(arg, "="); ok {
			candidate = value
		}
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) && cwd != "" {
			candidate = filepath.Join(cwd, candidate)
		}
		if _, err := os.Lstat(candidate); err == nil {
			paths = append(paths, candidate)
		}
	}
	return paths
}

func writePathFingerprint(h io.Writer, logicalPath string, seen map[string]bool) {
	_, _ = io.WriteString(h, "path:"+logicalPath+"\x00")
	resolved, err := filepath.EvalSymlinks(logicalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _ = io.WriteString(h, "missing\x00")
		} else {
			_, _ = io.WriteString(h, "resolve-error:"+err.Error()+"\x00")
		}
		return
	}
	resolved = filepath.Clean(resolved)
	if seen[resolved] {
		_, _ = io.WriteString(h, "seen:"+resolved+"\x00")
		return
	}
	seen[resolved] = true
	defer delete(seen, resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		_, _ = io.WriteString(h, "stat-error:"+err.Error()+"\x00")
		return
	}
	if !info.IsDir() {
		_, _ = io.WriteString(h, info.Mode().String()+"\x00")
		raw, err := os.ReadFile(resolved)
		if err != nil {
			_, _ = io.WriteString(h, "read-error:"+err.Error()+"\x00")
		} else {
			_, _ = h.Write(raw)
			_, _ = io.WriteString(h, "\x00")
		}
		return
	}
	_ = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		rel, relErr := filepath.Rel(resolved, path)
		if relErr != nil {
			rel = path
		}
		_, _ = io.WriteString(h, rel+"\x00")
		if walkErr != nil {
			_, _ = io.WriteString(h, "walk-error:"+walkErr.Error()+"\x00")
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			writePathFingerprint(h, path, seen)
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			_, _ = io.WriteString(h, "read-error:"+err.Error()+"\x00")
		} else {
			_, _ = h.Write(raw)
			_, _ = io.WriteString(h, "\x00")
		}
		return nil
	})
}
