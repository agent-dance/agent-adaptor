package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// errPersistentFallback tells adapter.Run that the persistent path could not
// serve this turn (unsupported platform, spawn failure, or a live process that
// died mid-turn) and it should transparently use the normal spawn path.
var errPersistentFallback = errors.New("claude: persistent process unavailable; falling back to per-run spawn")

// persistentIdleTimeout reaps a live process that has not received a turn for
// this long, so a long-idle session does not hold a claude subprocess forever.
const persistentIdleTimeout = 5 * time.Minute

// persistentSpec is everything the pool needs to spawn or reuse a live process
// and run exactly one turn on it. It is derived from a DriverRunRequest by
// adapter.Run; the pool never reaches back into SDK request types.
type persistentSpec struct {
	command   string
	model     string
	effort    string
	extraArgs []string
	cwd       string
	env       []agentadaptor.EnvBinding
	skipPerms bool
	streaming bool   // spawn with --include-partial-messages for token deltas
	resumeID  string // non-empty on resume runs; also the pool key
	prompt    string
}

// sig is the reuse signature: a live process may only serve a new turn when its
// spawn signature matches, otherwise config drifted (model/effort/cwd/
// streaming/...) and the process is evicted and respawned with --resume.
// streaming is part of the signature because it changes the spawn flags
// (--include-partial-messages), so a streaming and a non-streaming turn on the
// same session use distinct live processes rather than one contaminating the
// other's output shape.
func (s persistentSpec) sig() string {
	return strings.Join([]string{
		s.command, s.model, s.effort,
		strconv.FormatBool(s.skipPerms), strconv.FormatBool(s.streaming), s.cwd,
		strings.Join(s.extraArgs, "\x00"),
	}, "|")
}

// spawnArgs mirrors the stream-json input flags proven by scripts/claudebench:
// --input-format stream-json keeps the process alive to accept one NDJSON user
// frame per turn. Streaming turns add --include-partial-messages so the parser
// receives token deltas; non-streaming turns spawn without it so their output
// shape is byte-identical to the historical batch path. It omits the HITL
// permission/replay flags because the persistent path only serves
// non-interactive turns.
func (s persistentSpec) spawnArgs() []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	if s.streaming {
		args = append(args, "--include-partial-messages")
	}
	if s.resumeID != "" {
		args = append(args, "--resume", s.resumeID)
	}
	if s.skipPerms {
		args = append(args, "--dangerously-skip-permissions")
	}
	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}
	args = append(args, s.extraArgs...)
	return args
}

// persistentPool owns the live processes keyed by Claude session id (which is
// also the DriverSessionState.ResumeID). It is held by the adapter value and
// shared across Runs via the pointer field.
type persistentPool struct {
	mu   sync.Mutex
	live map[string]*liveProcess
}

func newPersistentPool() *persistentPool {
	return &persistentPool{live: map[string]*liveProcess{}}
}

// run serves one turn on a caller-supplied, pre-configured parser (the driver
// owns whether it streams / carries HITL context). It acquires (reuse or spawn)
// a live process, feeds the prompt, reads one turn's worth of NDJSON into the
// parser, then arms the idle timer. On any live-process failure it evicts and
// returns errPersistentFallback so the caller uses the spawn path.
func (pool *persistentPool) run(ctx context.Context, spec persistentSpec, sink agentadaptor.EventSink, parser *claudeParser) (agentadaptor.RawStreams, error) {
	lp, _, err := pool.acquire(ctx, spec, sink)
	if err != nil {
		return agentadaptor.RawStreams{}, errPersistentFallback
	}

	lp.turnMu.Lock()
	defer lp.turnMu.Unlock()
	lp.stopIdle()

	raw, err := lp.turn(ctx, spec.prompt, parser)
	if err != nil {
		pool.evict(lp)
		// A reused process that died between turns is expected occasionally;
		// the caller falls back to a fresh --resume spawn transparently.
		return raw, errPersistentFallback
	}

	// First turn of a fresh session self-registers under the discovered
	// session id so subsequent resume runs (ResumeID == that id) hit this
	// same live process.
	pool.register(lp, parser.sessionID)
	lp.startIdle()
	return raw, nil
}

// acquire returns a live process for the spec, reusing an existing one keyed by
// resumeID when the signature matches, otherwise spawning a new one.
func (pool *persistentPool) acquire(ctx context.Context, spec persistentSpec, sink agentadaptor.EventSink) (*liveProcess, bool, error) {
	if spec.resumeID != "" {
		pool.mu.Lock()
		if lp := pool.live[spec.resumeID]; lp != nil {
			match := lp.sig == spec.sig() && !lp.isClosed()
			pool.mu.Unlock()
			if match {
				return lp, true, nil
			}
			// Config drifted or the process is gone: drop it and respawn
			// with --resume so the conversation still continues.
			pool.evict(lp)
		} else {
			pool.mu.Unlock()
		}
	}
	lp, err := pool.spawn(ctx, spec, sink)
	if err != nil {
		return nil, false, err
	}
	return lp, false, nil
}

func (pool *persistentPool) spawn(_ context.Context, spec persistentSpec, sink agentadaptor.EventSink) (*liveProcess, error) {
	if runtime.GOOS == "windows" {
		// The Windows .cmd/.ps1 shim lives in clihelper; the persistent POC
		// stays POSIX-only and falls back on Windows.
		return nil, errPersistentFallback
	}

	// The process must outlive a single Run's ctx (that ctx is cancelled when
	// Run returns), so we give it a pool-owned context and kill it explicitly
	// on eviction. ConfigureCancellation wires the process-group kill onto
	// this context's cancel.
	procCtx, cancel := context.WithCancel(context.Background())
	args := spec.spawnArgs()
	cmd := exec.CommandContext(procCtx, spec.command, args...)
	processx.ConfigureCancellation(cmd)
	if spec.cwd != "" {
		cmd.Dir = spec.cwd
	}
	// ensureRootSandboxEnv injects IS_SANDBOX=1 when running as root with
	// --dangerously-skip-permissions, matching the spawn path in driver.go.
	cmd.Env = persistentEnv(ensureRootSandboxEnv(args, spec.env))

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
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	_ = sink.Emit(agentadaptor.RunEvent{
		Type:      agentadaptor.RunEventSpawn,
		Text:      "persistent claude spawned",
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"pid": cmd.Process.Pid, "persistent": true},
	})

	return &liveProcess{
		pool:   pool,
		sig:    spec.sig(),
		key:    spec.resumeID,
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 1<<20),
		stderr: stderr,
	}, nil
}

// register records lp under sessionID (idempotent), evicting any stale process
// previously registered for the same id.
func (pool *persistentPool) register(lp *liveProcess, sessionID string) {
	if sessionID == "" {
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if existing := pool.live[sessionID]; existing != nil && existing != lp {
		existing.kill()
	}
	lp.key = sessionID
	pool.live[sessionID] = lp
}

func (pool *persistentPool) evict(lp *liveProcess) {
	pool.mu.Lock()
	if lp.key != "" && pool.live[lp.key] == lp {
		delete(pool.live, lp.key)
	}
	pool.mu.Unlock()
	lp.kill()
}

// liveProcess is a single long-lived claude subprocess. turnMu serializes turns
// so only one NDJSON exchange is in flight at a time.
type liveProcess struct {
	turnMu sync.Mutex

	pool *persistentPool
	key  string // session id (== ResumeID) once known
	sig  string

	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *lockedBuffer

	idleMu    sync.Mutex
	idleTimer *time.Timer
	closed    bool
}

// turn writes one user frame and reads NDJSON until the turn's result frame,
// feeding the caller-supplied parser. A returned error means the process is
// unusable and the caller should fall back.
func (lp *liveProcess) turn(ctx context.Context, prompt string, parser *claudeParser) (agentadaptor.RawStreams, error) {
	frame, err := encodeInteractiveUserFrame(prompt)
	if err != nil {
		return agentadaptor.RawStreams{}, err
	}
	if _, err := io.WriteString(lp.stdin, frame); err != nil {
		return agentadaptor.RawStreams{}, err
	}

	var rawOut strings.Builder
	stderrStart := lp.stderr.len()

	done := make(chan struct{})
	var readErr error
	go func() {
		defer close(done)
		for {
			line, err := lp.stdout.ReadString('\n')
			if len(line) > 0 {
				rawOut.WriteString(line)
				_ = parser.onChunk("stdout", []byte(line), time.Now().UTC())
				if isResultLine(line) {
					return
				}
			}
			if err != nil {
				readErr = err
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return agentadaptor.RawStreams{}, ctx.Err()
	case <-done:
	}

	parser.finalize()
	raw := agentadaptor.RawStreams{Stdout: rawOut.String(), Stderr: lp.stderr.since(stderrStart)}
	if readErr != nil {
		// EOF/read error before the result frame means the process ended.
		return raw, readErr
	}
	return raw, nil
}

func (lp *liveProcess) startIdle() {
	lp.idleMu.Lock()
	defer lp.idleMu.Unlock()
	if lp.closed {
		return
	}
	lp.idleTimer = time.AfterFunc(persistentIdleTimeout, func() {
		lp.pool.evict(lp)
	})
}

func (lp *liveProcess) stopIdle() {
	lp.idleMu.Lock()
	defer lp.idleMu.Unlock()
	if lp.idleTimer != nil {
		lp.idleTimer.Stop()
		lp.idleTimer = nil
	}
}

func (lp *liveProcess) isClosed() bool {
	lp.idleMu.Lock()
	defer lp.idleMu.Unlock()
	return lp.closed
}

func (lp *liveProcess) kill() {
	lp.idleMu.Lock()
	if lp.closed {
		lp.idleMu.Unlock()
		return
	}
	lp.closed = true
	if lp.idleTimer != nil {
		lp.idleTimer.Stop()
		lp.idleTimer = nil
	}
	lp.idleMu.Unlock()

	_ = lp.stdin.Close()
	if lp.cancel != nil {
		lp.cancel() // fires ConfigureCancellation's process-group kill
	}
	go func() { _ = lp.cmd.Wait() }() // reap
}

// isResultLine cheaply detects a stream-json turn terminator without disturbing
// the parser (which also consumes the same line).
func isResultLine(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "{") {
		return false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(t), &probe) != nil {
		return false
	}
	return probe.Type == "result"
}

func persistentEnv(bindings []agentadaptor.EnvBinding) []string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		if k, v, ok := strings.Cut(item, "="); ok {
			env[k] = v
		}
	}
	for _, b := range bindings {
		env[b.Name] = b.Value
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// lockedBuffer is a tiny concurrency-safe byte sink for the persistent
// process's continuous stderr, letting each turn snapshot the delta that
// arrived during it.
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
