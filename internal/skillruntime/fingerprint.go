package skillruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
)

// ProfileSkillsFingerprint hashes the installed tree, including linked skill
// contents. It is read-only and independent of timestamps or enumeration order.
// Providers use it in checkpoint and persistent-process compatibility guards so
// a SyncProfile or external edit cannot silently reuse stale loaded skills.
func ProfileSkillsFingerprint(ctx context.Context, profileDir string) (string, error) {
	root, err := filepath.Abs(filepath.Join(profileDir, "skills"))
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	ancestors := map[string]bool{}
	count := 0
	var walk func(string, string) error
	walk = func(path, relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return fmt.Errorf("profile skills tree exceeds 100000 entries")
		}
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			values[relative] = "missing"
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			real, err := filepath.EvalSymlinks(path)
			// Windows can report an orphaned directory junction as a directory
			// through Stat even though resolving its target fails.
			if os.IsNotExist(err) {
				values[relative] = "missing"
				return nil
			}
			if err != nil {
				return fmt.Errorf("resolve profile skills directory %s: %w", path, err)
			}
			key := normalizePath(real)
			if ancestors[key] {
				return fmt.Errorf("profile skills directory contains a symlink cycle: %s", path)
			}
			ancestors[key] = true
			defer delete(ancestors, key)
			values[relative] = "directory"
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := walk(filepath.Join(path, entry.Name()), filepath.ToSlash(filepath.Join(relative, entry.Name()))); err != nil {
					return err
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("profile skills contain a non-regular file: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, fingerprintReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		values[relative] = fmt.Sprintf("%o:%s", info.Mode().Perm(), hex.EncodeToString(hash.Sum(nil)))
		return ctx.Err()
	}
	if err := walk(root, "."); err != nil {
		return "", err
	}
	return driver.CanonicalSessionConfigFingerprint("profile/skills/v1", values)
}

type fingerprintReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r fingerprintReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
