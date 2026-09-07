//go:build linux || darwin

package hostedprofile

import (
	"errors"
	"fmt"
	"os"

	"github.com/agent-dance/agent-adaptor/profile"
	"golang.org/x/sys/unix"
)

func canonicalDirectory(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", unsafe("source is not directory", nil)
	}
	s, ok := st.Sys().(*unix.Stat_t)
	if !ok { // os exposes syscall.Stat_t, use the descriptor instead.
		var stat unix.Stat_t
		if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
			return "", "", err
		}
		s = &stat
	}
	if err := supportedFilesystem(f); err != nil {
		return "", "", err
	}
	return path, fmt.Sprintf("%x:%x", s.Dev, s.Ino), nil
}
func validateObject(f *os.File, dir bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return unsafe("non-private owner or permissions", nil)
	}
	if dir {
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return unsafe("directory required", nil)
		}
	} else if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 {
		return unsafe("single-link private regular control file required", nil)
	}
	return supportedFilesystem(f)
}
func secureObject(f *os.File, dir bool) error {
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return validateObject(f, dir)
}
func noFollowFlags() int { return unix.O_NOFOLLOW | unix.O_CLOEXEC }
func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return profile.ErrInUse
	}
	return err
}
func unlockFile(f *os.File) error    { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func syncDirectory(f *os.File) error { return f.Sync() }

func renameWithin(r *os.Root, old, next string) error { return r.Rename(old, next) }

func openOwnershipLock(r *os.Root) (*os.File, error) { return openChecked(r, "owner.lock", false) }
