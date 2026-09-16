package cursor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

const cursorProjectionOwner = "agent-adaptor/cursor-run-projection/v1\n"

// Each invocation gets its own projection. Nothing is copied back. Provider
// session/config roots remain in the selected profile, outside this temporary
// HOME. The parent profile coordinator owns persistent resource serialization;
// unique run directories avoid introducing another writer or lock contract.
func prepareCursorProjection(ctx context.Context, profileDir string, isolated bool, skills driver.ResolvedSkills, bindings []driver.EnvBinding) (out []driver.EnvBinding, pluginDir string, cleanup func() error, returnErr error) {
	parent := ""
	if isolated {
		if err := prepareCursorPrivateHome(profileDir); err != nil {
			return nil, "", nil, err
		}
		parent = filepath.Join(profileDir, cursorPrivateHomeName)
	}
	if parent == "" {
		parent = os.TempDir()
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", nil, err
	}
	name := "cursor-run-" + rand.Text()
	if err := cursorMakePrivateDir(parentRoot, name); err != nil {
		parentRoot.Close()
		return nil, "", nil, err
	}
	home := filepath.Join(parent, name)
	root, err := parentRoot.OpenRoot(filepath.Base(home))
	if err != nil {
		parentRoot.Close()
		_ = os.Remove(home)
		return nil, "", nil, err
	}
	identity, err := root.Stat(".")
	if err != nil {
		root.Close()
		parentRoot.Close()
		_ = os.Remove(home)
		return nil, "", nil, err
	}
	var markerIdentity os.FileInfo
	cleanup = func() error {
		defer parentRoot.Close()
		defer root.Close()
		current, err := parentRoot.Lstat(filepath.Base(home))
		if err != nil || !os.SameFile(identity, current) || current.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cursor projection directory identity changed")
		}
		if err := verifyCursorPrivateRoot(root); err != nil {
			return err
		}
		if markerIdentity == nil {
			return fmt.Errorf("cursor projection ownership identity missing")
		}
		if err := verifyCursorOwner(root, cursorProjectionOwner, markerIdentity); err != nil {
			return err
		}
		// parent.OpenRoot retains an anchored child with delete sharing on
		// Windows. Keep it held until rooted removal is complete.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		count := 0
		return removeCursorProjection(cleanupCtx, parentRoot, filepath.Base(home), identity, 0, &count)
	}
	if err := cursorWritePrivateFile(root, cursorPrivateHomeMarker, []byte(cursorProjectionOwner), 0600); err != nil {
		removeErr := root.Remove(cursorPrivateHomeMarker)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		current, pathErr := parentRoot.Lstat(filepath.Base(home))
		if pathErr == nil && os.SameFile(identity, current) {
			pathErr = parentRoot.Remove(filepath.Base(home))
		}
		return nil, "", nil, errors.Join(err, removeErr, pathErr, root.Close(), parentRoot.Close())
	}
	markerIdentity, err = root.Lstat(cursorPrivateHomeMarker)
	if err != nil {
		root.Close()
		parentRoot.Close()
		return nil, "", nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cleanup())
		}
	}()
	source, err := os.OpenRoot(profileDir)
	if err != nil {
		return nil, "", cleanup, err
	}
	defer source.Close()
	budget := &cursorProjectionBudget{ctx: ctx, profileDir: profileDir}
	if isolated {
		for _, name := range []string{"mcp.json", "hooks.json", "skills"} {
			if err := copyCursorProjection(source, name, root, filepath.Join(".cursor", name), skills, budget); err != nil {
				return nil, "", cleanup, err
			}
		}
	}
	if err := copyCursorProjection(source, "agents", root, filepath.Join("agents-plugin", "agents"), driver.ResolvedSkills{}, budget); err != nil {
		return nil, "", cleanup, err
	}
	if _, err := root.Stat("agents-plugin"); err == nil {
		pluginDir = filepath.Join(home, "agents-plugin")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", cleanup, err
	}
	out = append([]driver.EnvBinding(nil), bindings...)
	if isolated {
		for _, name := range []string{"HOME", "USERPROFILE"} {
			out = skillruntime.WithBinding(out, name, home)
		}
		for name, suffix := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
			out = skillruntime.WithBinding(out, name, filepath.Join(home, suffix))
		}
	}
	return out, pluginDir, cleanup, nil
}

type cursorProjectionBudget struct {
	ctx        context.Context
	profileDir string
	files      int
	bytes      int64
	static     map[string]string
}

func copyCursorProjection(source *os.Root, name string, target *os.Root, dest string, skills driver.ResolvedSkills, budget *cursorProjectionBudget) error {
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	if strings.Count(filepath.ToSlash(dest), "/") > 64 {
		return fmt.Errorf("cursor resource projection exceeds depth limit")
	}
	info, err := source.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	budget.files++
	if budget.files > 4096 {
		return fmt.Errorf("cursor resource projection exceeds 4096 entries")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Only a top-level materialized skill with this invocation's resolved
		// source is an authorized external link. Nested/unknown links fail.
		relative, relErr := filepath.Rel(budget.profileDir, filepath.Join(source.Name(), name))
		if relErr != nil {
			return relErr
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 2 || parts[0] != "skills" {
			return fmt.Errorf("cursor projection rejects resource symlink: %s", name)
		}
		link, err := source.Readlink(name)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(source.Name(), filepath.Dir(name), link)
		}
		actual, err := filepath.EvalSymlinks(link)
		if err != nil {
			return err
		}
		allowed := false
		for _, skill := range skills.Entries {
			if skill.RuntimeName == parts[1] && skill.SourcePath != "" {
				expected, e := filepath.EvalSymlinks(skill.SourcePath)
				if e == nil && expected == actual {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return fmt.Errorf("cursor projection rejects unresolved skill link: %s", name)
		}
		external, err := os.OpenRoot(actual)
		if err != nil {
			return err
		}
		defer external.Close()
		return copyCursorProjection(external, ".", target, dest, driver.ResolvedSkills{}, budget)
	}
	if info.IsDir() {
		if budget.static != nil {
			budget.static[filepath.ToSlash(dest)] = fmt.Sprintf("directory:%o", info.Mode().Perm())
		}
		if target != nil {
			if err := cursorPrivateMkdirAll(target, dest); err != nil {
				return err
			}
		}
		child, err := source.OpenRoot(name)
		if err != nil {
			return err
		}
		defer child.Close()
		actual, err := child.Stat(".")
		if err != nil || !os.SameFile(info, actual) {
			return fmt.Errorf("cursor projection source directory changed: %s", name)
		}
		current, err := source.Lstat(name)
		if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
			return fmt.Errorf("cursor projection source directory changed: %s", name)
		}
		return cursorProjectionEntries(budget.ctx, child, func(entry os.DirEntry) error {
			return copyCursorProjection(child, entry.Name(), target, filepath.Join(dest, entry.Name()), skills, budget)
		})
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cursor projection requires regular resources: %s", name)
	}
	if info.Size() > 8<<20 || budget.bytes+info.Size() > 32<<20 {
		return fmt.Errorf("cursor resource projection exceeds byte limit")
	}
	f, err := source.Open(name)
	if err != nil {
		return err
	}
	opened, statErr := f.Stat()
	current, pathErr := source.Lstat(name)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		f.Close()
		return fmt.Errorf("cursor projection source file changed: %s", name)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	budget.bytes += int64(len(data))
	if len(data) > 8<<20 || budget.bytes > 32<<20 {
		return fmt.Errorf("cursor resource projection exceeds byte limit")
	}
	if budget.static != nil {
		budget.static[filepath.ToSlash(dest)] = fmt.Sprintf("file:%o:%x", info.Mode().Perm(), sha256.Sum256(data))
	}
	if target == nil {
		return nil
	}
	if err := cursorPrivateMkdirAll(target, filepath.Dir(dest)); err != nil {
		return err
	}
	tmp := dest + ".agent-adaptor-tmp"
	if err := cursorWritePrivateFile(target, tmp, data, info.Mode().Perm()&0100|0600); err != nil {
		return err
	}
	return target.Rename(tmp, dest)
}

// Remove only through held parent anchors, never following provider-created
// links. Keep the owner marker until all other entries have been removed.
func removeCursorProjection(ctx context.Context, parent *os.Root, name string, expected os.FileInfo, depth int, count *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*count++
	if *count > 16384 || depth > 64 {
		return fmt.Errorf("cursor projection cleanup exceeds entry/depth limit")
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if expected != nil && !os.SameFile(expected, info) {
		return fmt.Errorf("cursor projection cleanup identity changed")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return parent.Remove(name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		child.Close()
		return fmt.Errorf("cursor projection cleanup directory changed")
	}
	defer child.Close()
	markerPresent := false
	if err := cursorProjectionEntries(ctx, child, func(entry os.DirEntry) error {
		if entry.Name() == cursorPrivateHomeMarker {
			markerPresent = true
			return nil
		}
		return removeCursorProjection(ctx, child, entry.Name(), nil, depth+1, count)
	}); err != nil {
		return err
	}
	if markerPresent {
		if err := removeCursorProjection(ctx, child, cursorPrivateHomeMarker, nil, depth+1, count); err != nil {
			return err
		}
	}
	current, err := parent.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
		return fmt.Errorf("cursor projection cleanup directory changed")
	}
	return parent.Remove(name)
}

// A fixed-size directory batch bounds allocation before entry/depth checks;
// context is checked before each filesystem batch and entry.
func cursorProjectionEntries(ctx context.Context, root *os.Root, visit func(os.DirEntry) error) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := f.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

const cursorSessionResourceState = "cursor_resource_state"

// Hash the closed set of user hooks and profile agents that this print path
// loads, including empty directories and permission modes. JSON sorts map
// keys; neither enumeration order nor the per-run projection path enters it.
// MCP is deliberately owned by the resolved invocation fingerprint: its
// gateway URL and credential carrier can rotate across healthy cold resumes.
func cursorResourceState(ctx context.Context, profileDir string) (string, error) {
	budget := &cursorProjectionBudget{ctx: ctx, profileDir: profileDir, static: map[string]string{}}
	source, err := os.OpenRoot(profileDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		defer source.Close()
		for _, name := range []string{"agents", "hooks.json"} {
			if err := copyCursorProjection(source, name, nil, name, driver.ResolvedSkills{}, budget); err != nil {
				return "", err
			}
		}
	}
	data, err := json.Marshal(budget.static)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func verifyCursorPrivateRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return verifyCursorPrivateObject(f, true)
}

func cursorPrivateMkdirAll(root *os.Root, dir string) error {
	if dir == "." {
		return verifyCursorPrivateRoot(root)
	}
	if !filepath.IsLocal(dir) {
		return os.ErrInvalid
	}
	current := ""
	for _, part := range strings.Split(filepath.Clean(dir), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := cursorMakePrivateDir(root, current); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return os.ErrPermission
		}
		child, err := root.OpenRoot(current)
		if err != nil {
			return err
		}
		err = verifyCursorPrivateRoot(child)
		child.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
