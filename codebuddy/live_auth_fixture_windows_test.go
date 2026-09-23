//go:build windows

package codebuddy

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func unsafeCodeBuddyLiveInfo(info os.FileInfo) bool {
	if info == nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// Match the repository's proven private-object SID/protected-DACL scheme.
// Every projection directory/file is protected at creation, before resource
// bytes are written; safety does not depend on inherited selected-profile ACLs.
func codeBuddyLiveWindowsSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func codeBuddyLiveWindowsDescriptor(dir bool) (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := codeBuddyLiveWindowsSID()
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

func createCodeBuddyLiveWindowsObject(parent *os.File, name string, dir bool) (*os.File, error) {
	sd, err := codeBuddyLiveWindowsDescriptor(dir)
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
		// A lifetime pin needs to withhold FILE_SHARE_DELETE, not request
		// DELETE itself. Holding DELETE would block ordinary directory readers
		// whose own sharing mode does not include FILE_SHARE_DELETE.
		access = windows.FILE_GENERIC_READ
	}
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access, &attributes, &windows.IO_STATUS_BLOCK{}, nil,
		windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_CREATE, options, 0, 0)
	if err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			err = status.Errno()
		}
		return nil, &os.PathError{Op: "create private CodeBuddy live fixture object", Path: filepath.Join(parent.Name(), name), Err: err}
	}
	return os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name)), nil
}

func verifyCodeBuddyPrivateObject(file *os.File, dir bool) error {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir || (!dir && info.NumberOfLinks != 1) {
		return fmt.Errorf("unsafe CodeBuddy live fixture object: %w", os.ErrPermission)
	}
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return fmt.Errorf("CodeBuddy live fixture filesystem lacks persistent ACLs: %w", os.ErrPermission)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.Join(os.ErrPermission, err)
	}
	return validateCodeBuddyLiveWindowsDescriptor(sd, dir)
}

func validateCodeBuddyLiveWindowsDescriptor(sd *windows.SECURITY_DESCRIPTOR, dir bool) error {
	invalid := fmt.Errorf("CodeBuddy live fixture owner or DACL is not private: %w", os.ErrPermission)
	current, err := codeBuddyLiveWindowsSID()
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

func createCodeBuddyPrivateObject(root *os.Root, name string, dir bool) (file *os.File, resultErr error) {
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := parent.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
			if file != nil {
				resultErr = errors.Join(resultErr, file.Close())
				file = nil
			}
		}
	}()
	file, err = createCodeBuddyLiveWindowsObject(parent, name, dir)
	if err != nil {
		return nil, err
	}
	if err := verifyCodeBuddyPrivateObject(file, dir); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openCodeBuddyLiveSeed(root *os.Root, name string) (*os.File, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return nil, errors.Join(os.ErrPermission, file.Close())
	}
	return file, nil
}

func TestCodeBuddyNativeAuthFixtureWindowsProtectedCreationAndPin(t *testing.T) {
	parent := t.TempDir()
	permissive, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := permissive.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	f, err := makeCodeBuddyLiveHome(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := verifyCodeBuddyPrivateObject(f.directory, true); err != nil {
		t.Fatal("private HOME inherited permissive parent", err)
	}
	file, err := createCodeBuddyPrivateObject(f.root, "creation-check", false)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the empty object before any credential byte can be written.
	if info, err := file.Stat(); err != nil || info.Size() != 0 {
		t.Fatal("creation check was not before first write")
	}
	if err := verifyCodeBuddyPrivateObject(file, false); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.home, f.home+".moved"); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("owned HOME lifetime pin: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(filepath.Join(f.home, "creation-check"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	opened, err := f.root.Open("creation-check")
	if err != nil {
		t.Fatal(err)
	}
	verifyErr := verifyCodeBuddyPrivateObject(opened, false)
	opened.Close()
	if !errors.Is(verifyErr, os.ErrPermission) {
		t.Fatal("world-readable seed DACL accepted")
	}
}

func TestCodeBuddyNativeAuthFixtureWindowsRejectsDescriptorDrift(t *testing.T) {
	owner, err := codeBuddyLiveWindowsSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, sddl := range []string{
		"O:" + owner.String() + "D:(A;;FA;;;" + owner.String() + ")",
		"O:" + owner.String() + "D:P(A;;FA;;;WD)",
		"O:BAD:P(A;;FA;;;" + owner.String() + ")",
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCodeBuddyLiveWindowsDescriptor(sd, false); err == nil {
			t.Fatal("unsafe descriptor accepted")
		}
	}
}

// Isolate the Windows sharing rule from the fixture implementation: an open
// handle's DELETE access requires every later opener to share delete access.
// Ordinary os.ReadDir does not share delete; omitting DELETE from desired access
// still pins rename because the held handle itself does not share delete.
func TestCodeBuddyNativeAuthFixtureWindowsDirectorySharingControl(t *testing.T) {
	for _, deleteAccess := range []bool{true, false} {
		t.Run(fmt.Sprintf("delete_access=%t", deleteAccess), func(t *testing.T) {
			path := t.TempDir()
			t.Cleanup(func() {
				if err := os.RemoveAll(path + ".moved"); err != nil {
					t.Error(err)
				}
			})
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			access := uint32(windows.GENERIC_READ)
			if deleteAccess {
				access |= windows.DELETE
			}
			handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(handle), path)
			t.Cleanup(func() {
				if file != nil {
					if err := file.Close(); err != nil {
						t.Error(err)
					}
				}
			})
			entries, readErr := os.ReadDir(path)
			var readCode syscall.Errno
			errors.As(readErr, &readCode)
			t.Logf("desired_delete=%t read_errno=%d read_error=%v entry_count=%d", deleteAccess, readCode, readErr, len(entries))
			if deleteAccess {
				if !errors.Is(readErr, windows.ERROR_SHARING_VIOLATION) {
					t.Fatalf("DELETE access control: read_error=%v want=%v", readErr, windows.ERROR_SHARING_VIOLATION)
				}
			} else if readErr != nil || len(entries) != 0 {
				t.Fatalf("read-only access control: read_error=%v entry_count=%d want=0", readErr, len(entries))
			}
			renameErr := os.Rename(path, path+".moved")
			var renameCode syscall.Errno
			errors.As(renameErr, &renameCode)
			t.Logf("desired_delete=%t rename_errno=%d", deleteAccess, renameCode)
			if !errors.Is(renameErr, windows.ERROR_SHARING_VIOLATION) {
				t.Fatalf("held directory rename: error=%v want=%v", renameErr, windows.ERROR_SHARING_VIOLATION)
			}
			closeErr := file.Close()
			file = nil
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
				t.Fatalf("closed control: read_error=%v entry_count=%d want=0", err, len(entries))
			}
		})
	}
}

func TestCodeBuddyNativeAuthFixtureWindowsPinnedDirectoriesReadable(t *testing.T) {
	f := newCodeBuddyLiveHome(t)
	if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: codeBuddySyntheticSeed(t)}); err != nil {
		t.Fatal(err)
	}
	relative, err := codeBuddyNativeAuthPath("windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.home, f.profile, filepath.Dir(filepath.Join(f.home, relative))} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatalf("pinned fixture ordinary ReadDir: error=%v entry_count=%d", err, len(entries))
		}
		if err := os.Rename(path, path+".moved"); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("readable fixture lifetime pin: error=%v want=%v", err, windows.ERROR_SHARING_VIOLATION)
		}
	}
}
