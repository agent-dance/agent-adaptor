//go:build darwin

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
	return unix.RenameatxNp(int(f.Fd()), from, int(f.Fd()), to, unix.RENAME_EXCL)
}
