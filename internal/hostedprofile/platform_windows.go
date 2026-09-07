//go:build windows

package hostedprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	stdunsafe "unsafe"

	"github.com/agent-dance/agent-adaptor/profile"
	"golang.org/x/sys/windows"
)

func windowsFileInfo(f *os.File) (windows.ByHandleFileInformation, error) {
	var i windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &i)
	return i, err
}
func finalWindowsPath(f *os.File) (string, error) {
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buf)) {
		return "", profile.ErrUnsafe
	}
	s := windows.UTF16ToString(buf[:n])
	if strings.HasPrefix(s, `\\?\UNC\`) {
		return "", profile.ErrUnsupportedFilesystem
	}
	return strings.TrimPrefix(s, `\\?\`), nil
}
func canonicalDirectory(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	i, err := windowsFileInfo(f)
	if err != nil {
		return "", "", err
	}
	if i.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", "", profile.ErrUnsafe
	}
	path, err = finalWindowsPath(f)
	if err != nil {
		return "", "", err
	}
	if err := windowsFilesystem(f); err != nil {
		return "", "", err
	}
	return path, fmt.Sprintf("%x:%x:%x", i.VolumeSerialNumber, i.FileIndexHigh, i.FileIndexLow), nil
}
func windowsFilesystem(f *os.File) error {
	path, err := finalWindowsPath(f)
	if err != nil {
		return err
	}
	if len(path) < 3 || path[1] != ':' {
		return profile.ErrUnsupportedFilesystem
	}
	root, err := windows.UTF16PtrFromString(path[:3])
	if err != nil {
		return err
	}
	if windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return profile.ErrUnsupportedFilesystem
	}
	var name [128]uint16
	var serial, maxComponent, flags uint32
	if err := windows.GetVolumeInformationByHandle(windows.Handle(f.Fd()), nil, 0, &serial, &maxComponent, &flags, &name[0], uint32(len(name))); err != nil {
		return errors.Join(profile.ErrUnsupportedFilesystem, err)
	}
	fs := windows.UTF16ToString(name[:])
	if (fs != "NTFS" && fs != "ReFS") || flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return profile.ErrUnsupportedFilesystem
	}
	return nil
}
func currentWindowsSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}
func validateObject(f *os.File, dir bool) error {
	i, err := windowsFileInfo(f)
	if err != nil {
		return err
	}
	if i.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (i.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != dir || (!dir && i.NumberOfLinks != 1) {
		return profile.ErrUnsafe
	}
	if err := windowsFilesystem(f); err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.Join(profile.ErrUnsupportedFilesystem, err)
	}
	current, err := currentWindowsSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.Equals(current) {
		return profile.ErrUnsafe
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return profile.ErrUnsafe
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return profile.ErrUnsafe
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	expected := uint16(2)
	if current.Equals(system) {
		expected = 1
	}
	if acl.AceCount != expected {
		return profile.ErrUnsafe
	}
	seenUser, seenSystem := false, false
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != windows.ACCESS_MASK(0x1f01ff) {
			return profile.ErrUnsafe
		}
		sid := (*windows.SID)(stdunsafe.Pointer(&ace.SidStart))
		if sid.Equals(current) {
			seenUser = true
			if sid.Equals(system) {
				seenSystem = true
			}
		} else if sid.Equals(system) {
			seenSystem = true
		} else {
			return profile.ErrUnsafe
		}
	}
	if !seenUser || !seenSystem {
		return profile.ErrUnsafe
	}
	return nil
}
func secureObject(f *os.File, dir bool) error {
	sid, err := currentWindowsSID()
	if err != nil {
		return err
	}
	sddl := "D:P(A;;FA;;;" + sid.String() + ")"
	if sid.String() != "S-1-5-18" {
		sddl += "(A;;FA;;;SY)"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// Reopen with WRITE_DAC on the same object and verify its immutable file ID.
	path, err := finalWindowsPath(f)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	old, err := windowsFileInfo(f)
	if err != nil {
		return err
	}
	var now windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &now); err != nil {
		return err
	}
	if old.VolumeSerialNumber != now.VolumeSerialNumber || old.FileIndexHigh != now.FileIndexHigh || old.FileIndexLow != now.FileIndexLow || now.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return profile.ErrUnsafe
	}
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sid, nil, acl, nil); err != nil {
		return err
	}
	return validateObject(f, dir)
}
func noFollowFlags() int { return 0 }
func lockFile(f *os.File) error {
	var o windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &o)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return profile.ErrInUse
	}
	return err
}
func unlockFile(f *os.File) error {
	var o windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &o)
}

// Windows has no supported directory FlushFileBuffers operation. Control data
// is flushed before a write-through same-volume publication in renameWithin.
func syncDirectory(f *os.File) error { return windowsFilesystem(f) }
func renameWithin(r *os.Root, old, next string) error {
	// Resolve beneath the held Root, then check the root's path still names its
	// held object before the Win32 write-through rename (Root.Rename lacks flags).
	oldInfo, err := r.Lstat(old)
	if err != nil {
		return err
	}
	if oldInfo.Mode()&os.ModeSymlink != 0 {
		return profile.ErrUnsafe
	}
	held, err := r.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Lstat(r.Name())
	if err != nil || !os.SameFile(held, named) {
		return profile.ErrUnsafe
	}
	from, err := windows.UTF16PtrFromString(filepath.Join(r.Name(), old))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(r.Name(), next))
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	published, err := r.Lstat(next)
	if err != nil || !os.SameFile(oldInfo, published) {
		return profile.ErrUnsafe
	}
	return nil
}

func publishWithin(r *os.Root, old, next string) error {
	from, err := windows.UTF16PtrFromString(filepath.Join(r.Name(), old))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(r.Name(), next))
	if err != nil {
		return err
	}
	held, err := r.Stat(".")
	if err != nil {
		return err
	}
	named, err := os.Lstat(r.Name())
	if err != nil || !os.SameFile(held, named) {
		return profile.ErrUnsafe
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

// The ownership handle deliberately omits FILE_SHARE_DELETE. os.Root.OpenFile
// grants delete sharing on Windows, so it cannot hold this permanent lock inode.
func openOwnershipLock(r *os.Root) (*os.File, error) {
	before, err := r.Lstat("owner.lock")
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, profile.ErrUnsafe
	}
	held, err := r.Stat(".")
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(r.Name())
	if err != nil || !os.SameFile(held, named) {
		return nil, profile.ErrUnsafe
	}
	path := filepath.Join(r.Name(), "owner.lock")
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if err := validateObject(f, false); err != nil {
		f.Close()
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, profile.ErrUnsafe
	}
	current, err := r.Lstat("owner.lock")
	if err != nil || !os.SameFile(current, after) {
		f.Close()
		return nil, profile.ErrUnsafe
	}
	return f, nil
}
