//go:build linux

package hostedprofile

import (
	"github.com/agent-dance/agent-adaptor/profile"
	"golang.org/x/sys/unix"
	"os"
)

func supportedFilesystem(f *os.File) error {
	var s unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &s); err != nil {
		return err
	}
	switch uint64(s.Type) {
	case 0xef53, 0x58465342, 0x01021994:
		return nil
	}
	return profile.ErrUnsupportedFilesystem
}

func publishWithin(r *os.Root, old, next string) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Renameat2(int(f.Fd()), old, int(f.Fd()), next, unix.RENAME_NOREPLACE)
}
