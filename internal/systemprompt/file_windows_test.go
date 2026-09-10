//go:build windows

package systemprompt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsTestDescriptor(t *testing.T, path string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

func windowsTestSetDACL(t *testing.T, path, sddl string, protected bool) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if protected {
		information = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func windowsTestOwner(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func windowsTestAssertPrivate(t *testing.T, object *os.File) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(windows.Handle(object.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || owner.String() != windowsTestOwner(t) {
		t.Fatalf("creation owner=%v error=%v", owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("unprotected creation descriptor: %s (%v)", sd.String(), err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		t.Fatalf("missing creation DACL: %v", err)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 ||
			(sid != owner.String() && sid != "S-1-5-18") {
			t.Fatalf("creation inherited or exposed access: %s", sd.String())
		}
	}
}

func TestWindowsAppendPrivateCreationUnderSharedTemp(t *testing.T) {
	shared := t.TempDir()
	windowsTestSetDACL(t, shared, "D:P(A;OICI;FA;;;WD)", true)
	t.Setenv("TMP", shared)
	t.Setenv("TEMP", shared)
	// Prove the parent actually supplies a world-accessible inheritable ACE.
	control := filepath.Join(shared, "inherited-control")
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatal(err)
	}
	if sd := windowsTestDescriptor(t, control); !strings.Contains(sd.String(), ";;;WD)") {
		t.Fatalf("shared-parent fixture did not inherit Everyone: %s", sd.String())
	}
	if err := os.Remove(control); err != nil {
		t.Fatal(err)
	}

	// Inspect the directory immediately after its creation API returns, before
	// Materialize can apply chmod or create/write the prompt file.
	directory, err := makePrivateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { directory.Close(); os.Remove(directory.Name()) }()
	windowsTestAssertPrivate(t, directory)
	if err := os.Rename(directory.Name(), directory.Name()+"-taken"); err == nil {
		t.Fatal("creation handle allowed directory replacement")
	}
	if err := os.Remove(directory.Name()); err == nil {
		t.Fatal("creation handle allowed deletion from shared parent")
	}
	pending, err := makePrivatePending(directory, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { pending.Close(); os.Remove(pending.Name()) }()
	windowsTestAssertPrivate(t, pending)
	if info, err := pending.Stat(); err != nil || info.Size() != 0 {
		t.Fatalf("permission checks must precede prompt bytes: %v, %v", info, err)
	}

	f, err := Materialize(context.Background(), "private append bytes\n甲")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(f.Path()); err != nil || string(got) != "private append bytes\n甲" {
		t.Fatalf("private publication=%q, %v", got, err)
	}
}

func TestWindowsAppendDACLChangesRejected(t *testing.T) {
	for _, object := range []string{"directory", "file"} {
		for _, change := range []string{"world-read", "unprotected", "null-dacl"} {
			t.Run(object+"/"+change, func(t *testing.T) {
				f, err := Materialize(context.Background(), "private text")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				path := f.Path()
				if object == "directory" {
					path = filepath.Dir(path)
				}
				original := windowsTestDescriptor(t, path).String()
				defer windowsTestSetDACL(t, path, original, true)
				switch change {
				case "world-read":
					windowsTestSetDACL(t, path, "D:P(A;;FA;;;"+windowsTestOwner(t)+")(A;;FR;;;WD)", true)
				case "unprotected":
					windowsTestSetDACL(t, path, original, false)
				case "null-dacl":
					if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.Verify(context.Background()); !errors.Is(err, os.ErrPermission) {
					t.Fatalf("%s DACL change accepted: %v", object, err)
				}
			})
		}
	}
}

func TestWindowsAppendLifetimePinAndCleanupRetry(t *testing.T) {
	shared := t.TempDir()
	windowsTestSetDACL(t, shared, "D:P(A;OICI;FA;;;WD)", true)
	t.Setenv("TMP", shared)
	t.Setenv("TEMP", shared)
	f, err := Materialize(context.Background(), "private provider input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dir := filepath.Dir(f.Path())
	if err := os.Rename(dir, dir+"-taken"); err == nil {
		t.Fatal("Materialize released the directory pin before provider exit")
	}
	if err := f.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unowned")
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err == nil {
		t.Fatal("Close removed an unknown sibling")
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "keep" {
		t.Fatalf("failed Close changed unowned file: %q, %v", data, err)
	}
	if err := os.Rename(dir, dir+"-taken"); err == nil {
		t.Fatal("failed Close released the directory pin")
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := f.Close(); err != nil {
			t.Fatal("close retry failed", err)
		}
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned directory survived handle deletion: %v", err)
	}
}

func TestWindowsAppendOwnerAndTrustedAdministrators(t *testing.T) {
	f, err := Materialize(context.Background(), "private text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	owner := windowsTestOwner(t)
	for _, path := range []string{filepath.Dir(f.Path()), f.Path()} {
		// Positive control: Windows system accounts may retain full access, but
		// they do not replace the current user as the object's owner.
		sddl := "D:P(A;;FA;;;" + owner + ")"
		for _, sid := range []string{"S-1-5-18", "S-1-5-32-544"} {
			if sid != owner {
				sddl += "(A;;FA;;;" + sid + ")"
			}
		}
		windowsTestSetDACL(t, path, sddl, true)
	}
	if err := f.Verify(context.Background()); err != nil {
		t.Fatal("private current-owner/SYSTEM/Administrators descriptor rejected", err)
	}
	// Validate owner rejection even on hosts that cannot assign another owner.
	foreign, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;" + owner + ")")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsDescriptor(foreign, false); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("foreign descriptor owner accepted: %v", err)
	}
	for _, object := range []string{"directory", "file"} {
		t.Run("owner-change/"+object, func(t *testing.T) {
			path := f.Path()
			if object == "directory" {
				path = filepath.Dir(path)
			}
			admin, err := windows.StringToSid("S-1-5-32-544")
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, admin, nil, nil, nil); err != nil {
				if errors.Is(err, windows.ERROR_INVALID_OWNER) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Skipf("host cannot assign Administrators ownership: %v", err)
				}
				t.Fatal(err)
			}
			defer func() {
				original, err := windows.StringToSid(owner)
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, original, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}()
			if err := f.Verify(context.Background()); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("changed native owner accepted: %v", err)
			}
		})
	}
}

func TestWindowsAppendHardLinkRejected(t *testing.T) {
	f, err := Materialize(context.Background(), "private text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	link := filepath.Join(filepath.Dir(f.Path()), "second-link")
	if err := os.Link(f.Path(), link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	if err := f.Verify(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("multiply-linked append file accepted: %v", err)
	}
}
