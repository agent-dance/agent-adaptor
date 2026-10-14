//go:build !windows

package systemprompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func renameTestFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// An earlier failed directory removal must not make Close accept the path's
// next occupant. The open directory anchor stays available across the retry.
func TestFileCloseRetryRejectsDirectoryReplacement(t *testing.T) {
	f, err := Materialize(context.Background(), "private text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dir := filepath.Dir(f.Path())
	unknown := filepath.Join(dir, "unowned")
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err == nil {
		t.Fatal("Close removed an unknown sibling")
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrPermission) {
		t.Fatal("Close retry adopted replacement directory", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatal("replacement directory was deleted", err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, "unowned")); err != nil || string(data) != "keep" {
		t.Fatal("original unknown sibling changed", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
