package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// processStdin serializes complete frames from the hook writer and the HITL
// responder. Closing never waits for the write lock: cancellation must unblock
// a writer stuck in a full pipe.
type processStdin struct {
	lp *liveProcess
}

func (w processStdin) Write(frame []byte) error {
	w.lp.writeMu.Lock()
	defer w.lp.writeMu.Unlock()
	n, err := w.lp.stdin.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	return err
}

func (w processStdin) Close() error {
	w.lp.beginClose()
	return nil
}

// HITL responses use the same FIFO writer as hooks. Returning after enqueue
// matches StdinController's existing HITL contract and keeps the parser free
// to drain stdout while a hook response is blocked in the pipe.
type messageControlStdin struct{ session *messageSession }

func (w messageControlStdin) Write(frame []byte) error {
	return w.session.enqueue(messageWrite{frame: frame})
}

func (w messageControlStdin) Close() error {
	return w.session.enqueue(messageWrite{close: true})
}

// messageTurn reads concurrently with initialization, prompt delivery and hook
// writes. Resident calls stop at result; per-run calls drain to EOF and Wait so
// a late nonzero exit cannot create a healthy checkpoint.
func (lp *liveProcess) messageTurn(ctx context.Context, prompt string, sink driver.EventSink, parser *claudeParser, bind interactiveBinder, once bool) (driver.RawStreams, bool, error) {
	if err := ctx.Err(); err != nil {
		return driver.RawStreams{}, false, err
	}
	s := parser.messages
	if lp.hooksInitialized {
		s.resolveInitialization(lp.hooksErr)
	}
	stdin := processStdin{lp: lp}
	s.start(stdin, lp.signalTerminate)
	if bind != nil {
		bind(messageControlStdin{session: s})
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
	turnDone := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			lp.signalTerminate()
		case <-turnDone:
		}
	}()
	defer func() { close(turnDone); <-watchDone }()
	type readResult struct {
		raw    string
		result bool
		err    error
	}
	readDone := make(chan struct{})
	var rr readResult
	go func() {
		defer close(readDone)
		var raw strings.Builder
		defer func() { rr.raw = raw.String() }()
		for {
			line, err := lp.stdout.ReadString('\n')
			if len(line) > 0 {
				raw.WriteString(line)
				ts := time.Now().UTC()
				emitPersistentChunk(sink, "stdout", []byte(line), ts)
				if parseErr := parser.onChunk("stdout", []byte(line), ts); parseErr != nil {
					rr.err = parseErr
					return
				}
				if isMessageTerminalLine(line) {
					rr.result = true
					if !once {
						return
					}
					s.enqueue(messageWrite{close: true})
				}
			}
			if err != nil {
				rr.err = err
				return
			}
		}
	}()
	var runErr error
	if !lp.hooksInitialized {
		lp.hooksErr, runErr = s.initialize(readDone)
		if runErr == nil {
			lp.hooksInitialized = true
		}
	}
	if runErr == nil && lp.hooksErr != nil && sink != nil {
		runErr = sink.Emit(driver.RunEvent{
			Type:      driver.RunEventLifecycle,
			Timestamp: time.Now().UTC(),
			Text:      "Claude message hooks unavailable; pending messages continue at turn end via resume: " + lp.hooksErr.Error(),
			Metadata:  map[string]string{"code": "claude_hooks_unavailable"},
		})
	}
	sent := false
	if runErr == nil {
		var frame string
		frame, runErr = encodeInteractiveUserFrame(prompt)
		if runErr == nil {
			// Conservatively suppress fallback once a prompt write is attempted.
			sent = true
			runErr = s.write([]byte(frame))
		}
	}
	if runErr != nil {
		lp.signalTerminate()
	}
	<-readDone
	if !once && !rr.result && runErr == nil {
		runErr = rr.err
		if runErr == nil {
			runErr = io.ErrUnexpectedEOF
		}
	}
	if once && rr.err != nil && !errors.Is(rr.err, io.EOF) && runErr == nil {
		runErr = rr.err
	}
	if runErr == nil {
		runErr = s.failure()
	}
	if once || runErr != nil {
		if runErr != nil {
			lp.signalTerminate()
		}
		lp.startWait()
		<-lp.waitCh
	}
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	s.finish(runErr)
	parser.finalize()
	if parser.interactiveErr != nil {
		runErr = parser.interactiveErr
	}
	return driver.RawStreams{Stdout: rr.raw, Stderr: lp.stderr.since(stderrStart), Terminal: parser.terminal}, sent, runErr
}

func runMessageProcess(ctx context.Context, spec persistentSpec, args []string, req driver.Request, sink driver.EventSink, parser *claudeParser, model, cwd, fingerprint string) (driver.Response, error) {
	if err := ctx.Err(); err != nil {
		return driver.Response{}, err
	}
	spec.runArgs = append([]string(nil), args...)
	pool := newPersistentPool()
	emitPersistentInvocation(sink, spec)
	lp, err := pool.spawn(spec, sink)
	if err != nil {
		return driver.Response{}, err
	}
	defer func() { _ = lp.terminateAndWait(context.Background()) }()
	var bind interactiveBinder
	if spec.interactive {
		decisionSink, ok := sink.(driver.DecisionCapableSink)
		if !ok {
			_ = lp.terminateAndWait(context.Background())
			return driver.Response{}, errClaudeInteractiveSinkRequired
		}
		bind = func(stdin interactiveStdin) { parser.enableInteractive(ctx, decisionSink, stdin) }
	}
	raw, _, err := lp.messageTurn(ctx, spec.prompt, sink, parser, bind, true)
	if err != nil {
		if ctx.Err() == nil {
			parser.completeStream(&driver.RunFailure{Code: driver.FailureAgentError, Message: err.Error()}, 0, "", false)
			return driver.Response{}, err
		}
		// Cancellation may have won immediately after Start, before the
		// turn installed its reader and cancellation watcher.
		_ = lp.terminateAndWait(context.Background())
	}
	exitCode := lp.cmd.ProcessState.ExitCode()
	if lp.waitErr != nil {
		// ExitError is represented by the response's exit status, just like
		// the shared process helper. Other Wait errors are infrastructure errors.
		var exitErr *exec.ExitError
		if !errors.As(lp.waitErr, &exitErr) && ctx.Err() == nil {
			return driver.Response{}, lp.waitErr
		}
	}
	signal := ""
	if !lp.cmd.ProcessState.Exited() {
		signal = lp.cmd.ProcessState.String()
	}
	if ctx.Err() != nil {
		if exitCode == 0 {
			exitCode = -1
		}
		if signal == "" {
			signal = ctx.Err().Error()
		}
	}
	return buildClaudeResponse(req, parser, raw, exitCode, signal, errors.Is(ctx.Err(), context.DeadlineExceeded), model, cwd, fingerprint)
}

func isMessageTerminalLine(line string) bool {
	var frame struct {
		Type string `json:"type"`
	}
	return json.Unmarshal([]byte(line), &frame) == nil && (frame.Type == "result" || frame.Type == "error")
}
