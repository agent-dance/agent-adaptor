package opencode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

var errServerPoolClosed = errors.New("opencode: server pool is closed")

const serverIdleTimeout = 5 * time.Minute

// Aliases identify the one writer for an engine record or provider session.
// A slot stays busy throughout stop/wait/replacement, including WithSpawn.
// Different Thread slots never serialize each other's execution.
type serverPool struct {
	mu          sync.Mutex
	aliases     map[string]*serverSlot
	all         map[*serverSlot]struct{}
	closed      chan struct{}
	idleTimeout time.Duration
}
type serverSlot struct {
	process   *serverProcess
	signature string
	busy      bool
	changed   chan struct{}
	aliases   []string
	timer     *time.Timer
}
type serverLease struct {
	pool       *serverPool
	slot       *serverSlot
	persistent bool
}

func newServerPool() *serverPool {
	return &serverPool{aliases: map[string]*serverSlot{}, all: map[*serverSlot]struct{}{}, closed: make(chan struct{}), idleTimeout: serverIdleTimeout}
}
func (p *serverPool) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func serverKeys(req driver.Request) []string {
	if req.Session == nil || req.Session.Mode == driver.SessionStateless {
		return nil
	}
	var keys []string
	if req.Session.EngineSessionID != "" {
		keys = append(keys, "engine:"+req.Session.EngineSessionID)
	}
	if req.Session.PreviousID != "" {
		keys = append(keys, "engine:"+req.Session.PreviousID)
	}
	if req.Session.Mode != driver.SessionFork && req.Session.State != nil && req.Session.State.ResumeID != "" {
		keys = append(keys, "session:"+req.Session.State.ResumeID)
	}
	return keys
}
func (p *serverPool) acquire(ctx context.Context, req driver.Request) (*serverLease, error) {
	keys := serverKeys(req)
	for {
		p.mu.Lock()
		if p.isClosed() {
			p.mu.Unlock()
			return nil, errServerPoolClosed
		}
		var slot *serverSlot
		for _, key := range keys {
			if candidate := p.aliases[key]; candidate != nil {
				if slot != nil && slot != candidate {
					p.mu.Unlock()
					return nil, errors.New("opencode: conflicting live session writers")
				}
				slot = candidate
			}
		}
		if slot != nil && slot.busy {
			changed := slot.changed
			p.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			case <-p.closed:
				return nil, errServerPoolClosed
			}
		}
		if slot == nil {
			slot = &serverSlot{}
			p.all[slot] = struct{}{}
		}
		if slot.timer != nil {
			slot.timer.Stop()
			slot.timer = nil
		}
		slot.busy = true
		slot.changed = make(chan struct{})
		for _, key := range keys {
			if p.aliases[key] == nil {
				p.aliases[key] = slot
				slot.aliases = append(slot.aliases, key)
			}
		}
		p.mu.Unlock()
		return &serverLease{pool: p, slot: slot, persistent: req.Session != nil && req.Session.EngineSessionID != "" && req.Session.Mode != driver.SessionStateless && !req.Spawn}, nil
	}
}
func (l *serverLease) register(resumeID string) error {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	if l.pool.isClosed() {
		return errServerPoolClosed
	}
	key := "session:" + resumeID
	if old := l.pool.aliases[key]; old != nil && old != l.slot {
		return errors.New("opencode: provider returned a session already owned by another writer")
	}
	if l.pool.aliases[key] == nil {
		l.pool.aliases[key] = l.slot
		l.slot.aliases = append(l.slot.aliases, key)
	}
	return nil
}
func (l *serverLease) release() {
	p, s := l.pool, l.slot
	p.mu.Lock()
	defer p.mu.Unlock()
	s.busy = false
	close(s.changed)
	if s.process == nil || p.isClosed() {
		for _, key := range s.aliases {
			if p.aliases[key] == s {
				delete(p.aliases, key)
			}
		}
		if s.process == nil || !s.process.alive() {
			delete(p.all, s)
		}
		return
	}
	s.timer = time.AfterFunc(p.idleTimeout, func() { p.evict(s) })
}
func (p *serverPool) evict(s *serverSlot) {
	p.mu.Lock()
	if s.busy || p.isClosed() {
		p.mu.Unlock()
		return
	}
	if _, ok := p.all[s]; !ok {
		p.mu.Unlock()
		return
	}
	s.busy = true
	s.changed = make(chan struct{})
	s.timer = nil
	p.mu.Unlock()
	l := &serverLease{pool: p, slot: s}
	_ = l.stop(context.Background())
	l.release()
}
func (l *serverLease) stop(ctx context.Context) error {
	l.pool.mu.Lock()
	proc := l.slot.process
	l.pool.mu.Unlock()
	if proc == nil {
		return nil
	}
	if err := proc.stop(ctx); err != nil {
		return err
	}
	l.pool.mu.Lock()
	if l.slot.process == proc {
		l.slot.process = nil
	}
	l.pool.mu.Unlock()
	return nil
}
func (p *serverPool) close(ctx context.Context) error {
	p.mu.Lock()
	if !p.isClosed() {
		close(p.closed)
	}
	var processes []*serverProcess
	for s := range p.all {
		if s.timer != nil {
			s.timer.Stop()
		}
		if s.process != nil {
			processes = append(processes, s.process)
		}
	}
	p.mu.Unlock()
	// Cancel everyone before waiting so one slow child cannot delay another.
	for _, proc := range processes {
		proc.cancel()
	}
	var errs []error
	for _, proc := range processes {
		if err := proc.stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Full effective environment is hashed, never retained as printable metadata.
// Process-only flags and the CLI file join resolved invocation/profile guards.
func serverSignature(c Config, cwd string, env []driver.EnvBinding, guards map[string]string) (string, error) {
	effective := map[string]string{}
	key := func(s string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(s)
		}
		return s
	}
	for _, binding := range os.Environ() {
		k, v, ok := strings.Cut(binding, "=")
		if ok {
			effective[key(k)] = v
		}
	}
	for _, binding := range env {
		effective[key(binding.Name)] = binding.Value
	}
	command := c.Command
	if command == "" {
		command = "opencode"
	}
	var commandFile string
	if resolved, err := exec.LookPath(command); err == nil {
		commandFile, err = driverutil.NativeFilesFingerprint("opencode/command/v1", resolved)
		if err != nil {
			return "", err
		}
	}
	return driver.CanonicalSessionConfigFingerprint("opencode/server-process/v1", struct {
		Config      Config
		CWD         string
		Env, Guards map[string]string
		CommandFile string
	}{c, cwd, effective, guards, commandFile})
}

type serverProcess struct {
	permissionsVerified bool
	client              *serverClient
	transport           *http.Transport
	cancel              context.CancelFunc
	done                chan struct{}
	result              clihelper.CommandResult
	err                 error
	output              processOutput
}

// The process helper emits only operational events. Every turn binds its own
// sink and takes its own raw-byte audit; idle bytes remain pending for the next
// invocation instead of being silently dropped or sent to a closed sink.
type processOutput struct {
	mu             sync.Mutex
	sink           driver.EventSink
	stdout, stderr strings.Builder
	err            error
	pid            any
}

func (o *processOutput) Emit(event driver.RunEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if event.Type == driver.RunEventChunk {
		if event.Stream == "stdout" {
			o.stdout.Write(event.Bytes)
		} else if event.Stream == "stderr" {
			o.stderr.Write(event.Bytes)
		}
	}
	if event.Type == driver.RunEventSpawn {
		o.pid = event.Data["pid"]
	}
	if o.sink != nil {
		err := o.sink.Emit(event)
		o.err = errors.Join(o.err, err)
		return err
	}
	return nil
}
func (o *processOutput) EmitStream(driver.StreamPayload) error {
	return errors.New("opencode: process transport cannot emit semantic events")
}
func (o *processOutput) begin(sink driver.EventSink) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sink = sink
	o.err = nil
	for _, chunk := range []struct{ stream, text string }{{"stdout", o.stdout.String()}, {"stderr", o.stderr.String()}} {
		if chunk.text != "" {
			o.err = errors.Join(o.err, sink.Emit(driver.RunEvent{Type: driver.RunEventChunk, Stream: chunk.stream, Bytes: []byte(chunk.text)}))
		}
	}
	return o.err
}
func (o *processOutput) end() (driver.RawStreams, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw := driver.RawStreams{Stdout: o.stdout.String(), Stderr: o.stderr.String()}
	o.stdout.Reset()
	o.stderr.Reset()
	o.sink = nil
	return raw, o.err
}
func (o *processOutput) processID() any { o.mu.Lock(); defer o.mu.Unlock(); return o.pid }

func (p *serverProcess) stop(ctx context.Context) error {
	p.cancel()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-p.done:
		p.transport.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
func (p *serverProcess) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (l *serverLease) start(ctx context.Context, c Config, cwd string, env []driver.EnvBinding, sink driver.EventSink) (*serverProcess, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret)
	env = skillruntime.WithBinding(env, "OPENCODE_SERVER_PASSWORD", password)
	env = skillruntime.WithBinding(env, "OPENCODE_SERVER_USERNAME", "opencode")
	procCtx, cancel := context.WithCancel(context.Background())
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 45 * time.Second}
	proc := &serverProcess{cancel: cancel, done: make(chan struct{}), transport: transport}
	proc.client = &serverClient{password: password, cwd: cwd, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	_ = proc.output.begin(sink)
	l.pool.mu.Lock()
	if l.pool.isClosed() {
		l.pool.mu.Unlock()
		cancel()
		return nil, errServerPoolClosed
	}
	l.slot.process = proc
	l.pool.mu.Unlock()
	ready := make(chan string, 1)
	var lines driverutil.Lines
	command := c.Command
	if command == "" {
		command = "opencode"
	}
	go func() {
		proc.result, proc.err = clihelper.Run(procCtx, clihelper.CommandRequest{Command: command, Args: []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}, CWD: cwd, Env: env, Observe: driverutil.ObserveStdout(&lines, func(line []byte) {
			if address, ok := strings.CutPrefix(strings.TrimSpace(string(line)), "opencode server listening on "); ok {
				select {
				case ready <- address:
				default:
				}
			}
		})}, &proc.output)
		close(proc.done)
	}()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	var address string
	select {
	case address = <-ready:
	case <-proc.done:
		return proc, errors.Join(errors.New("opencode: server exited before readiness"), proc.err)
	case <-timer.C:
		return proc, errors.New("opencode: server readiness timed out")
	case <-ctx.Done():
		return proc, context.Cause(ctx)
	case <-l.pool.closed:
		return proc, errServerPoolClosed
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return proc, errors.New("opencode: invalid private server address")
	}
	proc.client.base = address
	return proc, nil
}
