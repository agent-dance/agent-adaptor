//go:build darwin || linux

package cursor

import (
	"golang.org/x/sys/unix"
	"os"
)

func openCursorRegularFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
