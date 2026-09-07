package skillruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

// CompatibilityView describes proved linked sources and paths the reconciler
// will remove. Callers must exclude Pruned paths from both the physical walk and
// the linked-source walk. The view does not mutate resources or their manifest.
type CompatibilityView struct {
	Targets map[string]string
	Pruned  map[string]bool
}

// CompatibilityTargets proves each linked skill against the exact reconciler
// manifest before permitting a read outside the profile. A non-nil payload
// overlays this run's resolved sources and the specified prune mode; it never
// materializes or resolves skills. Unowned trees remain ordinary resources for the caller.
func CompatibilityTargets(dir string, manifest profilestate.Manifest, payload *driver.ResolvedSkills, pruneMode ProfileSkillPruneMode) (CompatibilityView, error) {
	home := filepath.Join(dir, "skills")
	view := CompatibilityView{Targets: map[string]string{}, Pruned: map[string]bool{}}
	targets := view.Targets
	for _, entry := range manifest.KindEntries(profileSkillManifestKind) {
		rel, err := filepath.Rel(home, entry.Path)
		if err != nil || !filepath.IsLocal(rel) || rel == "." || filepath.Base(rel) != rel {
			return CompatibilityView{}, fmt.Errorf("%w: invalid managed skill path", profile.ErrUnsafe)
		}
		info, err := os.Lstat(entry.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return CompatibilityView{}, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		} // copied trees are hashed as actual files, including any drift
		source, err := os.Readlink(entry.Path)
		if err != nil {
			return CompatibilityView{}, err
		}
		if !filepath.IsAbs(source) {
			source = filepath.Join(filepath.Dir(entry.Path), source)
		}
		source = filepath.Clean(source)
		proof := driver.ResolvedSkill{Key: entry.Key, RuntimeName: rel, SourcePath: source}
		if entry.Metadata[manifestSourceHashKey] != hashedSourcePath(source) || entry.Fingerprint != profileSkillFingerprint(proof) {
			return CompatibilityView{}, fmt.Errorf("%w: managed skill ownership mismatch", profile.ErrUnsafe)
		}
		targets[filepath.ToSlash(filepath.Join("skills", rel))] = source
	}
	if payload == nil {
		return view, nil
	}
	desired, err := desiredProfileSkillEntries(*payload, nil)
	if err != nil {
		return CompatibilityView{}, err
	}
	for _, entry := range desired {
		if entry.SourcePath == "" {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("skills", entry.RuntimeName))
		target := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(target)
		if err != nil && !os.IsNotExist(err) {
			return CompatibilityView{}, err
		}
		if err == nil {
			if _, owned := targets[rel]; owned {
				// The current target was proved above. A Driver which defers
				// reconciliation to Run may now replace it with this resolved
				// source; the immutable view must describe that replacement.
			} else if info.Mode()&os.ModeSymlink != 0 {
				return CompatibilityView{}, fmt.Errorf("%w: unowned linked skill", profile.ErrUnsafe)
			} else {
				continue
			}
		}
		targets[rel] = entry.SourcePath
	}
	installed, err := ReadInstalledSkillTargets(home)
	if err != nil {
		return CompatibilityView{}, err
	}
	for _, entry := range manifest.KindEntries(profileSkillManifestKind) {
		mode := pruneMode
		if next, keep := desired[entry.Key]; keep {
			if next.SourcePath == "" || filepath.Clean(entry.Path) == filepath.Join(home, next.RuntimeName) {
				continue
			}
			// ReconcileProfileSkills always prunes the old name after a rename.
			mode = ProfileSkillPruneManaged
		}
		// ReadInstalledSkillTargets tolerates per-entry inspection errors for
		// discovery. A compatibility proof must distinguish those from absence.
		info, statErr := os.Lstat(entry.Path)
		if statErr != nil && !os.IsNotExist(statErr) {
			return CompatibilityView{}, statErr
		}
		name := runtimeNameFromPath(entry.Path)
		if _, exists := installed[name]; statErr == nil && !exists {
			return CompatibilityView{}, fmt.Errorf("%w: installed skill could not be inspected", profile.ErrUnsafe)
		}
		var remove bool
		switch mode {
		case ProfileSkillPruneNone:
			continue
		case ProfileSkillPruneManaged:
			remove, err = managedProfileSkillPathCanBePruned(home, entry, installed, nil)
		case ProfileSkillPruneBrokenManaged:
			remove, _, err = brokenManagedProfileSkillPathCanBePruned(home, entry, installed, nil)
		default:
			return CompatibilityView{}, fmt.Errorf("unsupported profile skill prune mode %q", mode)
		}
		if err != nil {
			return CompatibilityView{}, err
		}
		if !remove {
			continue
		}
		// A copied tree's source marker proves its origin, not its current
		// contents or modes. Do not erase possible user changes in projection.
		if statErr == nil && info.Mode()&os.ModeSymlink == 0 {
			return CompatibilityView{}, fmt.Errorf("%w: pruned skill contents cannot be proved", profile.ErrUnsafe)
		}
		if actual, exists := installed[name]; exists {
			proof := driver.ResolvedSkill{Key: entry.Key, RuntimeName: name, SourcePath: actual.TargetPath}
			if entry.Metadata[manifestSourceHashKey] != hashedSourcePath(actual.TargetPath) || entry.Fingerprint != profileSkillFingerprint(proof) {
				return CompatibilityView{}, fmt.Errorf("%w: pruned skill ownership mismatch", profile.ErrUnsafe)
			}
		}
		for _, next := range desired {
			if next.RuntimeName == name && next.SourcePath != "" {
				return CompatibilityView{}, fmt.Errorf("%w: pruned skill name conflicts with desired skill", profile.ErrUnsafe)
			}
		}
		rel := filepath.ToSlash(filepath.Join("skills", name))
		view.Pruned[rel] = true
		delete(targets, rel)
	}

	return view, nil
}
