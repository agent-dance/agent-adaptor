//go:build unix

package cursor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const hookFileName = "hooks.json"

// Keep every directory and the owned inode open until cleanup. No operation
// after opening the workspace resolves a parent through an absolute pathname.
type cursorHookFile struct {
	root, dir, file *os.File
	data            []byte
}

// Isolated so tests can replace an entry at the exact cleanup rename boundary.
var cursorHookRename = unix.Renameat

func openHookDir(parent *os.File, name string) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	var fd int
	var err error
	if parent == nil {
		fd, err = unix.Open(filepath.Clean(name), flags, 0)
	} else {
		fd, err = unix.Openat(int(parent.Fd()), name, flags, 0)
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func installCursorHook(cwd string, data []byte) (_ *cursorHookFile, err error) {
	h := &cursorHookFile{data: data}
	defer func() {
		if err != nil {
			err = errors.Join(err, h.close())
		}
	}()
	h.root, err = openHookDir(nil, cwd)
	if err != nil {
		return nil, fmt.Errorf("open Cursor workspace directory: %w", err)
	}
	if err = unix.Mkdirat(int(h.root.Fd()), ".cursor", 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf("create Cursor hook directory: %w", err)
	}
	h.dir, err = openHookDir(h.root, ".cursor")
	if err != nil {
		return nil, fmt.Errorf("open plain Cursor hook directory: %w", err)
	}
	fd, err := unix.Openat(int(h.dir.Fd()), hookFileName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire exclusive Cursor hooks.json: %w", err)
	}
	h.file = os.NewFile(uintptr(fd), hookFileName)
	n, err := h.file.Write(data)
	if err != nil {
		// Cleanup may remove only the bytes we actually wrote, never a
		// concurrently substituted or modified file.
		h.data = data[:n]
		return nil, errors.Join(fmt.Errorf("write Cursor hooks.json: %w", err), h.remove())
	}
	if err := h.checkDirectory(); err != nil {
		return nil, errors.Join(err, h.remove())
	}
	return h, nil
}

func (h *cursorHookFile) close() error {
	var err error
	for _, f := range []*os.File{h.file, h.dir, h.root} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	h.file, h.dir, h.root = nil, nil, nil
	return err
}

func sameHookFile(a, b *os.File) error {
	ai, aerr := a.Stat()
	bi, berr := b.Stat()
	if err := errors.Join(aerr, berr); err != nil {
		return err
	}
	if !os.SameFile(ai, bi) {
		return errors.New("Cursor hook path was replaced during the run; leaving it untouched")
	}
	return nil
}

func (h *cursorHookFile) checkDirectory() error {
	current, err := openHookDir(h.root, ".cursor")
	if err != nil {
		return fmt.Errorf("reopen plain Cursor hook directory: %w", err)
	}
	return errors.Join(sameHookFile(h.dir, current), current.Close())
}

func (h *cursorHookFile) matches(dir *os.File) (err error) {
	// NONBLOCK avoids hanging on a substituted FIFO/device. NOFOLLOW also
	// prevents reading a symlink target, even when it has identical contents.
	fd, err := unix.Openat(int(dir.Fd()), hookFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), hookFileName)
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := sameHookFile(h.file, f); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(h.data))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, h.data) {
		return errors.New("Cursor hooks.json was modified during the run; leaving it untouched")
	}
	return nil
}

func (h *cursorHookFile) remove() (err error) {
	if h == nil {
		return nil
	}
	defer func() { err = errors.Join(err, h.close()) }()
	// Refuse a renamed/replaced .cursor, rather than cleaning through its new
	// pathname or silently removing a file from a detached directory.
	if err := h.checkDirectory(); err != nil {
		return err
	}
	if err := h.matches(h.dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// A check followed by unlinkat would still delete a user's replacement
	// arriving between those calls. Capture the entry in a fresh private
	// directory first, then verify the captured inode and contents.
	name, err := hookRandom(16)
	if err != nil {
		return err
	}
	name = ".agent-adaptor-hook-cleanup-" + name
	dirfd := int(h.dir.Fd())
	if err := unix.Mkdirat(dirfd, name, 0o700); err != nil {
		return fmt.Errorf("create private Cursor hook cleanup directory: %w", err)
	}
	stage, err := openHookDir(h.dir, name)
	if err != nil {
		// Do not remove a directory whose identity we could not establish.
		return fmt.Errorf("open private Cursor hook cleanup directory %s: %w", name, err)
	}
	defer func() {
		// Never recurse: an unexpected entry must survive for recovery.
		current, openErr := openHookDir(h.dir, name)
		if openErr == nil {
			openErr = errors.Join(sameHookFile(stage, current), current.Close())
		}
		if openErr == nil {
			openErr = unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
		}
		err = errors.Join(err, openErr, stage.Close())
	}()
	stagefd := int(stage.Fd())
	if err := cursorHookRename(dirfd, hookFileName, stagefd, hookFileName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := h.matches(stage); err != nil {
		// linkat never overwrites a concurrently installed hooks.json and does
		// not follow symlinks. If restoration is impossible, retain the entry
		// in the cleanup directory and report its location instead of deleting.
		if restoreErr := unix.Linkat(stagefd, hookFileName, dirfd, hookFileName, 0); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("preserved displaced Cursor hooks at %s: %w", filepath.Join(h.root.Name(), ".cursor", name, hookFileName), restoreErr))
		}
		return errors.Join(err, unix.Unlinkat(stagefd, hookFileName, 0))
	}
	return unix.Unlinkat(stagefd, hookFileName, 0)
}
