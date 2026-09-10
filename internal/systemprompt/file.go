package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// File owns exactly one private directory and one regular file. No global
// cache, provider profile, or other handle's file is adopted or recursively removed.
type File struct {
	mu          sync.Mutex
	directory   *os.File
	parent      *os.Root
	root        *os.Root
	dir         string
	name        string
	text        string
	fingerprint string
	dirInfo     os.FileInfo
	fileInfo    os.FileInfo
	fileRemoved bool
	dirRemoved  bool
	closed      bool
}

// Materialize atomically publishes and verifies a private file and directory
// (0600/0700 on POSIX, protected private ACLs on Windows). The returned
// handle must outlive its actual provider process.
func Materialize(ctx context.Context, text string) (result *File, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, context.Cause(ctx))
	}
	if text == "" {
		return nil, nil
	}
	if err := Validate("", text); err != nil {
		return nil, err
	}
	directory, err := makePrivateDirectory()
	if err != nil {
		return nil, fmt.Errorf("create append directory: %w", err)
	}
	dir := directory.Name()
	f := &File{directory: directory, dir: dir, text: text, fingerprint: Fingerprint(text)}
	f.name = f.fingerprint + ".txt"
	// OpenRoot keeps all later file operations confined to the owned directory.
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, f.Close())
			result = nil
		}
	}()
	f.dirInfo, err = directory.Stat()
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	f.dirInfo, err = directory.Stat()
	if err != nil {
		return nil, err
	}
	if !f.dirInfo.IsDir() || unsafeInfo(f.dirInfo) {
		return nil, fmt.Errorf("unsafe append directory: %w", os.ErrPermission)
	}
	if err := f.verifyDirectory(); err != nil {
		return nil, err
	}
	f.parent, err = os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	f.root, err = f.parent.OpenRoot(filepath.Base(dir))
	if err != nil {
		return nil, err
	}
	temp, err := makePrivatePending(directory, f.root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil && f.root != nil {
			if e := f.root.Remove(".pending"); e != nil && !errors.Is(e, os.ErrNotExist) {
				resultErr = errors.Join(resultErr, e)
			}
		}
	}()
	// Do not write even the first prompt byte if permissions cannot be proven.
	if err := temp.Chmod(0o600); err != nil {
		return nil, errors.Join(err, temp.Close())
	}
	if err := verifyPrivateObject(temp, false); err != nil {
		return nil, errors.Join(err, temp.Close())
	}
	_, writeErr := temp.WriteString(text)
	syncErr := temp.Sync()
	info, statErr := temp.Stat()
	if err := errors.Join(writeErr, syncErr, statErr); err != nil {
		return nil, errors.Join(err, temp.Close())
	}
	f.fileInfo = info
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, context.Cause(ctx), temp.Close())
	}
	if err := publishPrivatePending(temp, f.root, f.name); err != nil {
		return nil, err
	}
	if err := f.Verify(ctx); err != nil {
		return nil, err
	}
	return f, nil
}

// Path returns the immutable owned path, or empty for a nil handle.
func (f *File) Path() string {
	if f == nil {
		return ""
	}
	return filepath.Join(f.dir, f.name)
}

// Fingerprint identifies content, never the randomized path.
func (f *File) Fingerprint() string {
	if f == nil {
		return ""
	}
	return f.fingerprint
}

func (f *File) verifyDirectory() error {
	info, err := privateDirectoryInfo(f.dir)
	if err != nil {
		return err
	}
	if f.dirInfo == nil || unsafeInfo(info) || !info.IsDir() || !os.SameFile(f.dirInfo, info) || info.Mode().Perm() != f.dirInfo.Mode().Perm() {
		return fmt.Errorf("append directory identity or permissions changed: %w", os.ErrPermission)
	}
	return nil
}
func (f *File) verifyIdentity() error {
	info, err := f.root.Lstat(f.name)
	if err != nil {
		return err
	}
	if f.fileInfo == nil || unsafeInfo(info) || !info.Mode().IsRegular() || !os.SameFile(f.fileInfo, info) {
		return fmt.Errorf("append file identity changed: %w", os.ErrPermission)
	}
	return nil
}

// Verify checks directory/file identity, link type, platform permissions and
// owner, full bytes and SHA-256. Changes and replacement files fail closed.
func (f *File) Verify(ctx context.Context) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	if f.closed || f.fileRemoved || f.root == nil {
		return os.ErrClosed
	}
	if err := f.verifyDirectory(); err != nil {
		return err
	}
	if err := f.verifyIdentity(); err != nil {
		return err
	}
	file, err := f.root.Open(f.name)
	if err != nil {
		return err
	}
	if err := verifyPrivateObject(file, false); err != nil {
		return errors.Join(err, file.Close())
	}
	info, statErr := file.Stat()
	// Bound reads by expected bytes + 1 so a corrupted file cannot force an
	// unbounded allocation. The byte comparison detects truncated/grown content.
	data, readErr := io.ReadAll(io.LimitReader(file, int64(len(f.text))+1))
	permissionErr := verifyPrivateObject(file, false)
	closeErr := file.Close()
	if err := errors.Join(statErr, readErr, permissionErr, closeErr); err != nil {
		return err
	}
	if !os.SameFile(f.fileInfo, info) || unsafeInfo(info) || !info.Mode().IsRegular() || info.Mode().Perm() != f.fileInfo.Mode().Perm() || string(data) != f.text || Fingerprint(string(data)) != f.fingerprint {
		return fmt.Errorf("append file integrity check failed: %w", os.ErrPermission)
	}
	if err := f.verifyDirectory(); err != nil {
		return err
	}
	if err := f.verifyIdentity(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	return nil
}

// Close removes only the current handle's proven objects. It is idempotent,
// reports failures, and retries safely without adopting replacements.
func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	if !f.dirRemoved {
		if err := f.verifyDirectory(); err != nil {
			return err
		}
		if !f.fileRemoved && f.root != nil {
			if err := f.verifyIdentity(); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			} else if err := f.root.Remove(f.name); err != nil {
				return err
			}
			f.fileRemoved = true
		}
		if err := removePrivateDirectory(f); err != nil {
			return err
		}
		f.dirRemoved = true
	}
	var errs []error
	if f.root != nil {
		if err := f.root.Close(); err != nil {
			errs = append(errs, err)
		} else {
			f.root = nil
		}
	}
	if f.directory != nil {
		if err := f.directory.Close(); err != nil {
			errs = append(errs, err)
		} else {
			f.directory = nil
		}
	}
	if f.parent != nil {
		if err := f.parent.Close(); err != nil {
			errs = append(errs, err)
		} else {
			f.parent = nil
		}
	}
	if len(errs) == 0 {
		f.closed = true
	}
	return errors.Join(errs...)
}
