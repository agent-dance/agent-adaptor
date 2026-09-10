//go:build windows

package systemprompt

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A pathname-only CreateDirectory followed by Chmod leaves Windows children
// governed by inherited ACLs. NtCreateFile installs the protected descriptor at
// creation and returns the same directory handle without delete sharing. Even
// a shared parent granting DELETE_CHILD cannot replace it while File is live.
func makePrivateDirectory() (*os.File, error) {
	parent, err := os.Open(os.TempDir())
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	for range 100 {
		name := "agent-adaptor-append-" + rand.Text()
		directory, err := createPrivateWindowsObject(parent, name, true)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return directory, err
	}
	return nil, fmt.Errorf("allocate append directory: %w", os.ErrExist)
}

func makePrivatePending(directory *os.File, _ *os.Root) (*os.File, error) {
	return createPrivateWindowsObject(directory, ".pending", false)
}

func currentWindowsSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func privateWindowsDescriptor(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := currentWindowsSID()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if dir {
		inheritance = "OICI"
	}
	sddl := "O:" + owner.String() + "D:P(A;" + inheritance + ";FA;;;" + owner.String() + ")"
	if owner.String() != "S-1-5-18" {
		sddl += "(A;" + inheritance + ";FA;;;SY)"
	}
	return windows.SecurityDescriptorFromString(sddl)
}

func createPrivateWindowsObject(parent *os.File, name string, dir bool) (*os.File, error) {
	sd, err := privateWindowsDescriptor(dir)
	if err != nil {
		return nil, err
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory:      windows.Handle(parent.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: sd,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_REPARSE_POINT | windows.FILE_NON_DIRECTORY_FILE)
	access := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE)
	if dir {
		options &^= windows.FILE_NON_DIRECTORY_FILE
		options |= windows.FILE_DIRECTORY_FILE
		access = windows.FILE_GENERIC_READ | windows.DELETE
	}
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_CREATE, options, 0, 0)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			err = status.Errno()
		}
		return nil, &os.PathError{Op: "create private append object", Path: filepath.Join(parent.Name(), name), Err: err}
	}
	return os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name)), nil
}

// publishPrivatePending consumes the pending handle, including on failure.
func publishPrivatePending(file *os.File, _ *os.Root, name string) (resultErr error) {
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/:`) {
		return os.ErrInvalid
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	// A same-directory rename uses a NULL RootDirectory and a simple basename.
	// Supplying the directory handle makes Windows open the target directory
	// again, which can conflict with our lifetime DELETE pin on Server 2022.
	// Rename the original pending handle without releasing either identity pin.
	// FILE_RENAME_INFORMATION layout, including its trailing UTF-16 buffer:
	// https://learn.microsoft.com/windows-hardware/drivers/ddi/ntifs/ns-ntifs-_file_rename_information
	type renameInformation struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	}
	var layout renameInformation
	buffer := make([]byte, int(unsafe.Sizeof(layout))+len(encoded)*2)
	info := (*renameInformation)(unsafe.Pointer(&buffer[0]))
	info.FileNameLength = uint32((len(encoded) - 1) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(encoded)), encoded)
	// Zero ReplaceIfExists rejects a conflicting file rather than replacing it.
	err = windows.NtSetInformationFile(windows.Handle(file.Fd()), &windows.IO_STATUS_BLOCK{},
		&buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
	if status, ok := err.(windows.NTStatus); ok {
		err = status.Errno()
	}
	if err != nil {
		return &os.LinkError{Op: "publish private append", Old: file.Name(), New: name, Err: err}
	}
	return nil
}

func removePrivateDirectory(f *File) error {
	if f.directory == nil {
		return os.ErrClosed
	}
	// Mark the proven, still-pinned directory for deletion through its creation
	// handle. Releasing the pin and then removing a pathname would let another
	// user with DELETE_CHILD on the shared parent replace that pathname first.
	deleteFile := byte(1) // FILE_DISPOSITION_INFO.DeleteFile (BOOLEAN).
	return windows.SetFileInformationByHandle(windows.Handle(f.directory.Fd()), windows.FileDispositionInfo, &deleteFile, 1)
}

func privateDirectoryInfo(path string) (os.FileInfo, error) {
	fullPath, err := syscall.FullPath(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(fullPath, `\\?\`) {
		if strings.HasPrefix(fullPath, `\\`) {
			fullPath = `\\?\UNC\` + fullPath[2:]
		} else {
			fullPath = `\\?\` + fullPath
		}
	}
	name, err := windows.UTF16PtrFromString(fullPath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(handle), path)
	info, statErr := directory.Stat()
	permissionErr := verifyPrivateObject(directory, true)
	closeErr := directory.Close()
	if err := errors.Join(statErr, permissionErr, closeErr); err != nil {
		return nil, err
	}
	// Return handle-derived identity: os.Lstat defers loading file IDs through
	// a later open without delete sharing, which conflicts with the live pin.
	return info, nil
}

// Only the current owner and the Windows system administrators may have access.
// Reject inherited/unprotected, NULL, unfamiliar or restricted-owner ACLs;
// chmod's synthetic mode bits cannot establish any of these properties.
func verifyPrivateObject(file *os.File, dir bool) error {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir || (!dir && info.NumberOfLinks != 1) {
		return fmt.Errorf("unsafe append object: %w", os.ErrPermission)
	}
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return fmt.Errorf("append filesystem lacks persistent ACLs: %w", os.ErrPermission)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	return validateWindowsDescriptor(sd, dir)
}

func validateWindowsDescriptor(sd *windows.SECURITY_DESCRIPTOR, dir bool) error {
	invalid := fmt.Errorf("append owner or DACL is not private: %w", os.ErrPermission)
	current, err := currentWindowsSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(current) {
		return invalid
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return invalid
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount < 1 || acl.AceCount > 3 {
		return invalid
	}
	allowedFlags := byte(0)
	if dir {
		allowedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seen := make(map[string]bool, acl.AceCount)
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return errors.Join(invalid, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags & ^allowedFlags != 0 || ace.Mask != 0x1f01ff {
			return invalid
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if seen[sid] || (sid != current.String() && sid != "S-1-5-18" && sid != "S-1-5-32-544") {
			return invalid
		}
		seen[sid] = true
	}
	if !seen[current.String()] {
		return invalid
	}
	return nil
}

func unsafeInfo(info os.FileInfo) bool {
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
