//go:build !windows

package codebuddy

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func unsafeCodeBuddyLiveInfo(info os.FileInfo) bool {
	return info == nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

func createCodeBuddyPrivateObject(root *os.Root, name string, dir bool) (*os.File, error) {
	var file *os.File
	var err error
	if dir {
		if err = root.Mkdir(name, 0700); err == nil {
			file, err = root.Open(name)
		}
	} else {
		file, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return nil, err
	}
	if err = verifyCodeBuddyPrivateObject(file, dir); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func verifyCodeBuddyPrivateObject(file *os.File, dir bool) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	if info.IsDir() != dir || !dir && !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return os.ErrPermission
	}
	return nil
}

func openCodeBuddyLiveSeed(root *os.Root, name string) (file *os.File, resultErr error) {
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := parent.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
			if file != nil {
				resultErr = errors.Join(resultErr, file.Close())
				file = nil
			}
		}
	}()
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func TestCodeBuddyNativeAuthFixturePOSIXUnsafeNodesAndCleanup(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo.info")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeBuddyLiveSeed(fifo, true); err == nil {
		t.Fatal("FIFO seed accepted")
	}
	source := codeBuddySyntheticSeed(t)
	if err := os.Chmod(source, 0000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := readCodeBuddyLiveSeed(source, true); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("permission error swallowed: %v", err)
		}
	}
	if err := os.Chmod(source, 0600); err != nil {
		t.Fatal(err)
	}
	f := newCodeBuddyLiveHome(t)
	if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: source}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside remains"), 0600); err != nil {
		t.Fatal(err)
	}
	moved := f.home + ".moved"
	if err := os.Rename(f.home, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, f.home); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("replaced HOME not reported: %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "outside remains" {
		t.Fatal("cleanup followed replacement symlink")
	}
	if entries, err := os.ReadDir(moved); err != nil || len(entries) != 0 {
		t.Fatal("held owned HOME contents not cleaned")
	}
}
