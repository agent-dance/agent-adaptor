//go:build windows

package systemprompt

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func unsafeInfo(info os.FileInfo) bool {
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func privateSecurity(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	flags := ""
	if dir {
		// Children are private from creation, before any prompt bytes are
		// written. The published file also receives its own protected DACL.
		flags = "OICI"
	}
	sddl := "O:" + user.String() + "D:P(A;" + flags + ";FA;;;" + user.String() + ")"
	if user.String() != "S-1-5-18" {
		sddl += "(A;" + flags + ";FA;;;SY)"
	}
	return windows.SecurityDescriptorFromString(sddl)
}

// Chmod cannot create a private Windows directory. Supply the protected DACL
// and creator SID at creation so no inherited broad ACL exposes prompt data.
func makePrivateDirectory() (string, error) {
	sd, err := privateSecurity(true)
	if err != nil {
		return "", err
	}
	attrs := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for attempt := 0; attempt < 20; attempt++ {
		path := filepath.Join(os.TempDir(), "agent-adaptor-append-"+rand.Text())
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return "", err
		}
		if err := windows.CreateDirectory(p, &attrs); err != nil {
			if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				continue
			}
			return "", &os.PathError{Op: "mkdir", Path: path, Err: err}
		}
		return path, nil
	}
	return "", &os.PathError{Op: "mkdir", Path: os.TempDir(), Err: os.ErrExist}
}

func openPrivateObject(path string, access uint32) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, access|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func protectFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	object, err := openPrivateObject(file.Name(), windows.WRITE_DAC|windows.WRITE_OWNER)
	if err != nil {
		return err
	}
	defer object.Close()
	opened, err := object.Stat()
	if err != nil {
		return err
	}
	if unsafeInfo(opened) || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return os.ErrPermission
	}
	sd, err := privateSecurity(false)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(windows.Handle(object.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, acl, nil); err != nil {
		return err
	}
	return verifyWindowsObject(object, false)
}

func verifyPrivateObject(path string, expected os.FileInfo, dir bool) error {
	object, err := openPrivateObject(path, 0)
	if err != nil {
		return err
	}
	defer object.Close()
	info, err := object.Stat()
	if err != nil {
		return err
	}
	if expected == nil || !os.SameFile(expected, info) {
		return os.ErrPermission
	}
	return verifyWindowsObject(object, dir)
}

func verifyWindowsObject(object *os.File, dir bool) error {
	h := windows.Handle(object.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir || (!dir && info.NumberOfLinks != 1) {
		return os.ErrPermission
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.Equals(user) {
		return os.ErrPermission
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return os.ErrPermission
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return os.ErrPermission
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	expectedCount := uint16(2)
	if user.Equals(system) {
		expectedCount = 1
	}
	if acl.AceCount != expectedCount {
		return os.ErrPermission
	}
	flags := byte(0)
	if dir {
		flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	seenUser, seenSystem := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != flags || ace.Mask != windows.ACCESS_MASK(0x1f01ff) {
			return os.ErrPermission
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(user) {
			seenUser = true
			if sid.Equals(system) {
				seenSystem = true
			}
		} else if sid.Equals(system) {
			seenSystem = true
		} else {
			return os.ErrPermission
		}
	}
	if !seenUser || !seenSystem {
		return os.ErrPermission
	}
	return nil
}
