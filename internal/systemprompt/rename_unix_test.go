//go:build !windows

package systemprompt

import "os"

func renameTestFile(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
