package clihelper

import (
	"os"
	"os/exec"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/internal/processx"
)

const outputDrainTimeout = 5 * time.Second

// commandOutput owns its pipes so Wait can observe exit independently of EOF
// without closing unread bytes. Inherited writers can outlive exit on any OS.
type commandOutput struct {
	stdout, stderr             *processx.PipeFile
	stdoutWriter, stderrWriter *processx.PipeFile
}

func newCommandOutput(cmd *exec.Cmd, hooks *outputHooks) (*commandOutput, error) {
	p := &commandOutput{}
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

func (p *commandOutput) closeWriters() {
	if p.stdoutWriter != nil {
		_ = p.stdoutWriter.Close()
	}
	if p.stderrWriter != nil {
		_ = p.stderrWriter.Close()
	}
}

func (p *commandOutput) close() {
	p.closeWriters()
	p.closeReaders()
}

func (p *commandOutput) closeReaders() {
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
}

func (p *commandOutput) wait(cmd *exec.Cmd, readersDone <-chan struct{}, exited func(), hooks *outputHooks) error {
	err := cmd.Wait()
	deadline, stop := hooks.drainTimer()
	defer stop()
	exited()
	if hooks != nil && hooks.afterWait != nil {
		hooks.afterWait()
	}
	select {
	case <-readersDone:
	case <-deadline:
		p.closeReaders()
		<-readersDone
	}
	return err
}

// outputHooks provides per-run gates and a controlled drain clock for tests.
type outputHooks struct {
	afterClose    func(*os.File, error)
	afterStart    func(*commandOutput)
	afterWait     func()
	afterReader   func()
	newDrainTimer func(time.Duration) (<-chan time.Time, func())
}

func (h *outputHooks) started(p *commandOutput) {
	if h != nil && h.afterStart != nil {
		h.afterStart(p)
	}
}

func (h *outputHooks) readerFinished() {
	if h != nil && h.afterReader != nil {
		h.afterReader()
	}
}

func (h *outputHooks) drainTimer() (<-chan time.Time, func()) {
	if h != nil && h.newDrainTimer != nil {
		return h.newDrainTimer(outputDrainTimeout)
	}
	timer := time.NewTimer(outputDrainTimeout)
	return timer.C, func() { timer.Stop() }
}

func (h *outputHooks) closed(f *os.File, err error) {
	if h != nil && h.afterClose != nil {
		h.afterClose(f, err)
	}
}
