package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/codex/appserver"
	"github.com/agent-dance/agent-adaptor/driver"
)

// errPersistentFallback is returned only while no user prompt has been sent
// to app-server. Once turn/start may have reached the peer, replaying through
// codex exec or a new app-server could duplicate tool side effects.
var errPersistentFallback = errors.New("codex: persistent process unavailable before prompt delivery")

var errPersistentPoolClosed = errors.New("codex: persistent process pool closed")

var codexPersistentIdleTimeout = 5 * time.Minute

const codexPersistentDefaultGracePeriod = 2 * time.Second

type persistentSpec struct {
	command   string
	cwd       string
	env       []driver.EnvBinding
	model     string
	effort    string
	fastMode  bool
	extraArgs []string

	resumeID     string
	engineID     string
	previousID   string
	prompt       string
	runID        string
	approval     string
	sandbox      string
	outputSchema *driver.OutputSchema

	profileFingerprint  string
	settingsFingerprint string
	commandFingerprint  string
	gracePeriod         time.Duration
}

func (s persistentSpec) openOptions() appserver.Options {
	return appserver.Options{
		Command:        s.command,
		ExtraArgs:      append([]string(nil), s.extraArgs...),
		CWD:            s.cwd,
		Env:            append([]driver.EnvBinding(nil), s.env...),
		ClientName:     "agent-adaptor",
		ClientVersion:  "v0",
		ResumeThreadID: s.resumeID,
		Ephemeral:      false,
		Sandbox:        s.sandbox,
		Model:          s.model,
		ServiceTier:    persistentServiceTier(s.fastMode),
	}
}

func (s persistentSpec) turnOptions() appserver.Options {
	opts := s.openOptions()
	opts.Prompt = s.prompt
	opts.RunID = s.runID
	opts.Approval = s.approval
	opts.Effort = s.effort
	opts.OutputSchema = s.outputSchema
	return opts
}

func persistentServiceTier(fast bool) string {
	if fast {
		return "fast"
	}
	return ""
}

// sig covers all effective inputs which can affect a long-lived app-server.
// resumeID and prompt are intentionally excluded: they identify/use the same
// conversation rather than configure the process. Environment values are
// hashed and never exposed in events or metadata.
func (s persistentSpec) sig() string {
	return persistentHashStrings(
		"codex_persistent_v1",
		s.command,
		s.cwd,
		persistentHashStrings(codexPersistentEnv(s.env)...),
		s.model,
		s.effort,
		strconv.FormatBool(s.fastMode),
		strings.Join(s.extraArgs, "\x00"),
		s.sandbox,
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

func (w *persistentWriter) run(ctx context.Context, spec persistentSpec, sink driver.EventSink) (driver.Response, error) {
	if w == nil {
		return driver.Response{}, errPersistentFallback
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
		lp, err = w.pool.open(ctx, spec, sink)
		if err != nil {
			return driver.Response{}, errPersistentFallback
		}
	}

	lp.stopIdle()
	result, sent, err := lp.process.RunTurn(ctx, spec.turnOptions(), sink)
	if err != nil {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		if !sent && ctx.Err() == nil {
			return driver.Response{}, errPersistentFallback
		}
		return result, err
	}
	if result.Failure != nil || result.Checkpoint == nil || !result.Checkpoint.Valid || result.Checkpoint.State == nil || result.Checkpoint.State.ResumeID == "" {
		w.pool.detach(lp)
		_ = lp.terminateAndWait(context.Background())
		return result, nil
	}
	for _, stale := range w.pool.register(lp, result.Checkpoint.State.ResumeID, spec.engineID) {
		_ = stale.terminateAndWait(context.Background())
	}
	lp.startIdle()
	return result, nil
}

// suspendAndWait enforces the per-session single-writer handoff. It does not
// return until the old app-server process group has exited and cmd.Wait has
// reaped it.
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

// preWarm performs initialize + initialized + thread/resume but sends no
// turn/start, leaving the current checkpoint ready for the next streaming run.
func (w *persistentWriter) preWarm(spec persistentSpec, sink driver.EventSink) error {
	if w == nil || strings.TrimSpace(spec.resumeID) == "" {
		return nil
	}
	if current := w.pool.lookup(spec.resumeID); current != nil {
		w.pool.detach(current)
		if err := current.terminateAndWait(context.Background()); err != nil {
			return err
		}
	}
	openCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lp, err := w.pool.open(openCtx, spec, sink)
	if err != nil {
		return err
	}
	for _, stale := range w.pool.register(lp, spec.resumeID, spec.engineID) {
		_ = stale.terminateAndWait(context.Background())
	}
	lp.startIdle()
	return nil
}

func (p *persistentPool) open(ctx context.Context, spec persistentSpec, sink driver.EventSink) (*liveProcess, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPersistentPoolClosed
	}
	p.mu.Unlock()
	process, err := appserver.Open(ctx, spec.openOptions(), sink)
	if err != nil {
		return nil, err
	}
	lp := &liveProcess{pool: p, process: process, sig: spec.sig(), key: spec.resumeID, grace: spec.gracePeriod}
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
	if strings.TrimSpace(resumeID) == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[resumeID]
}

func (p *persistentPool) lookupEngine(engineID string) *liveProcess {
	if strings.TrimSpace(engineID) == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.engines[engineID]
}

func (p *persistentPool) register(lp *liveProcess, resumeID, engineID string) []*liveProcess {
	if lp == nil || strings.TrimSpace(resumeID) == "" {
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

func (p *persistentPool) forget(lp *liveProcess) {
	p.mu.Lock()
	delete(p.all, lp)
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

type liveProcess struct {
	pool      *persistentPool
	process   *appserver.Process
	sig       string
	key       string
	engineKey string
	grace     time.Duration

	mu   sync.Mutex
	idle *time.Timer
}

func (lp *liveProcess) startIdle() {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	if lp.process.IsClosed() {
		return
	}
	if lp.idle != nil {
		lp.idle.Stop()
	}
	lp.idle = time.AfterFunc(codexPersistentIdleTimeout, func() { lp.pool.evictIdle(lp) })
}

func (lp *liveProcess) stopIdle() {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	if lp.idle != nil {
		lp.idle.Stop()
		lp.idle = nil
	}
}

func (lp *liveProcess) isClosed() bool {
	return lp == nil || lp.process == nil || lp.process.IsClosed()
}

func (lp *liveProcess) terminateAndWait(ctx context.Context) error {
	if lp == nil {
		return nil
	}
	lp.stopIdle()
	err := lp.process.TerminateAndWait(ctx)
	lp.pool.forget(lp)
	return err
}

func (lp *liveProcess) gracefulStop(ctx context.Context) error {
	if lp == nil {
		return nil
	}
	lp.stopIdle()
	grace := lp.grace
	if grace <= 0 {
		grace = codexPersistentDefaultGracePeriod
	}
	err := lp.process.CloseGracefully(ctx, grace)
	lp.pool.forget(lp)
	return err
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
	if resumeID := codexResumeID(req); resumeID != "" {
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

func codexResumeID(req driver.Request) string {
	if req.Session == nil || req.Session.State == nil {
		return ""
	}
	return strings.TrimSpace(req.Session.State.ResumeID)
}

func persistentEligible(cfg Config, req driver.Request) bool {
	if !req.Streaming || persistentSessionKey(req) == "" ||
		(req.Session != nil && req.Session.Mode == driver.SessionFork) {
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
	copyReq.Streaming = true
	copyReq.OutputSchema = nil
	copyReq.StructuredOutputSource = ""
	return persistentEligible(cfg, copyReq)
}

func emitPersistentInvocation(sink driver.EventSink, spec persistentSpec) {
	if sink == nil {
		return
	}
	_ = sink.Emit(driver.RunEvent{
		Type:      driver.RunEventInvocation,
		Text:      "starting persistent Codex turn",
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"command":    spec.command,
			"args":       []string{"app-server", "--listen", "stdio://"},
			"cwd":        spec.cwd,
			"env_keys":   persistentEnvKeys(spec.env),
			"persistent": true,
		},
	})
}

func codexPersistentEnv(bindings []driver.EnvBinding) []string {
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
	seen := map[string]struct{}{}
	keys := make([]string, 0, len(bindings))
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

// codexSettingsFingerprint covers provider/profile/project files read while
// app-server is alive. Provider-owned mutable session/log/auth state is
// intentionally excluded; SDK-managed resources and ambient configuration are
// included so out-of-band edits trigger a clean writer handoff.
func codexSettingsFingerprint(codexHome, cwd string) string {
	paths := []string{
		filepath.Join(codexHome, "config.toml"),
		filepath.Join(codexHome, "config.json"),
		filepath.Join(codexHome, "instructions.md"),
		filepath.Join(codexHome, "AGENTS.md"),
		filepath.Join(codexHome, "AGENTS.override.md"),
	}
	for _, name := range []string{"skills", "rules", "prompts", "agents", "hooks"} {
		paths = append(paths, filepath.Join(codexHome, name))
	}
	if cwd != "" {
		current := filepath.Clean(cwd)
		for {
			paths = append(paths,
				filepath.Join(current, "AGENTS.md"),
				filepath.Join(current, "AGENTS.override.md"),
				filepath.Join(current, ".codex", "config.toml"),
				filepath.Join(current, ".codex", "config.json"),
				filepath.Join(current, ".codex", "instructions.md"),
				filepath.Join(current, ".codex", "skills"),
				filepath.Join(current, ".codex", "rules"),
			)
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		path = filepath.Clean(path)
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	h := sha256.New()
	_, _ = io.WriteString(h, "codex-home:"+filepath.Clean(codexHome)+"\x00")
	seen := map[string]bool{}
	for _, path := range unique {
		writePathFingerprint(h, path, seen)
	}
	return hex.EncodeToString(h.Sum(nil))
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
	ignoreSystemSkills := filepath.Base(logicalPath) == "skills"
	_ = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		rel, relErr := filepath.Rel(resolved, path)
		if relErr != nil {
			rel = path
		}
		// Codex installs and updates built-in skills below skills/.system while
		// app-server is alive. They are provider-owned mutable state, not host
		// configuration drift, and must not force a single-writer handoff on the
		// following turn. User and SDK skills remain fingerprinted.
		if ignoreSystemSkills && rel == ".system" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		_, _ = io.WriteString(h, rel+"\x00")
		if walkErr != nil {
			_, _ = io.WriteString(h, "walk-error:"+walkErr.Error()+"\x00")
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			_, _ = io.WriteString(h, "read-error:"+readErr.Error()+"\x00")
			return nil
		}
		_, _ = h.Write(raw)
		_, _ = io.WriteString(h, "\x00")
		return nil
	})
}
