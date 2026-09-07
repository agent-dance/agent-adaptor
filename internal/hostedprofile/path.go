package hostedprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// CanonicalSource reads an existing explicitly selected source without creating it.
func CanonicalSource(path string) (string, string, error) {
	path, err := engine.NormalizeProfileDir(path)
	if err != nil {
		return "", "", err
	}
	if path == "" {
		return "", "", unsafe("source directory required", nil)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	return canonicalDirectory(filepath.Clean(path))
}
func safeRelative(path string) bool {
	return path != "" && path != "." && filepath.IsLocal(path) && filepath.ToSlash(filepath.Clean(path)) == path && !strings.Contains(path, "\\")
}
func openChecked(root *os.Root, name string, dir bool) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || before.IsDir() != dir {
		return nil, unsafe("unexpected path type", nil)
	}
	flags := os.O_RDONLY
	if !dir {
		flags = os.O_RDWR
	}
	f, err := root.OpenFile(name, flags|noFollowFlags(), 0)
	if err != nil {
		return nil, err
	}
	if err := validateObject(f, dir); err != nil {
		f.Close()
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, unsafe("path identity changed", err)
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(current, after) {
		f.Close()
		return nil, unsafe("path identity changed", err)
	}
	return f, nil
}
func checkedRoot(parent *os.Root, name string) (*os.Root, error) {
	f, err := openChecked(parent, name, true)
	if err != nil {
		return nil, unsafe("owned directory unavailable", err)
	}
	defer f.Close()
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	i, err := r.Stat(".")
	expected, _ := f.Stat()
	if err != nil || !os.SameFile(i, expected) {
		r.Close()
		return nil, unsafe("directory identity changed", err)
	}
	return r, nil
}
func makePrivateDir(root *os.Root, name string) error {
	if err := root.Mkdir(name, 0700); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_RDONLY|noFollowFlags(), 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return secureObject(f, true)
}
func writeNew(root *os.Root, name string, raw []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollowFlags(), 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := secureObject(f, false); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}
func readRecord(root *os.Root, name string, out any) error {
	f, err := openChecked(root, name, false)
	if err != nil {
		return unsafe("control record unavailable", err)
	}
	defer f.Close()
	limit := markerLimit
	if name == "seed.json" {
		limit = 64 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return unsafe("oversized control record", nil)
	}
	return DecodeJSON(raw, out, true)
}
func writeRecord(root *os.Root, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	limit := markerLimit
	if name == "seed.json" {
		limit = 64 << 20
	}
	if len(raw) > limit {
		return unsafe("oversized control record", nil)
	}
	if f, err := openChecked(root, name, false); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	g, err := generation()
	if err != nil {
		return err
	}
	temp := "." + name + "-" + g
	if err := writeNew(root, temp, raw); err != nil {
		return err
	}
	defer root.Remove(temp)
	if f, err := openChecked(root, name, false); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := renameWithin(root, temp, name); err != nil {
		return err
	}
	f, err := openChecked(root, name, false)
	if err != nil {
		return err
	}
	f.Close()
	return syncRoot(root)
}
func syncRoot(r *os.Root) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return syncDirectory(f)
}

// publishDir never adopts an existing directory without its complete markers.
func publishDir(parent *os.Root, name string, initialize func(*os.Root) error) error {
	if _, err := parent.Lstat(name); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	g, err := generation()
	if err != nil {
		return err
	}
	stage := "." + name + "-" + g
	if err := makePrivateDir(parent, stage); err != nil {
		return err
	}
	r, err := checkedRoot(parent, stage)
	if err != nil {
		return err
	}
	defer r.Close()
	// This exact directory was exclusively created and remains held by r.
	defer func() {
		i, e := parent.Lstat(stage)
		j, _ := r.Stat(".")
		if e == nil && os.SameFile(i, j) {
			_ = parent.RemoveAll(stage)
		}
	}()
	if err := initialize(r); err != nil {
		return err
	}
	if err := syncRoot(r); err != nil {
		return err
	}
	if err := publishWithin(parent, stage, name); err != nil {
		if _, exists := parent.Lstat(name); exists != nil {
			return fmt.Errorf("publish hosted profile: %w", err)
		}
	}
	return syncRoot(parent)
}

// ReadResourceFile bounds actual bytes read from a regular in-root resource.
func ReadResourceFile(r *os.Root, path string, limit int64) ([]byte, error) {
	info, err := r.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, unsafe("non-regular resource", nil)
	}
	if limit < 0 || info.Size() > limit {
		return nil, unsafe("resource byte limit exceeded", nil)
	}
	f, err := r.OpenFile(path, os.O_RDONLY|noFollowFlags(), 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, unsafe("resource identity changed", err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, unsafe("resource byte limit exceeded", nil)
	}
	return raw, nil
}
