//go:build !windows

package systemprompt

import (
	"errors"
	"os"
	"path/filepath"
)

func makePrivateDirectory() (*os.File, error) {
	dir, err := os.MkdirTemp(os.TempDir(), "agent-adaptor-append-*")
	if err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, errors.Join(err, os.Remove(dir))
	}
	return f, nil
}

func makePrivatePending(_ *os.File, root *os.Root) (*os.File, error) {
	return root.OpenFile(".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func removePrivateDirectory(f *File) error {
	if f.parent != nil {
		return f.parent.Remove(filepath.Base(f.dir))
	}
	return os.Remove(f.dir)
}

func privateDirectoryInfo(path string) (os.FileInfo, error) { return os.Lstat(path) }
func verifyPrivateObject(*os.File, bool) error              { return nil }

func unsafeInfo(info os.FileInfo) bool { return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 }
