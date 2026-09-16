//go:build darwin || linux

package cursor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"golang.org/x/sys/unix"
)

func TestCursorConfigurationRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli-config.json")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := cursorConfigState([]driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: filepath.Dir(path)}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO configuration accepted")
		}
	case <-time.After(time.Second):
		// Release the old blocking implementation's open/read before failing, so
		// this deterministic regression never leaves a test goroutine stuck.
		f, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			f.Close()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("configuration inspection blocked on FIFO")
	}
}
