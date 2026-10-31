package pi

import (
	"bytes"
	"context"
	"errors"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var errRPCPoolClosed = errors.New("pi: rpc pool is closed")

const rpcIdleTimeout = 5 * time.Minute

// Aliases identify the one writer for an engine record or provider session.
// A slot stays busy throughout stop/wait/replacement, including WithSpawn.
// Different Thread slots never serialize each other's execution.
type rpcPool struct {
	mu          sync.Mutex
	aliases     map[string]*rpcSlot
	all         map[*rpcSlot]struct{}
	closed      chan struct{}
	idleTimeout time.Duration
}
type rpcSlot struct {
	process   *rpcProcess
	signature string
	busy      bool
	changed   chan struct{}
	aliases   []string
	timer     *time.Timer
}
type rpcLease struct {
	pool       *rpcPool
	slot       *rpcSlot
	persistent bool
}

func newRPCPool() *rpcPool {
	return &rpcPool{aliases: map[string]*rpcSlot{}, all: map[*rpcSlot]struct{}{}, closed: make(chan struct{}), idleTimeout: rpcIdleTimeout}
}
func (p *rpcPool) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func rpcKeys(req driver.Request) []string {
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
func (p *rpcPool) acquire(ctx context.Context, req driver.Request) (*rpcLease, error) {
	keys := rpcKeys(req)
	for {
		p.mu.Lock()
		if p.isClosed() {
			p.mu.Unlock()
			return nil, errRPCPoolClosed
		}
		var slot *rpcSlot
		for _, key := range keys {
			if candidate := p.aliases[key]; candidate != nil {
				if slot != nil && slot != candidate {
					p.mu.Unlock()
					return nil, errors.New("pi: conflicting live session writers")
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
				return nil, errRPCPoolClosed
			}
		}
		if slot == nil {
			slot = &rpcSlot{}
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
		return &rpcLease{pool: p, slot: slot, persistent: req.Session != nil && req.Session.EngineSessionID != "" && req.Session.Mode != driver.SessionStateless && !req.Spawn}, nil
	}
}
func (l *rpcLease) register(resumeID string) error {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	if l.pool.isClosed() {
		return errRPCPoolClosed
	}
	key := "session:" + resumeID
	if old := l.pool.aliases[key]; old != nil && old != l.slot {
		return errors.New("pi: provider returned a session already owned by another writer")
	}
	if l.pool.aliases[key] == nil {
		l.pool.aliases[key] = l.slot
		l.slot.aliases = append(l.slot.aliases, key)
	}
	return nil
}
func (l *rpcLease) release() {
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
func (p *rpcPool) evict(s *rpcSlot) {
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
	l := &rpcLease{pool: p, slot: s}
	_ = l.stop(context.Background())
	l.release()
}
func (l *rpcLease) stop(ctx context.Context) error {
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
func (p *rpcPool) close(ctx context.Context) error {
	p.mu.Lock()
	if !p.isClosed() {
		close(p.closed)
	}
	var processes []*rpcProcess
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
func rpcSignature(c Config, args []string, cwd string, env []driver.EnvBinding, guards map[string]string, extension string) (string, error) {
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
		command = "pi"
	}
	var commandFile string
	if resolved, err := exec.LookPath(command); err == nil {
		commandFile, err = driverutil.NativeFilesFingerprint("pi/command/v1", resolved)
		if err != nil {
			return "", err
		}
	}
	return driver.CanonicalSessionConfigFingerprint("pi/rpc-process/v1", struct {
		Config      Config
		Args        []string
		Extension   string
		CWD         string
		Env, Guards map[string]string
		CommandFile string
	}{c, args, extension, cwd, effective, guards, commandFile})
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
	return errors.New("pi: process transport cannot emit semantic events")
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

type rpcFrame struct {
	raw []byte
	err error
}
type rpcProcess struct {
	cancel     context.CancelFunc
	done       chan struct{}
	frames     chan rpcFrame
	stdin      clihelper.StdinController
	output     processOutput
	result     clihelper.CommandResult
	err        error
	extension  *rpcExtension
	nextID     uint64 // Used only by the exclusive slot lease holder.
	uiRequests map[string]bool
}

func (p *rpcProcess) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *rpcProcess) stop(ctx context.Context) error {
	p.cancel()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
func (l *rpcLease) start(c Config, args []string, cwd string, env []driver.EnvBinding, b *mcpBridge, sink driver.EventSink) (*rpcProcess, error) {
	host, err := newRPCExtension(b)
	if err != nil {
		return nil, err
	}
	args = append([]string{"--mode", "rpc"}, args...)
	if host != nil {
		args = append(args, "--extension", host.path)
		env = append(append([]driver.EnvBinding(nil), env...), host.env...)
	}
	procCtx, cancel := context.WithCancel(context.Background())
	proc := &rpcProcess{cancel: cancel, done: make(chan struct{}), frames: make(chan rpcFrame, 128), stdin: clihelper.NewStdinController(), extension: host}
	_ = proc.output.begin(sink)
	l.pool.mu.Lock()
	if l.pool.isClosed() {
		l.pool.mu.Unlock()
		cancel()
		host.close()
		return nil, errRPCPoolClosed
	}
	l.slot.process = proc
	l.pool.mu.Unlock()
	command := c.Command
	if command == "" {
		command = "pi"
	}
	go func() {
		defer close(proc.done)
		defer proc.stdin.Close()
		defer host.close()
		var pending []byte
		framingFailed := false
		send := func(frame rpcFrame) {
			select {
			case proc.frames <- frame:
			case <-procCtx.Done():
			}
		}
		proc.result, proc.err = clihelper.Run(procCtx, clihelper.CommandRequest{Command: command, Args: args, CWD: cwd, Env: env, Stdin: proc.stdin, Observe: func(stream string, chunk []byte, _ time.Time) error {
			if stream != "stdout" {
				return nil
			}
			if framingFailed {
				return nil
			}
			fail := func() {
				framingFailed = true
				pending = nil
				send(rpcFrame{err: errors.New("pi: RPC record exceeds 32 MiB")})
				// Use the configured process-tree cancellation, never a parent-
				// only kill which could leave a CLI shim's child holding pipes.
				proc.cancel()
			}
			pending = append(pending, chunk...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				if i > 32<<20 {
					fail()
					return nil
				}
				line := append([]byte(nil), pending[:i]...)
				pending = pending[i+1:]
				send(rpcFrame{raw: line})
			}
			if len(pending) > 32<<20 {
				fail()
			}
			return nil
		}}, &proc.output)
		if len(pending) > 0 {
			send(rpcFrame{raw: pending, err: errors.New("pi: unterminated RPC record")})
		}
	}()
	return proc, nil
}
