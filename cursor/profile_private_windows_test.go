//go:build windows

package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCursorWindowsProjectionCreatesProtectedObjectsAndRejectsChangedACL(t *testing.T) {
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := cursorMakePrivateDir(parent, "private"); err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot("private")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := verifyCursorPrivateRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := cursorWritePrivateFile(root, "resource", []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := root.Open("resource")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCursorPrivateObject(f, false); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	// The real selected parent may be broadly accessible. The projection must
	// establish its own protected DACL before writing rather than inheriting it.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(filepath.Join(parent.Name(), "private"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyCursorPrivateRoot(root); err == nil {
		t.Fatal("accepted Everyone-accessible projection directory")
	}
}

func TestCursorWindowsPrivateDescriptorRejectsWrongOwnerAndUnprotectedACL(t *testing.T) {
	user, err := cursorWindowsSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, sddl := range []string{
		"O:" + user.String() + "D:(A;;FA;;;" + user.String() + ")",
		"O:" + user.String() + "D:P(A;;FA;;;WD)",
		"O:S-1-5-32-545D:P(A;;FA;;;" + user.String() + ")",
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCursorWindowsDescriptor(sd, false); err == nil {
			t.Fatal("unsafe owner/DACL accepted")
		}
	}
}
