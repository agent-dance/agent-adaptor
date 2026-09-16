//go:build windows

package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
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

func TestCursorWindowsOfficialEnvironmentNamesAreCaseInsensitive(t *testing.T) {
	cursorPathTestEnvironment(t)
	config, data, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	env := []driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: t.TempDir()}, {Name: "cursor_config_dir", Value: config}, {Name: "cursor_data_dir", Value: data}, {Name: "xdg_config_home", Value: xdg}}
	b, err := effectiveCursorBindingsNoInitialize(driver.CommonConfig{Env: env}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorConfigDir(b) != config || resolveCursorDataDir(b) != data {
		t.Fatal("Windows explicit env lost last-wins case-insensitive selection")
	}
	env = append(env, driver.EnvBinding{Name: "cursor_config_dir", Value: ""})
	b, err = effectiveCursorBindingsNoInitialize(driver.CommonConfig{Env: env}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorConfigDir(b) != filepath.Join(xdg, "cursor") {
		t.Fatal("Windows lower-case XDG fallback was ignored")
	}
}
