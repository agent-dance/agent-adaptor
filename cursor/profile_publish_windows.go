//go:build windows

package cursor

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Publish through original handles with ReplaceIfExists=false. Root.Rename
// uses replacement semantics on Windows and is not an exclusive publication.
func publishCursorPrivateHome(root *os.Root, from, to string) error {
	for _, name := range []string{from, to} {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
			return os.ErrInvalid
		}
	}
	before, err := root.Lstat(from)
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return os.ErrPermission
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	sourceName, err := windows.NewNTUnicodeString(from)
	if err != nil {
		return err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: sourceName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.DELETE, &attrs, &windows.IO_STATUS_BLOCK{}, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		return cursorWindowsStatus(err)
	}
	file := os.NewFile(uintptr(handle), from)
	defer file.Close()
	opened, err := file.Stat()
	current, pathErr := root.Lstat(from)
	if err != nil || pathErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, current) || current.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor private HOME staging identity changed")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return os.ErrPermission
	}
	encoded, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	type renameInformation struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	}
	var layout renameInformation
	buffer := make([]byte, int(unsafe.Sizeof(layout))+len(encoded)*2)
	rename := (*renameInformation)(unsafe.Pointer(&buffer[0]))
	rename.RootDirectory = windows.Handle(parent.Fd())
	rename.FileNameLength = uint32((len(encoded) - 1) * 2)
	copy(unsafe.Slice(&rename.FileName[0], len(encoded)), encoded)
	return cursorWindowsStatus(windows.NtSetInformationFile(handle, &windows.IO_STATUS_BLOCK{}, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation))
}

func cursorWindowsStatus(err error) error {
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
