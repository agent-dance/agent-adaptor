package skillruntime

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

// CompatibilityTargets proves each linked skill against the exact reconciler
// manifest before permitting a read outside the profile. A non-nil payload
// overlays this run's resolved sources in a pure view; it never materializes or
// resolves skills. Unowned trees remain ordinary resources for the caller.
func CompatibilityTargets(dir string, manifest profilestate.Manifest, payload *driver.ResolvedSkills) (map[string]string, error) {
	home := filepath.Join(dir, "skills")
	targets := map[string]string{}
	for _, entry := range manifest.KindEntries(profileSkillManifestKind) {
		rel, err := filepath.Rel(home, entry.Path)
		if err != nil || !filepath.IsLocal(rel) || rel == "." || filepath.Base(rel) != rel {
			return nil, fmt.Errorf("%w: invalid managed skill path", profile.ErrUnsafe)
		}
		info, err := os.Lstat(entry.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		} // copied trees are hashed as actual files, including any drift
		source, err := os.Readlink(entry.Path)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(source) {
			source = filepath.Join(filepath.Dir(entry.Path), source)
		}
		source = filepath.Clean(source)
		proof := driver.ResolvedSkill{Key: entry.Key, RuntimeName: rel, SourcePath: source}
		if entry.Metadata[manifestSourceHashKey] != hashedSourcePath(source) || entry.Fingerprint != profileSkillFingerprint(proof) {
			return nil, fmt.Errorf("%w: managed skill ownership mismatch", profile.ErrUnsafe)
		}
		targets[filepath.ToSlash(filepath.Join("skills", rel))] = source
	}
	if payload == nil {
		return targets, nil
	}
	desired, err := desiredProfileSkillEntries(*payload, nil)
	if err != nil {
		return nil, err
	}
	for _, entry := range desired {
		if entry.SourcePath == "" {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("skills", entry.RuntimeName))
		target := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(target)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if _, owned := targets[rel]; owned {
				// The current target was proved above. A Driver which defers
				// reconciliation to Run may now replace it with this resolved
				// source; the immutable view must describe that replacement.
			} else if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("%w: unowned linked skill", profile.ErrUnsafe)
			} else {
				continue
			}
		}
		targets[rel] = entry.SourcePath
	}
	return targets, nil
}
