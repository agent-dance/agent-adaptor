//go:build windows

package hostedprofile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/profile"
	"golang.org/x/sys/windows"
)

func TestWindowsPrivateDACLAndTamperedDACL(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	readyTest(t, c)
	key := filepath.Dir(c.Dir())
	if err := c.ReleaseUnused(context.Background()); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(key, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if next, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrUnsafe) {
		if next != nil {
			next.ReleaseUnused(context.Background())
		}
		t.Fatalf("world-accessible DACL accepted: %v", err)
	}
}
func TestWindowsReparseProfileRejected(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	readyTest(t, c)
	if err := c.ReleaseUnused(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := c.Dir()
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatalf("reparse profile accepted: %v", err)
	}
}

func TestWindowsOwnershipLockForbidsRenameAndDelete(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	path := filepath.Join(filepath.Dir(c.Dir()), "owner.lock")
	if err := os.Rename(path, path+"-moved"); err == nil {
		t.Fatal("owned lock allowed rename")
	}
	if err := os.Remove(path); err == nil {
		t.Fatal("owned lock allowed delete")
	}
	if err := c.ReleaseUnused(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+"-moved", path); err != nil {
		t.Fatal(err)
	}
}
