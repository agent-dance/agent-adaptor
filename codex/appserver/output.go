package appserver

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/processx"
)

// outputDrainTimeout bounds pipe draining after process exit. Normally both
// readers reach EOF; a descendant may retain an inherited writer on any OS.
// Closing our read ends at the deadline preserves only bytes already observed.
const outputDrainTimeout = 5 * time.Second

// processOutput owns the output pipes independently of exec.Cmd. Wait closes
// StdoutPipe/StderrPipe even if their readers haven't run yet; assigning file
// writers instead lets us reap the process without discarding unread bytes.
type processOutput struct {
	stdout, stderr             *processx.PipeFile
	stdoutWriter, stderrWriter *processx.PipeFile
}

func newProcessOutput(cmd *exec.Cmd, hooks *processHooks) (*processOutput, error) {
	p := &processOutput{}
	var err error
	p.stdout, p.stdoutWriter, err = processx.NewPipe(hooks.closed)
	if err != nil {
		return nil, err
	}
	p.stderr, p.stderrWriter, err = processx.NewPipe(hooks.closed)
	if err != nil {
		p.close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = p.stdoutWriter.File, p.stderrWriter.File
	return p, nil
}

func (p *processOutput) closeWriters() {
	if p.stdoutWriter != nil {
		_ = p.stdoutWriter.Close()
	}
	if p.stderrWriter != nil {
		_ = p.stderrWriter.Close()
	}
}

func (p *processOutput) close() {
	p.closeWriters()
	p.closeReaders()
}

func (p *processOutput) closeReaders() {
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
}

// wait publishes process exit independently, then joins both readers. The drain
// deadline starts at exit even without host cancellation: inherited writers must
// not prevent a turn from observing exit or releasing its Thread lease.
// JSON-RPC disconnect alone is not a reader join: Client.Close can publish it
// while ReadObject is still running.
func (p *processOutput) wait(cmd *exec.Cmd, stream *stdioStream, stderrDone <-chan struct{}, exited func(error), hooks *processHooks) error {
	defer p.close()
	waitErr := cmd.Wait()
	deadline, stop := hooks.drainTimer()
	defer stop()
	if exited != nil {
		exited(waitErr)
	}
	hooks.waited()
	stdoutDone := stream.readDone
	for stdoutDone != nil || stderrDone != nil {
		select {
		case <-stdoutDone:
			stdoutDone = nil
		case <-stderrDone:
			stderrDone = nil
		case <-deadline:
			p.closeReaders()
			deadline = nil
		}
	}
	return waitErr
}

// processHooks allows package tests to hold a pipe reader until after Wait,
// reproducing a descheduled collector without relying on sleeps or CPU load.
type processHooks struct {
	afterClose    func(*os.File, error)
	stdoutReader  func(io.Reader) io.Reader
	stderrReader  func(io.Reader) io.Reader
	afterWait     func()
	afterStart    func(*processOutput)
	newDrainTimer func(time.Duration) (<-chan time.Time, func())
}

func (h *processHooks) stderr(r io.Reader) io.Reader {
	if h != nil && h.stderrReader != nil {
		return h.stderrReader(r)
	}
	return r
}

func (h *processHooks) waited() {
	if h != nil && h.afterWait != nil {
		h.afterWait()
	}
}

// started runs before closing the parent's writer copies. Tests may retain one
// to model an inherited handle without OS-specific descriptor duplication.
func (h *processHooks) started(p *processOutput) {
	if h != nil && h.afterStart != nil {
		h.afterStart(p)
	}
}

func (h *processHooks) drainTimer() (<-chan time.Time, func()) {
	if h != nil && h.newDrainTimer != nil {
		return h.newDrainTimer(outputDrainTimeout)
	}
	timer := time.NewTimer(outputDrainTimeout)
	return timer.C, func() { timer.Stop() }
}

func (h *processHooks) stdout(r io.Reader) io.Reader {
	if h != nil && h.stdoutReader != nil {
		return h.stdoutReader(r)
	}
	return r
}

// A forced drain cutoff is EOF at the pipe layer. Let json.Decoder distinguish
// a complete last frame from a truncated one (io.ErrUnexpectedEOF), rather than
// treating our descriptor closure as provider JSON corruption.
type outputPipeReader struct{ io.Reader }

func (r outputPipeReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if errors.Is(err, os.ErrClosed) {
		err = io.EOF
	}
	return n, err
}

func (h *processHooks) closed(f *os.File, err error) {
	if h != nil && h.afterClose != nil {
		h.afterClose(f, err)
	}
}
