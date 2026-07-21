package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// console serializes streaming deltas and full-line log records onto one
// writer. The leader run, three delegated roles, the MCP sidecar, and the
// delegation trace all emit concurrently, so a shared lock keeps a streamed
// line from being torn apart by an unrelated lifecycle log.
type console struct {
	mu          sync.Mutex
	w           io.Writer
	activeLabel string
	midLine     bool
}

// term is the process-wide console shared by every workflow goroutine.
var term = newConsole(os.Stderr)

func newConsole(w io.Writer) *console {
	return &console{w: w}
}

// Logf writes a self-contained line, first closing any open streamed line so
// lifecycle records never interleave with streaming text mid-line.
func (c *console) Logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.breakLine()
	fmt.Fprintln(c.w, strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

// Stream appends a streaming delta attributed to label. When the active
// speaker changes it prints a header so interleaved leader/role output stays
// readable.
func (c *console) Stream(label, delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if label != c.activeLabel {
		c.breakLine()
		fmt.Fprintf(c.w, "\u250c\u2500 %s\n", label)
		c.activeLabel = label
	}
	_, _ = io.WriteString(c.w, delta)
	c.midLine = !strings.HasSuffix(delta, "\n")
}

func (c *console) breakLine() {
	if c.midLine {
		_, _ = io.WriteString(c.w, "\n")
		c.midLine = false
	}
	c.activeLabel = ""
}
