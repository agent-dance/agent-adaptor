//go:build !windows

package cursor

import (
	"fmt"
	"os"
)

func cursorMakePrivateDir(root *os.Root, name string) error { return root.Mkdir(name, 0700) }
func cursorWritePrivateFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
func verifyCursorPrivateObject(f *os.File, dir bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != dir || !dir && !info.Mode().IsRegular() {
		return fmt.Errorf("cursor private object has unsafe type")
	}
	expected := os.FileMode(0600)
	if dir {
		expected = 0700
	}
	if info.Mode().Perm() != expected {
		return fmt.Errorf("cursor private object permissions changed")
	}
	return nil
}
