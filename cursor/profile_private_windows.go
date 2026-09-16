//go:build windows

package cursor

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"unsafe"
)

// Match the repository's proven private-object SID/protected-DACL scheme.
// Every projection directory/file is protected at creation, before resource
// bytes are written; safety does not depend on inherited selected-profile ACLs.
func cursorWindowsSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func cursorWindowsDescriptor(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := cursorWindowsSID()
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

func createCursorWindowsObject(parent *os.File, name string, dir bool) (*os.File, error) {
	sd, err := cursorWindowsDescriptor(dir)
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
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_CREATE, options, 0, 0)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			err = status.Errno()
		}
		return nil, &os.PathError{Op: "create private Cursor projection object", Path: filepath.Join(parent.Name(), name), Err: err}
	}
	return os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name)), nil
}

func verifyCursorPrivateObject(file *os.File, dir bool) error {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir || (!dir && info.NumberOfLinks != 1) {
		return fmt.Errorf("unsafe Cursor projection object: %w", os.ErrPermission)
	}
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return fmt.Errorf("Cursor projection filesystem lacks persistent ACLs: %w", os.ErrPermission)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	return validateCursorWindowsDescriptor(sd, dir)
}

func validateCursorWindowsDescriptor(sd *windows.SECURITY_DESCRIPTOR, dir bool) error {
	invalid := fmt.Errorf("Cursor projection owner or DACL is not private: %w", os.ErrPermission)
	current, err := cursorWindowsSID()
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

func cursorMakePrivateDir(root *os.Root, name string) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := createCursorWindowsObject(parent, name, true)
	if err != nil {
		return err
	}
	return errors.Join(verifyCursorPrivateObject(f, true), f.Close())
}
func cursorWritePrivateFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	f, err := createCursorWindowsObject(parent, name, false)
	if err != nil {
		return err
	}
	if err := verifyCursorPrivateObject(f, false); err != nil {
		return errors.Join(err, f.Close())
	}
	_, err = f.Write(data)
	return errors.Join(err, verifyCursorPrivateObject(f, false), f.Close())
}
