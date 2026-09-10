//go:build windows

package systemprompt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Rename an already published file to construct an actual replacement or link
// attack while the production directory pin stays open. MoveFileEx, used by
// os.Rename, reopens the target directory and conflicts with that pin on Server
// 2022. This independent fixture opens its own source handle and uses a fixed
// ABI buffer; it does not call the production pending-file publisher.
func renameTestFile(oldpath, newpath string) (resultErr error) {
	if filepath.Dir(oldpath) != filepath.Dir(newpath) {
		return os.ErrInvalid
	}
	name := filepath.Base(newpath)
	if name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
		return os.ErrInvalid
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	// FILE_RENAME_INFORMATION with a NULL RootDirectory renames within the
	// source file's directory. The current directory is not used or changed.
	// https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information
	info := struct {
		ReplaceIfExists byte
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [syscall.MAX_PATH]uint16
	}{}
	if len(encoded) > len(info.FileName) {
		return os.ErrInvalid
	}
	copy(info.FileName[:], encoded)
	info.FileNameLength = uint32((len(encoded) - 1) * 2)
	fullPath, err := syscall.FullPath(oldpath)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(fullPath, `\\?\`) {
		if strings.HasPrefix(fullPath, `\\`) {
			fullPath = `\\?\UNC\` + fullPath[2:]
		} else {
			fullPath = `\\?\` + fullPath
		}
	}
	source, err := windows.UTF16PtrFromString(fullPath)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(source, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, windows.CloseHandle(handle)) }()
	err = windows.NtSetInformationFile(handle, &windows.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation)
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if err != nil {
		return &os.LinkError{Op: "rename fixture file", Old: oldpath, New: newpath, Err: err}
	}
	return nil
}
