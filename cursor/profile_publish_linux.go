//go:build linux

package cursor

import (
	"golang.org/x/sys/unix"
	"os"
)

func publishCursorPrivateHome(root *os.Root, from, to string) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Renameat2(int(f.Fd()), from, int(f.Fd()), to, unix.RENAME_NOREPLACE)
}
