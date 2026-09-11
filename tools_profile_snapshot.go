package adaptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/internal/toolruntime"
	"github.com/agent-dance/agent-adaptor/profile"
	toml "github.com/pelletier/go-toml/v2"
)

type hostedToolProfileFingerprintEntry struct {
	Path        string
	Mode        fs.FileMode
	Fingerprint string
}

// hostedToolMaterializedProfileFingerprint covers the provider-visible
// settings, MCP declarations, and skills copied into the isolated profile.
// Authentication files are deliberately excluded: they are linked rather
// than copied, may rotate independently, and must never enter durable hashes.
func hostedToolMaterializedProfileFingerprint(driverType, dir string) (string, error) {
	return hostedToolResolvedProfileFingerprint(driverType, dir, nil)
}

func hostedToolResolvedProfileFingerprint(driverType, dir string, req *driver.Request) (string, error) {
	return hostedToolProfileFingerprint(driverType, dir, req, true)
}

func hostedToolProfileFingerprint(driverType, dir string, req *driver.Request, readResolvedTargets bool) (string, error) {
	roots := hostedprofile.ResourceRoots(driverType)
	if roots == nil {
		return "", fmt.Errorf("unsupported hosted profile driver %q", driverType)
	}
	// Only the provider's explicit configuration files have JSON/TOML object
	// semantics. Skill attachments and other manifest resources are opaque bytes,
	// even when their names have those extensions (including invalid examples).
	configFiles := make(map[string]bool, len(roots))
	for _, path := range roots {
		configFiles[path] = strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".toml")
	}
	mcpPath, mcpRaw, err := mcpruntime.HostedCompatibilityBaseline(driverType, dir)
	if err == nil && req != nil {
		mcpPath, mcpRaw, err = mcpruntime.ResolvedCompatibilityBaseline(driverType, dir, req.MCP)
	}
	if err != nil {
		return "", err
	}
	manifest, err := mcpruntime.ReadHostedManifest(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range manifest.Entries {
		if entry.Path == "" {
			continue
		}
		rel, err := filepath.Rel(dir, entry.Path)
		if err != nil || !filepath.IsLocal(rel) || rel == "." {
			return "", fmt.Errorf("%w: resource path outside hosted profile", profile.ErrUnsafe)
		}
		switch filepath.Base(rel) {
		case "auth.json", ".credentials.json", "credentials.json", "cli-config.json":
			return "", fmt.Errorf("%w: authentication cannot be a resource", profile.ErrUnsafe)
		}
		roots = append(roots, rel)
	}
	var skills *driver.ResolvedSkills
	if req != nil {
		skills = &req.Skills
	}
	// Hosted Claude, CodeBuddy and Cursor profiles use managed pruning.
	// Codex retains healthy unselected skills and skips an empty payload.
	pruneMode := skillruntime.ProfileSkillPruneManaged
	if driverType == "codex" {
		pruneMode = skillruntime.ProfileSkillPruneNone
		if skills != nil && len(skills.Entries) > 0 {
			pruneMode = skillruntime.ProfileSkillPruneBrokenManaged
		}
	}
	view, err := skillruntime.CompatibilityTargets(dir, manifest, skills, pruneMode)
	if err != nil {
		return "", err
	}
	targets := view.Targets
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	entries := make([]hostedToolProfileFingerprintEntry, 0)
	seen := map[string]bool{}
	var totalBytes int64
	for _, name := range roots {
		if _, projected := targets[filepath.ToSlash(name)]; projected || view.Pruned[filepath.ToSlash(name)] {
			continue
		}
		if _, err := root.Lstat(name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		err = fs.WalkDir(root.FS(), filepath.ToSlash(name), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if _, projected := targets[path]; projected || view.Pruned[path] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if seen[path] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			seen[path] = true
			if len(seen) > 20000 {
				return fmt.Errorf("profile resources exceed entry limit")
			}
			if d.IsDir() {
				info, err := root.Lstat(path)
				if err != nil {
					return err
				}
				entries = append(entries, hostedToolProfileFingerprintEntry{Path: path, Mode: info.Mode(), Fingerprint: "directory"})
				return nil
			}
			info, err := root.Lstat(path)
			if err != nil {
				return err
			}
			if path == mcpPath {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: unverified linked profile resource", profile.ErrUnsafe)
			}
			raw, err := hostedprofile.ReadResourceFile(root, path, (64<<20)-totalBytes)
			if err != nil {
				return err
			}
			totalBytes += int64(len(raw))
			if totalBytes > 64<<20 {
				return fmt.Errorf("profile resources exceed byte limit")
			}
			if configFiles[path] {
				raw, err = canonicalHostedProfileConfig(path, raw)
				if err != nil {
					return err
				}
			}
			digest := sha256.Sum256(raw)
			entries = append(entries, hostedToolProfileFingerprintEntry{Path: path, Mode: info.Mode().Perm(), Fingerprint: hex.EncodeToString(digest[:])})
			if len(entries) > 20000 {
				return fmt.Errorf("profile resources exceed entry limit")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	{ // A missing MCP root has its materializer default mode, so cold views agree.
		info, e := root.Lstat(mcpPath)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		mode := hostedToolMCPMode(runtime.GOOS, info)
		digest := sha256.Sum256(mcpRaw)
		entries = append(entries, hostedToolProfileFingerprintEntry{Path: mcpPath, Mode: mode, Fingerprint: hex.EncodeToString(digest[:])})
	}
	if len(targets) > 0 && !seen["skills"] {
		mode := fs.ModeDir | 0755
		if runtime.GOOS == "windows" {
			// MkdirAll(0755) creates a writable directory; Go observes its
			// Windows attributes as 0777, not POSIX permission bits.
			mode = fs.ModeDir | 0777
		}
		entries = append(entries, hostedToolProfileFingerprintEntry{Path: "skills", Mode: mode, Fingerprint: "directory"})
	}
	for rel, source := range targets {
		// Early ownership checks may encounter a valid managed link whose
		// cache is rebuilt by the sole resolver later in this invocation.
		if !readResolvedTargets {
			continue
		}
		sourceRoot, e := os.OpenRoot(source)
		if e != nil {
			return "", e
		}
		e = fs.WalkDir(sourceRoot.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, e := sourceRoot.Lstat(path)
			if e != nil {
				return e
			}
			name := filepath.ToSlash(filepath.Join(rel, path))
			seen[name] = true
			if len(seen) > 20000 {
				return fmt.Errorf("profile resources exceed entry limit")
			}
			if d.IsDir() {
				entries = append(entries, hostedToolProfileFingerprintEntry{Path: name, Mode: info.Mode(), Fingerprint: "directory"})
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: linked skill content", profile.ErrUnsafe)
			}
			raw, e := hostedprofile.ReadResourceFile(sourceRoot, path, (64<<20)-totalBytes)
			if e != nil {
				return e
			}
			totalBytes += int64(len(raw))
			digest := sha256.Sum256(raw)
			entries = append(entries, hostedToolProfileFingerprintEntry{Path: name, Mode: info.Mode().Perm(), Fingerprint: hex.EncodeToString(digest[:])})
			return nil
		})
		closeErr := sourceRoot.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return engine.StableHash("adaptor/hosted-tool-materialized-profile/v2", driverType, entries), nil
}

// Only explicit provider configuration files reach this function. Attachments
// retain their original bytes, regardless of their filename extension.
func canonicalHostedProfileConfig(path string, raw []byte) ([]byte, error) {
	if strings.HasSuffix(path, ".json") {
		return mcpruntime.StrictProfileJSON(raw)
	}
	if strings.HasSuffix(path, ".toml") {
		var value map[string]any
		if err := toml.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		if len(value) == 0 {
			return nil, nil
		}
		return json.Marshal(value)
	}
	return raw, nil
}

// hostedToolMCPMode predicts only the absent file's observable mode. Go's
// Windows Stat reports 0666 for a writable file created with the writer's 0644
// default; existing modes are always authoritative, independent of platform.
func hostedToolMCPMode(goos string, info fs.FileInfo) fs.FileMode {
	if info != nil {
		return info.Mode().Perm()
	}
	if goos == "windows" {
		return 0666
	}
	return 0644
}

// stabilizeHostedToolCompatibility separates the concrete connection used for
// this process from the semantic capability identity used by resumable
// sessions. The numeric loopback port is intentionally ephemeral across host
// restarts: Drivers must still materialize the real req.MCP URL, while Thread
// and provider session guards must see the stable catalog revision instead.
func (a *Agent) stabilizeHostedToolCompatibility(ctx context.Context, req *driver.Request) (string, any, error) {
	var view any = req.Profile
	if a.toolProvider != nil && req.Profile != nil {
		if err := a.toolProfileMu.LockContext(ctx); err != nil {
			return "", nil, err
		}
		var selections []hostedToolProfileSelection
		for _, selection := range a.toolProfileSelections {
			selections = append(selections, selection)
		}
		a.toolProfileMu.Unlock()
		for _, selection := range selections {
			if selection.execution == nil || filepath.Clean(selection.execution.Dir) != filepath.Clean(req.Profile.Dir) {
				continue
			}
			if selection.persistent != nil {
				if err := selection.persistent.Validate(ctx); err != nil {
					return "", nil, err
				}
			}
			lock, err := profilestate.AcquireLock(ctx, req.Profile.Dir, profilestate.LockOptions{})
			if err != nil {
				return "", nil, err
			}
			fingerprint, err := hostedToolResolvedProfileFingerprint(a.driver.Descriptor().Type, req.Profile.Dir, req)
			releaseErr := lock.Release()
			if err != nil {
				return "", nil, err
			}
			if releaseErr != nil {
				return "", nil, releaseErr
			}
			snapshot := selection.compatibility
			snapshot.Requested = engine.CloneProfileSelection(snapshot.Requested)
			snapshot.MaterializedFingerprint = fingerprint
			view = snapshot
			break
		}
	}
	mcpFingerprint := a.normalizeHostedToolMCPCompatibility(req)
	if snapshot, ok := view.(hostedToolProfileCompatibilityView); ok {
		req.ProfilePayload.Fingerprint = engine.StableHash("adaptor/resolved-profile/v1", req.ProfilePayload.Fingerprint, snapshot.MaterializedFingerprint)
		req.ProfilePayload.SessionCompatibilityFingerprint = engine.StableHash("adaptor/resolved-profile-session/v1", req.ProfilePayload.SessionCompatibilityFingerprint, snapshot.MaterializedFingerprint)
	}
	return mcpFingerprint, view, nil
}

func (a *Agent) normalizeHostedToolMCPCompatibility(req *driver.Request) string {
	if a == nil || req == nil || a.toolProvider == nil {
		if req == nil {
			return ""
		}
		return req.MCP.Fingerprint
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil || provider.fingerprint == "" {
		return req.MCP.Fingerprint
	}
	servers := engine.CloneMCPServerSpecs(req.MCP.Servers)
	found := false
	for index := range servers {
		if servers[index].Key != toolruntime.ServerKey {
			continue
		}
		// Replacing the volatile endpoint and per-Agent credential carrier
		// preserves every other normalized MCP dimension, including external
		// servers composed with WithTools.
		servers[index].URL = "agent-owned://host-defined-tools/" + provider.fingerprint
		servers[index].BearerTokenEnvVar = toolidentity.CompatibilityBearerTokenEnvVar
		found = true
	}
	if !found {
		return req.MCP.Fingerprint
	}
	mcpFingerprint := engine.StableHash("mcp", servers)
	profileMCP := req.ProfilePayload.MCP
	profileMCP.Fingerprint = mcpFingerprint
	compatibleProfile := engine.BuildProfilePayload(
		req.ProfilePayload.Skills,
		profileMCP,
		req.ProfilePayload.Agents,
		req.ProfilePayload.Hooks,
		req.ProfilePayload.Instructions,
		req.ProfilePayload.Config,
		req.ProfilePayload.Declared,
	)
	// Keep req.MCP and req.ProfilePayload.MCP untouched: their concrete URL
	// fingerprints drive collision-safe profile materialization. Only the
	// provider's resume guard uses this semantic compatibility fingerprint. The
	// Driver SPI requires every resumed invocation to apply the current Request,
	// so a restarted Agent can safely rebind the new endpoint without treating a
	// transport allocation detail as a new capability.
	req.ProfilePayload.SessionCompatibilityFingerprint = compatibleProfile.Fingerprint
	return mcpFingerprint
}

// normalizeHostedToolServiceCompatibility removes the same ephemeral URL from
// the runtime-service portion of the Thread fingerprint. ReuseKey already
// carries the deterministic catalog fingerprint and is checked here so an
// unrelated service using a similar name cannot be normalized accidentally.
func (a *Agent) normalizeHostedToolServiceCompatibility(view *threadRuntimeCompatibilityView) {
	if a == nil || view == nil || a.toolProvider == nil {
		return
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil || provider.fingerprint == "" {
		return
	}
	for index := range view.Ensured {
		ref := &view.Ensured[index]
		if ref.ID == toolruntime.ServerKey && ref.Name == toolruntime.ServerKey && ref.ReuseKey == provider.fingerprint {
			ref.URL = "agent-owned://host-defined-tools"
		}
	}
	ownedEnv := provider.bearerTokenEnvVar()
	for index, name := range view.SecretEnvNames {
		if name == ownedEnv {
			view.SecretEnvNames[index] = toolidentity.CompatibilityBearerTokenEnvVar
		}
	}
}
