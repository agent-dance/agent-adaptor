//go:build !windows

package systemprompt

import "os"

func unsafeInfo(info os.FileInfo) bool { return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 }

func makePrivateDirectory() (string, error) {
	return os.MkdirTemp(os.TempDir(), "agent-adaptor-append-*")
}

func protectFile(*os.File) error { return nil }

func verifyPrivateObject(string, os.FileInfo, bool) error { return nil }
