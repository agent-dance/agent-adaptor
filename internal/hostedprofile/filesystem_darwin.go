//go:build darwin

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
	name := unix.ByteSliceToString(s.Fstypename[:])
	if s.Flags&unix.MNT_LOCAL == 0 || (name != "apfs" && name != "hfs") {
		return profile.ErrUnsupportedFilesystem
	}
	return nil
}

func publishWithin(r *os.Root, old, next string) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.RenameatxNp(int(f.Fd()), old, int(f.Fd()), next, unix.RENAME_EXCL)
}
