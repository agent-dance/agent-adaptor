//go:build windows

package systemprompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsFilePrivateACLAndOwnership(t *testing.T) {
	f, err := Materialize(context.Background(), "private text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, path := range []string{filepath.Dir(f.Path()), f.Path()} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			original, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			control, _, err := original.Control()
			if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
				t.Fatalf("private object has inheritable DACL: control=%x, err=%v", control, err)
			}
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			owner, _, err := original.Owner()
			if err != nil || !owner.Equals(user.User.Sid) {
				t.Fatal("private object's owner is not the creating user", err)
			}
			acl, _, err := original.DACL()
			if err != nil || acl == nil {
				t.Fatal("private object has no DACL", err)
			}
			t.Cleanup(func() {
				if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
					t.Error(err)
				}
			})
			unsafe, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
			if err != nil {
				t.Fatal(err)
			}
			unsafeACL, _, err := unsafe.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, unsafeACL, nil); err != nil {
				t.Fatal(err)
			}
			if err := f.Verify(context.Background()); !errors.Is(err, os.ErrPermission) {
				t.Fatal("Everyone ACL accepted", err)
			}
			if err := f.Close(); !errors.Is(err, os.ErrPermission) {
				t.Fatal("Close adopted an object with changed ACL", err)
			}
			if text, err := os.ReadFile(f.Path()); err != nil || string(text) != "private text" {
				t.Fatal("failed Close changed private data", err)
			}
		})
	}
	if err := f.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsFileRejectsHardlink(t *testing.T) {
	f, err := Materialize(context.Background(), "private text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	link := filepath.Join(t.TempDir(), "external-link")
	if err := os.Link(f.Path(), link); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatal("hardlinked private file accepted", err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrPermission) {
		t.Fatal("Close adopted hardlinked private file", err)
	}
	if data, err := os.ReadFile(link); err != nil || string(data) != "private text" {
		t.Fatal("failed Close changed linked data", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
