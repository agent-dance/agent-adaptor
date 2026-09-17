package skillruntime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

const cloneSkillByteLimit = 64 << 20
const cloneSkillEntryLimit = 20000

type cloneSkillNode struct {
	name     string
	info     os.FileInfo
	data     []byte
	children []*cloneSkillNode
	source   string // Only a proved, direct managed link has an external source.
}

type cloneSkillBudget struct {
	bytes   int64
	entries int
}

func cloneSkillUnsafe(message string) error {
	return fmt.Errorf("%w: clone skills: %s", profile.ErrUnsafe, message)
}

// cloneSkillRoot pins a directory and checks its identity without accepting a
// symlink at that entry. Every recursive operation uses the held parent root.
func cloneSkillRoot(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, cloneSkillUnsafe("directory required")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(before, current) {
		root.Close()
		return nil, cloneSkillUnsafe("directory replaced")
	}
	if err := cloneSkillUnchanged(parent, name, before); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func cloneSkillUnchanged(parent *os.Root, name string, before os.FileInfo) error {
	after, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return cloneSkillUnsafe("resource changed during copy")
	}
	return nil
}

func cloneSkillAbsoluteRoot(path string) (*os.Root, *os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	root, err := cloneSkillRoot(parent, filepath.Base(path))
	if err != nil {
		parent.Close()
		return nil, nil, err
	}
	return root, parent, nil
}

// copyProfileSkills admits only links whose exact source is proved by the
// reconciler's manifest. It snapshots regular bytes through held roots before
// writing anything. No link is copied to the destination. Source markers prove
// origin only: existing copied contents are preserved and remain fingerprinted.
func copyProfileSkills(source, target string, names []string) error {
	for _, name := range names {
		if name != "skills" {
			if err := copyNamedEntriesIfMissing(source, target, []string{name}); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(filepath.Join(source, name)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := cloneProfileSkillsTree(source, target); err != nil {
			return err
		}
	}
	return nil
}

func cloneProfileSkillsTree(source, target string) error {
	src, srcParent, err := cloneSkillAbsoluteRoot(source)
	if err != nil {
		return err
	}
	defer src.Close()
	defer srcParent.Close()
	srcInfo, err := src.Stat(".")
	if err != nil {
		return err
	}
	manifest := profilestate.Manifest{}
	raw, err := hostedprofile.ReadResourceFile(src, profilestate.ManifestName, 4<<20)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err = hostedprofile.DecodeJSON(raw, &manifest, true); err != nil {
			return err
		}
	}
	view, err := CompatibilityTargets(source, manifest, nil, ProfileSkillPruneNone)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var ownedObjects []os.FileInfo
	for id, entry := range manifest.Entries {
		if entry.Kind != profileSkillManifestKind {
			continue
		}
		if id != profilestate.EntryID(entry.Kind, entry.Key) || seen[filepath.Clean(entry.Path)] {
			return cloneSkillUnsafe("ambiguous managed manifest")
		}
		seen[filepath.Clean(entry.Path)] = true
		rel, err := filepath.Rel(source, entry.Path)
		if err != nil || !filepath.IsLocal(rel) {
			return cloneSkillUnsafe("invalid managed path")
		}
		info, err := src.Lstat(rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, prior := range ownedObjects {
			if os.SameFile(prior, info) {
				return cloneSkillUnsafe("duplicate physical managed path")
			}
		}
		ownedObjects = append(ownedObjects, info)
	}
	budget := &cloneSkillBudget{}
	tree, err := readCloneSkillNode(src, "skills", view.Targets, "skills", budget, false)
	if err != nil {
		return err
	}
	// A second bounded read detects replacement, byte/mode drift and changed
	// directory membership before any destination resource is created.
	again, err := readCloneSkillNode(src, "skills", view.Targets, "skills", &cloneSkillBudget{}, false)
	if err != nil {
		return err
	}
	if !sameCloneSkillNode(tree, again) {
		return cloneSkillUnsafe("source changed during snapshot")
	}
	if err := cloneSkillUnchanged(srcParent, filepath.Base(source), srcInfo); err != nil {
		return err
	}
	current, err := hostedprofile.ReadResourceFile(src, profilestate.ManifestName, 4<<20)
	if os.IsNotExist(err) && raw == nil {
		err = nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, current) {
		return cloneSkillUnsafe("manifest changed during snapshot")
	}
	dst, dstParent, err := cloneSkillAbsoluteRoot(target)
	if err != nil {
		return err
	}
	defer dst.Close()
	defer dstParent.Close()
	dstInfo, err := dst.Stat(".")
	if err != nil {
		return err
	}
	if err := writeCloneSkillNode(dst, tree); err != nil {
		return err
	}
	// Directory mtime/size changes are ours; identity and mode must still agree.
	now, err := dstParent.Lstat(filepath.Base(target))
	if err != nil {
		return err
	}
	if !os.SameFile(dstInfo, now) || now.Mode() != dstInfo.Mode() {
		return cloneSkillUnsafe("destination replaced")
	}
	return nil
}

func readCloneSkillNode(parent *os.Root, name string, proved map[string]string, rel string, budget *cloneSkillBudget, reserved bool) (*cloneSkillNode, error) {
	budget.entries++
	if budget.entries > cloneSkillEntryLimit {
		return nil, cloneSkillUnsafe("entry limit exceeded")
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	node := &cloneSkillNode{name: name, info: info}
	if reserved && name == sourceMarkerName {
		return nil, cloneSkillUnsafe("source marker conflicts with managed source")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		source, ok := proved[filepath.ToSlash(rel)]
		if !ok {
			return nil, cloneSkillUnsafe("unproved symlink")
		}
		actual, err := parent.Readlink(name)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(actual) {
			actual = filepath.Join(parent.Name(), actual)
		}
		if filepath.Clean(actual) != source {
			return nil, cloneSkillUnsafe("managed source replaced")
		}
		root, sourceParent, err := cloneSkillAbsoluteRoot(source)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		defer sourceParent.Close()
		sourceInfo, err := root.Stat(".")
		if err != nil {
			return nil, err
		}
		node.children, err = readCloneSkillChildren(root, nil, "", budget, true)
		if err != nil {
			return nil, err
		}
		if err = cloneSkillUnchanged(sourceParent, filepath.Base(source), sourceInfo); err != nil {
			return nil, err
		}
		if err = cloneSkillUnchanged(parent, name, info); err != nil {
			return nil, err
		}
		node.info = sourceInfo
		node.source = source
		return node, nil
	}
	if info.IsDir() {
		root, err := cloneSkillRoot(parent, name)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		node.children, err = readCloneSkillChildren(root, proved, rel, budget, reserved)
		if err != nil {
			return nil, err
		}
	} else if info.Mode().IsRegular() {
		node.data, err = hostedprofile.ReadResourceFile(parent, name, cloneSkillByteLimit-budget.bytes)
		if err != nil {
			return nil, err
		}
		budget.bytes += int64(len(node.data))
	} else {
		return nil, cloneSkillUnsafe("special resource")
	}
	if err = cloneSkillUnchanged(parent, name, info); err != nil {
		return nil, err
	}
	return node, nil
}

func readCloneSkillChildren(root *os.Root, proved map[string]string, rel string, budget *cloneSkillBudget, reserved bool) ([]*cloneSkillNode, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(cloneSkillEntryLimit + 1)
	f.Close()
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > cloneSkillEntryLimit {
		return nil, cloneSkillUnsafe("entry limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	nodes := make([]*cloneSkillNode, 0, len(entries))
	for _, entry := range entries {
		node, err := readCloneSkillNode(root, entry.Name(), proved, filepath.Join(rel, entry.Name()), budget, reserved)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func sameCloneSkillNode(a, b *cloneSkillNode) bool {
	if a.name != b.name || a.source != b.source || !os.SameFile(a.info, b.info) || a.info.Mode() != b.info.Mode() || !a.info.ModTime().Equal(b.info.ModTime()) || !bytes.Equal(a.data, b.data) || len(a.children) != len(b.children) {
		return false
	}
	for i := range a.children {
		if !sameCloneSkillNode(a.children[i], b.children[i]) {
			return false
		}
	}
	return true
}

func writeCloneSkillNode(parent *os.Root, node *cloneSkillNode) (returnErr error) {
	existing, err := parent.Lstat(node.name)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return err
	}
	if !fresh && (existing.Mode()&os.ModeSymlink != 0 || existing.IsDir() != node.info.IsDir() || (!existing.IsDir() && !existing.Mode().IsRegular())) {
		return cloneSkillUnsafe("destination conflict")
	}
	if !node.info.IsDir() {
		if !fresh {
			return nil
		} // Clone never refreshes an existing regular resource.
		return writeCloneSkillFile(parent, node.name, node.data, node.info.Mode().Perm())
	}
	if fresh {
		if err := parent.Mkdir(node.name, 0700); err != nil {
			return err
		}
	}
	root, err := cloneSkillRoot(parent, node.name)
	if err != nil {
		return err
	}
	defer root.Close()
	identity, err := root.Stat(".")
	if err != nil {
		return err
	}
	if node.source != "" && !fresh {
		marker, err := hostedprofile.ReadResourceFile(root, sourceMarkerName, 1<<20)
		if err != nil {
			return err
		}
		if string(marker) != node.source {
			return cloneSkillUnsafe("destination source conflict")
		}
		// Validate the entire retained tree, but never overwrite changed contents.
		if _, err := readCloneSkillChildren(root, nil, "", &cloneSkillBudget{}, false); err != nil {
			return err
		}
	} else {
		for _, child := range node.children {
			if err := writeCloneSkillNode(root, child); err != nil {
				return err
			}
		}
	}
	// Prepare the marker before restoring a possibly read-only directory mode,
	// but publish its source only after all copying and identity checks succeed.
	var marker *os.File
	if fresh && node.source != "" {
		marker, err = root.OpenFile(sourceMarkerName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		markerInfo, err := marker.Stat()
		if err != nil {
			marker.Close()
			return err
		}
		defer func() {
			_ = marker.Close()
			if returnErr != nil {
				if current, err := root.Lstat(sourceMarkerName); err == nil && os.SameFile(markerInfo, current) {
					_ = root.Remove(sourceMarkerName)
				}
			}
		}()
		if err := marker.Chmod(0644); err != nil {
			return err
		}
	}
	if fresh {
		if err := root.Chmod(".", node.info.Mode().Perm()); err != nil {
			return err
		}
	}
	current, err := parent.Lstat(node.name)
	if err != nil {
		return err
	}
	if !os.SameFile(identity, current) || !current.IsDir() {
		return cloneSkillUnsafe("destination directory replaced")
	}
	if marker != nil {
		if _, err := marker.Write([]byte(node.source)); err != nil {
			return err
		}
		if err := marker.Sync(); err != nil {
			return err
		}
		return marker.Close()
	}
	return nil
}

func writeCloneSkillFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// validateCopiedSkillSource keeps a source marker from authorizing deletion of
// ordinary copied contents. It proves origin, never the current bytes or modes.
func validateCopiedSkillSource(target, desired string) error {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	root, parent, err := cloneSkillAbsoluteRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	defer parent.Close()
	marker, err := hostedprofile.ReadResourceFile(root, sourceMarkerName, 1<<20)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if normalizePath(strings.TrimSpace(string(marker))) != normalizePath(desired) {
		return fmt.Errorf("%w: copied skill source replacement cannot prove existing contents", profile.ErrUnsafe)
	}
	return cloneSkillUnchanged(parent, filepath.Base(target), info)
}
