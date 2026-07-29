package claude

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// errPersistentFallback is intentionally returned only when the persistent
// path failed before any prompt byte was accepted by the child. Once a prompt
// may have reached the CLI, replaying it could duplicate tool side effects and
// the original error must be surfaced instead.
var errPersistentFallback = errors.New("claude: persistent process unavailable before prompt delivery")

var errPersistentPoolClosed = errors.New("claude: persistent process pool closed")

var persistentIdleTimeout = 5 * time.Minute

const defaultPersistentGracePeriod = 2 * time.Second

type interactiveBinder func(stdin interactiveStdin)

type nonClosingStdin struct{ w io.Writer }

func (s nonClosingStdin) Write(frame []byte) error {
	_, err := s.w.Write(frame)
	return err
}

func (nonClosingStdin) Close() error { return nil }

// persistentSpec contains only process-start and per-turn data derived by the
// adapter. The pool never reaches back into DriverRunRequest or SDK defaults.
type persistentSpec struct {
	command     string
	model       string
	effort      string
	extraArgs   []string
	cwd         string
	env         []driver.EnvBinding
	skipPerms   bool
	browser     bool
	streaming   bool
	interactive bool
	resumeID    string
	engineID    string
	previousID  string
	prompt      string

	profileFingerprint  string
	settingsFingerprint string
	commandFingerprint  string
	gracePeriod         time.Duration
}

func (s persistentSpec) spawnArgs() []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	if s.streaming || s.interactive {
		args = append(args, "--include-partial-messages")
	}
	if s.interactive {
		args = append(args, "--replay-user-messages", "--permission-prompt-tool", "stdio")
	}
	if s.resumeID != "" {
		args = append(args, "--resume", s.resumeID)
	}
	if s.skipPerms && !s.interactive {
		args = append(args, "--dangerously-skip-permissions")
	}
	if s.browser {
		args = append(args, "--chrome")
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}
	return append(args, s.extraArgs...)
}

// sig covers every value Claude reads at process start. Full effective env is
// hashed (not retained in plaintext), including runtime-secret bindings and
// ambient variables; profileFingerprint covers desired skills/MCP/config;
// settingsFingerprint covers provider/project files after reconciliation; and
// commandFingerprint identifies the resolved CLI file/version.
func (s persistentSpec) sig() string {
	args := s.spawnArgs()
	env := persistentEnv(ensureRootSandboxEnv(args, s.env))
	return hashStrings(
		"claude_persistent_v2",
		s.command,
		s.model,
		s.effort,
		strconv.FormatBool(s.skipPerms),
		strconv.FormatBool(s.browser),
		strconv.FormatBool(s.streaming),
		strconv.FormatBool(s.interactive),
		s.cwd,
		strings.Join(s.extraArgs, "\x00"),
		hashStrings(env...),
		s.profileFingerprint,
		s.settingsFingerprint,
		s.commandFingerprint,
	)
}

type persistentPool struct {
	mu      sync.Mutex
	live    map[string]*liveProcess // provider resume ID -> sole writer process
	engines map[string]*liveProcess // engine record ID -> process for atomic rebind handoff
	all     map[*liveProcess]struct{}
	locks   map[string]*writerLock
	closed  bool

	// spawnProcess is a narrow test seam. Production leaves it nil and uses
	// spawn; tests use it to prove pre-delivery fallback deterministically.
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

// persistentWriter is the single-writer lease shared by persistent turns and
// temporary one-shot handoffs for one provider resume ID.
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

func (w *persistentWriter) run(ctx context.Context, spec persistentSpec, sink driver.EventSink, parser *claudeParser, bind interactiveBinder) (driver.RawStreams, error) {
	if w == nil {
		return driver.RawStreams{}, errPersistentFallback
	}
	emitPersistentInvocation(sink, spec)
	if previous := w.pool.lookupEngine(spec.previousID); previous != nil {
		w.pool.detach(previous)
		_ = previous.terminateAndWait(context.Background())
	}

	lp := w.pool.lookup(spec.resumeID)
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
	raw, sent, err := lp.turn(ctx, spec.prompt, sink, parser, bind)
	if err != nil {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		if !sent && ctx.Err() == nil {
			return raw, errPersistentFallback
		}
		return raw, err
	}

	if parser.sessionID == "" {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		return raw, nil
	}
	for _, stale := range w.pool.register(lp, parser.sessionID, spec.engineID) {
		_ = stale.terminateAndWait(context.Background())
	}
	lp.startIdle()
	return raw, nil
}

// suspendAndWait removes the live writer before terminating it and does not
// return until cmd.Wait confirms the old process has exited. Callers hold the
// matching persistentWriter for the entire temporary spawn, preventing a new
// writer from appearing in the gap.
func (w *persistentWriter) suspendAndWait(resumeID, engineID, previousID string) error {
	if w == nil {
		return nil
	}
	lp := w.pool.lookup(resumeID)
	if lp == nil {
		lp = w.pool.lookupEngine(engineID)
	}
	if lp == nil {
		lp = w.pool.lookupEngine(previousID)
	}
	if lp == nil {
		return nil
	}
	w.pool.detach(lp)
	return lp.terminateAndWait(context.Background())
}

// preWarm starts the normal chat-shaped process with the newest checkpoint
// but sends no user frame. Claude emits no ready event, so successful Start is
// the only synchronous signal; first-turn I/O still handles an early death.
func (w *persistentWriter) preWarm(spec persistentSpec, sink driver.EventSink) error {
	if w == nil || spec.resumeID == "" {
		return nil
	}
	lp, err := w.pool.spawnLive(spec, sink)
	if err != nil {
		return err
	}
	for _, stale := range w.pool.register(lp, spec.resumeID, spec.engineID) {
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

func (p *persistentPool) lookupEngine(engineID string) *liveProcess {
	if engineID == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.engines[engineID]
}

func (p *persistentPool) register(lp *liveProcess, resumeID, engineID string) []*liveProcess {
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
	if engineID != "" {
		if stale := p.engines[engineID]; stale != nil && stale != lp {
			staleSet[stale] = struct{}{}
		}
		lp.engineKey = engineID
		p.engines[engineID] = lp
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
	cmd.Env = persistentEnv(ensureRootSandboxEnv(spec.spawnArgs(), spec.env))

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
	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventSpawn,
		Text:      "persistent claude spawned",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"pid":        cmd.Process.Pid,
			"persistent": true,
		},
	})
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

	activeMu sync.RWMutex
	active   *persistentTurnObserver

	stateMu  sync.Mutex
	closed   bool
	idle     *time.Timer
	waitOnce sync.Once
	waitCh   chan struct{}
	waitErr  error
	grace    time.Duration
}

type persistentTurnObserver struct {
	sink   driver.EventSink
	parser *claudeParser
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

func (lp *liveProcess) turn(ctx context.Context, prompt string, sink driver.EventSink, parser *claudeParser, bind interactiveBinder) (driver.RawStreams, bool, error) {
	if err := ctx.Err(); err != nil {
		return driver.RawStreams{}, false, err
	}
	if bind != nil {
		bind(nonClosingStdin{w: lp.stdin})
	}
	frame, err := encodeInteractiveUserFrame(prompt)
	if err != nil {
		return driver.RawStreams{}, false, err
	}

	stderrStart := lp.stderr.len()
	lp.activeMu.Lock()
	lp.active = &persistentTurnObserver{sink: sink, parser: parser}
	lp.activeMu.Unlock()
	defer func() {
		lp.activeMu.Lock()
		lp.active = nil
		lp.activeMu.Unlock()
	}()

	n, writeErr := io.WriteString(lp.stdin, frame)
	promptSent := n > 0
	if writeErr != nil {
		return driver.RawStreams{Stderr: lp.stderr.since(stderrStart)}, promptSent, writeErr
	}
	if n != len(frame) {
		return driver.RawStreams{Stderr: lp.stderr.since(stderrStart)}, promptSent, io.ErrShortWrite
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
				if err := parser.onChunk("stdout", []byte(line), ts); err != nil {
					done <- readResult{stdout: raw.String(), err: err}
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
	parser.finalize()
	raw := driver.RawStreams{
		Stdout: rr.stdout,
		Stderr: lp.stderr.since(stderrStart),
	}
	if !rr.result {
		if rr.err == nil {
			rr.err = io.ErrUnexpectedEOF
		}
		return raw, promptSent, rr.err
	}
	return raw, promptSent, nil
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
	lp.idle = time.AfterFunc(persistentIdleTimeout, func() { lp.pool.evictIdle(lp) })
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
		// The process is deliberately killed as part of handoff/eviction, so an
		// ExitError (typically "signal: killed") is expected. The contract here
		// is confirmation of exit, not a successful CLI status.
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

func emitPersistentInvocation(sink driver.EventSink, spec persistentSpec) {
	if sink == nil {
		return
	}
	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventInvocation,
		Text:      "starting persistent command turn",
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
	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventChunk,
		Timestamp: ts,
		Stream:    stream,
		Bytes:     append([]byte(nil), chunk...),
	})
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

func persistentEnv(bindings []driver.EnvBinding) []string {
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

func persistentResumeWriterKey(resumeID string) string {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID == "" {
		return ""
	}
	return "resume:" + resumeID
}

func hashStrings(parts ...string) string {
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
		return hashStrings("lookpath-error", command, err.Error())
	}
	if target, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = target
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return hashStrings("stat-error", resolved, err.Error())
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

// claudeSettingsFingerprint hashes every known profile/project/managed file
// Claude may read at startup. Missing paths are included so creation/removal is
// a signature change. Desired resource fingerprints are folded separately.
func claudeSettingsFingerprint(bindings []driver.EnvBinding, cwd, profileDir string) string {
	paths := append([]string(nil), claudeConfigCandidates(bindings)...)
	treeRoots := []string{
		filepath.Join(profileDir, "skills"),
		filepath.Join(profileDir, "agents"),
		filepath.Join(profileDir, "commands"),
		filepath.Join(profileDir, "hooks"),
	}
	for _, name := range []string{"settings.json", "settings.local.json", "config.json", ".claude.json", ".mcp.json"} {
		paths = append(paths, filepath.Join(profileDir, name))
	}
	if cwd != "" {
		current := filepath.Clean(cwd)
		for {
			for _, rel := range []string{
				filepath.Join(".claude", "settings.json"),
				filepath.Join(".claude", "settings.local.json"),
				".mcp.json",
				"CLAUDE.md",
			} {
				paths = append(paths, filepath.Join(current, rel))
			}
			for _, rel := range []string{
				filepath.Join(".claude", "skills"),
				filepath.Join(".claude", "agents"),
				filepath.Join(".claude", "commands"),
				filepath.Join(".claude", "hooks"),
			} {
				treeRoots = append(treeRoots, filepath.Join(current, rel))
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, "/Library/Application Support/ClaudeCode/managed-settings.json")
	} else if runtime.GOOS != "windows" {
		paths = append(paths, "/etc/claude-code/managed-settings.json")
	}
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	h := sha256.New()
	for _, path := range unique {
		_, _ = io.WriteString(h, path)
		_, _ = h.Write([]byte{0})
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				_, _ = io.WriteString(h, "missing")
			} else {
				_, _ = io.WriteString(h, "read-error:"+err.Error())
			}
		} else {
			_, _ = h.Write(raw)
		}
		_, _ = h.Write([]byte{0})
	}
	sort.Strings(treeRoots)
	for _, root := range treeRoots {
		_, _ = io.WriteString(h, "tree:"+root)
		_, _ = h.Write([]byte{0})
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				_, _ = io.WriteString(h, "walk-error:"+path+":"+walkErr.Error())
				_, _ = h.Write([]byte{0})
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			_, _ = io.WriteString(h, rel)
			_, _ = h.Write([]byte{0})
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				_, _ = io.WriteString(h, "read-error:"+err.Error())
			} else {
				_, _ = h.Write(raw)
			}
			_, _ = h.Write([]byte{0})
			return nil
		})
		if errors.Is(err, os.ErrNotExist) {
			_, _ = io.WriteString(h, "missing")
			_, _ = h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
