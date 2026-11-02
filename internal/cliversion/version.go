// Package cliversion provides bounded, cached local CLI version probes.
package cliversion

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/v2/internal/processx"
)

const probeTimeout = time.Second

// Probe caches successful and unknown results per resolved executable identity.
// Concurrent callers share one subprocess. A CLI replacement invalidates the
// entry, including replacements with the same size and modification time.
type Probe struct {
	// Pattern recognizes provider output; its first capture is the version.
	Pattern *regexp.Regexp
	// Date treats suffixes as build hashes rather than semver prereleases.
	Date bool

	mu      sync.Mutex
	entries map[string]*entry
	timeout time.Duration
}

type entry struct {
	info   os.FileInfo
	done   chan struct{}
	result Version
}

// Version is a parsed CLI version or an unknown result with a diagnostic.
type Version struct {
	// Text is the parsed version without the provider label, or empty if unknown.
	Text string
	// Detail explains an unknown result without including arbitrary CLI output.
	Detail string
	parts  [3]uint64
	pre    bool
}

// Parse extracts a version from an explicitly recognized provider output line.
// Date-style build hashes do not lower the version below that day's release.
func (p *Probe) Parse(output string) Version {
	match := p.Pattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return Version{Detail: "CLI version output was not recognized"}
	}
	v := parse(match[1], p.Date)
	if v.Text == "" {
		v.Detail = "CLI version output contained an invalid version"
	}
	return v
}

func parse(text string, date bool) Version {
	base, _, _ := strings.Cut(text, "+")
	base, suffix, _ := strings.Cut(base, "-")
	fields := strings.Split(base, ".")
	if len(fields) != 3 {
		return Version{}
	}
	v := Version{Text: text, pre: suffix != "" && !date}
	for i, field := range fields {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return Version{}
		}
		v.parts[i] = n
	}
	if date {
		if _, err := time.Parse("2006.1.2", base); err != nil {
			return Version{}
		}
	}
	return v
}

// Below reports whether a known version is older than the minimum. Unknown
// versions never disable capabilities. Minimums are stable release versions.
func (v Version) Below(minimum string) bool {
	if v.Text == "" || minimum == "" {
		return false
	}
	min := parse(minimum, false)
	if min.Text == "" {
		return false
	}
	for i := range v.parts {
		if v.parts[i] != min.parts[i] {
			return v.parts[i] < min.parts[i]
		}
	}
	return v.pre && !min.pre
}

// Detect runs only the version flag, never execution arguments or a prompt.
// Inspection can prime the cache before execution. A cold runtime resolver may
// wait at most the short probe timeout; later calls only resolve and stat the
// executable. Caller cancellation stops waiting without poisoning the shared
// result for other callers.
func (p *Probe) Detect(ctx context.Context, cfg driver.CommonConfig, defaultCommand string) Version {
	if err := ctx.Err(); err != nil {
		return Version{Detail: err.Error()}
	}
	command := cfg.Command
	if command == "" {
		command = defaultCommand
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return Version{Detail: "CLI executable could not be resolved"}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Version{Detail: "CLI executable path could not be resolved"}
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return Version{Detail: "CLI executable could not be inspected"}
	}
	p.mu.Lock()
	e := p.entries[path]
	if e == nil || !os.SameFile(e.info, info) || e.info.Size() != info.Size() || !e.info.ModTime().Equal(info.ModTime()) {
		e = &entry{info: info, done: make(chan struct{})}
		if p.entries == nil {
			p.entries = make(map[string]*entry)
		}
		p.entries[path] = e
		go func() {
			e.result = p.run(path, cfg)
			close(e.done)
		}()
	}
	p.mu.Unlock()
	select {
	case <-e.done:
		return e.result
	case <-ctx.Done():
		return Version{Detail: ctx.Err().Error()}
	}
}

func (p *Probe) run(path string, cfg driver.CommonConfig) Version {
	timeout := p.timeout
	if timeout <= 0 {
		timeout = probeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command, args, err := processx.PrepareCommand(path, []string{"--version"})
	if err != nil {
		return Version{Detail: "CLI version command could not be prepared"}
	}
	cmd := exec.CommandContext(ctx, command, args...)
	processx.ConfigureCancellation(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Dir = cfg.CWD
	cmd.Env = os.Environ()
	for _, binding := range cfg.Env {
		cmd.Env = append(cmd.Env, binding.Name+"="+binding.Value)
	}
	var output boundedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Version{Detail: "CLI version command timed out"}
		}
		return Version{Detail: "CLI version command failed"}
	}
	return p.Parse(string(output))
}

type boundedOutput []byte

func (b *boundedOutput) Write(data []byte) (int, error) {
	const limit = 16 * 1024
	n := len(data)
	if remaining := limit - len(*b); remaining > 0 {
		*b = append(*b, data[:min(n, remaining)]...)
	}
	return n, nil
}

// Capability removes in-run delivery from a known unsupported CLI, retaining
// admission limits and persistence semantics for core's resume continuation.
func (v Version) Capability(caps driver.MessageCapability) driver.MessageCapability {
	caps.Boundaries = append([]driver.Boundary(nil), caps.Boundaries...)
	if v.Below(caps.MinVersion) {
		caps.Boundaries = nil
		caps.Interrupt = false
		caps.Continue = driver.ContinueResume
		caps.ConfirmedDelivery = false
	}
	return caps
}

// Environment adds stable version diagnostics and the effective local
// capability without making an otherwise usable base driver unhealthy.
func (v Version) Environment(name string, caps driver.MessageCapability, checks []driver.EnvironmentCheck) driver.EnvironmentReport {
	check := driver.EnvironmentCheck{Code: name + "_version", Level: "info", Message: "CLI version detected", Detail: v.Text}
	if v.Text == "" {
		check.Level, check.Message, check.Detail = "warn", "CLI version is unknown", v.Detail
		check.Hint = "Check that the configured CLI supports --version; message capabilities remain declared and runtime fallbacks still apply."
	}
	checks = append(checks, check)
	if v.Below(caps.MinVersion) {
		checks = append(checks, driver.EnvironmentCheck{
			Code: name + "_version_unsupported", Level: "warn", Message: v.unsupportedText(name, caps.MinVersion),
			Hint: "Upgrade the CLI to " + caps.MinVersion + " or newer to enable in-run message delivery.",
		})
	}
	report := driverutil.SummarizeEnvironment(name, checks)
	report.Version = v.Text
	report.Messages = v.Capability(caps)
	return report
}

func (v Version) unsupportedText(name, minimum string) string {
	return fmt.Sprintf("%s CLI %s is older than %s; in-run messages are unavailable. Pending messages continue at turn end via core resume.", name, v.Text, minimum)
}

var notices = struct {
	sync.Mutex
	seen map[noticeKey]bool
}{seen: make(map[noticeKey]bool)}

type noticeKey struct {
	done <-chan struct{}
	run  string
	code string
}

// PrepareRun disconnects the provider's in-run port on an unsupported CLI;
// core retains its original queue for resume. It emits one lifecycle Notice
// per execution, including executions spanning several Driver.Run calls. Core
// cancels the execution context on completion, releasing the deduplication
// entry. Unscoped direct SPI calls have no shared execution lifetime to dedupe.
func (v Version) PrepareRun(ctx context.Context, req driver.Request, sink driver.EventSink, name string, caps driver.MessageCapability) (driver.Request, error) {
	if req.Messages == nil || !v.Below(caps.MinVersion) {
		return req, nil
	}
	req.Messages = nil
	if sink == nil {
		return req, nil
	}
	key := noticeKey{done: ctx.Done(), run: req.RunID, code: name + "_version_unsupported"}
	if key.done != nil && key.run != "" {
		notices.Lock()
		if notices.seen[key] {
			notices.Unlock()
			return req, nil
		}
		notices.seen[key] = true
		notices.Unlock()
		context.AfterFunc(ctx, func() {
			notices.Lock()
			delete(notices.seen, key)
			notices.Unlock()
		})
	}
	return req, sink.Emit(driver.RunEvent{Type: driver.RunEventLifecycle, Text: v.unsupportedText(name, caps.MinVersion), Metadata: map[string]string{"code": key.code}})
}
