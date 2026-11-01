//go:build !unix

package cursor

import "errors"

type cursorHookFile struct{}

func installCursorHook(string, []byte) (*cursorHookFile, error) {
	return nil, errors.New("Cursor hook relay requires Unix directory-relative filesystem operations")
}

func (*cursorHookFile) remove() error { return nil }
