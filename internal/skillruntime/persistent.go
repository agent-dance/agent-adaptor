package skillruntime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

// ListProfileSkills observes a provider's skills/ directory without initializing
// it. Manifest ownership is retained even when no skills are selected by the
// inspecting Agent; user-installed entries remain external.
func ListProfileSkills(provider, profileDir string, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill) (driver.SkillSnapshot, error) {
	home := filepath.Join(profileDir, "skills")
	installed, err := ReadInstalledSkillTargets(home)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	out := BuildPersistentSnapshot(PersistentSnapshotOptions{
		DriverType: provider, Payload: payload, Selected: selected, Resolved: resolved,
		Installed: installed, SkillsHome: home, LocationLabel: home,
		InstalledDetail:        "Installed in the native profile skills directory.",
		MissingDetail:          "Selected skill is not installed in the native profile; call SyncProfile to install it.",
		ExternalConflictDetail: "An external installation occupies this skill name.",
		ExternalDetail:         "Installed outside agent-adaptor management.",
	})
	for i := range out.Entries {
		entry := &out.Entries[i]
		if runtimeNameInEntries(entry.RuntimeName, payload.Entries) {
			continue
		}
		for _, owned := range manifest.KindEntries(profileSkillManifestKind) {
			if normalizePath(owned.Path) != normalizePath(filepath.Join(home, entry.RuntimeName)) {
				continue
			}
			actual := installed[entry.RuntimeName]
			if !installedSkillMatchesManifest(actual, owned, nil) {
				continue
			}
			entry.Key, entry.Managed, entry.ReadOnly = owned.Key, true, false
			entry.State, entry.Origin = driver.SkillStateInstalled, driver.SkillOriginManaged
			entry.OriginLabel, entry.Detail = "SDK-managed", "Observed SDK-managed installation in the native profile."
			entry.SourcePath, entry.TargetPath = actual.TargetPath, owned.Path
			break
		}
	}
	for i := range out.Entries {
		entry := &out.Entries[i]
		if entry.State != driver.SkillStateInstalled {
			continue
		}
		info, err := os.Stat(filepath.Join(home, entry.RuntimeName, "SKILL.md"))
		if err != nil && !os.IsNotExist(err) {
			return driver.SkillSnapshot{}, err
		}
		if os.IsNotExist(err) || !info.Mode().IsRegular() {
			entry.State = driver.SkillStateMissing
			entry.Detail = "The installed target has no readable regular SKILL.md file."
		}
	}
	sortSnapshotEntries(out.Entries)
	return out, nil
}

// SyncProfileSkills uses the common ownership/locking reconciler. Shared
// profiles retain other managed skills; isolated profiles prune unselected
// managed entries. Both preserve external installations.
func SyncProfileSkills(ctx context.Context, provider, profileDir string, kind driver.ProfileKind, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill) (driver.SkillSnapshot, error) {
	prune := ProfileSkillPruneBrokenManaged
	if kind == driver.ProfileKindHostManaged {
		prune = ProfileSkillPruneManaged
	}
	if _, err := ReconcileProfileSkills(ctx, ProfileSkillReconcileOptions{
		ProfileDir: profileDir, SkillsHome: filepath.Join(profileDir, "skills"),
		Payload: payload, Selected: selected, ManagedRoots: []string{ManagedSkillCacheRoot()},
		ConflictMode: ProfileSkillConflictError, PruneMode: prune,
	}); err != nil {
		return driver.SkillSnapshot{}, err
	}
	return ListProfileSkills(provider, profileDir, payload, selected, resolved)
}
