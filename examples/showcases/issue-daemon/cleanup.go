package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Windows can briefly retain a sharing lock after a CLI and its helpers exit.
// Only sharing violations are retried; a persistent lock remains a failed check.
func removeWorkspace(ctx context.Context, dir string) error {
	for {
		err := os.RemoveAll(dir)
		if err == nil || runtime.GOOS != "windows" ||
			(!errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33))) {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}
