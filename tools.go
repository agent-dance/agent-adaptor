package adaptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/profile"
	toml "github.com/pelletier/go-toml/v2"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/internal/toolruntime"
	"github.com/agent-dance/agent-adaptor/tool"
)

// configureTools validates the final construction-scope Tool declaration and
// prepares its immutable internal runtime projection. New cannot return an
// error, so declaration errors are retained and surfaced by openStream before
// Driver validation, resource acquisition, or provider launch.
func (a *Agent) configureTools() {
	if a == nil || a.defaults.tools == nil || len(*a.defaults.tools) == 0 {
		return
	}
	definitions := append([]tool.Definition(nil), (*a.defaults.tools)...)
	seen := make(map[string]struct{}, len(definitions))
	var missingRevision string
	for index, definition := range definitions {
		if definition == nil {
			a.toolConfigErr = fmt.Errorf("%w: nil definition at index %d", tool.ErrInvalidDefinition, index)
			return
		}
		descriptor, err := definition.Descriptor()
		if err != nil {
			a.toolConfigErr = err
			return
		}
		if _, duplicate := seen[descriptor.Name]; duplicate {
			a.toolConfigErr = fmt.Errorf("%w: duplicate tool name %q", tool.ErrInvalidDefinition, descriptor.Name)
			return
		}
		seen[descriptor.Name] = struct{}{}
		if missingRevision == "" && strings.TrimSpace(descriptor.Revision) == "" {
			missingRevision = descriptor.Name
		}
	}

	runtime, err := toolruntime.New(definitions)
	if err != nil {
		a.toolConfigErr = fmt.Errorf("prepare host-defined Tools: %w", err)
		return
	}
	a.toolRuntime = runtime
	a.toolProvider = &hostedToolProvider{
		runtime:     runtime,
		fingerprint: runtime.Fingerprint(),
	}
	if missingRevision != "" {
		a.toolThreadErr = &engine.SessionIncompatibleError{
			Reason: fmt.Sprintf("host-defined tool %q has no semantic revision", missingRevision),
		}
	}
}

// hostedToolProvider is deliberately private. Public callers install Tools
// only through construction-scope WithTools and cannot smuggle this stable
// Agent capability into one call through WithRunServices.
type hostedToolProvider struct {
	runtime     *toolruntime.Runtime
	fingerprint string
}

func (p *hostedToolProvider) AttachRun(ctx context.Context, _ string) (RunAttachment, error) {
	if p == nil || p.runtime == nil {
		return RunAttachment{}, toolruntime.ErrClosed
	}
	endpoint, err := p.runtime.Start(ctx)
	if err != nil {
		return RunAttachment{}, err
	}
	token, ok := p.runtime.BearerToken()
	if !ok {
		return RunAttachment{}, toolruntime.ErrClosed
	}
	mcpServer := driver.MCPServerSpec{
		Key:               toolruntime.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               endpoint.URL,
		BearerTokenEnvVar: endpoint.BearerTokenEnvVar,
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	return RunAttachment{Services: []ServiceRef{{
		ID:        toolruntime.ServerKey,
		Name:      toolruntime.ServerKey,
		URL:       endpoint.URL,
		Status:    driver.RuntimeServiceRunning,
		Lifecycle: driver.RuntimeLifecycleShared,
		ReuseKey:  p.fingerprint,
		// A live listener is observable, but no MCP initialize/list probe has
		// happened yet. Do not report an unobserved health check as success.
		Health: driver.RuntimeHealthUnknown,
		MCP:    &mcpServer,
		SecretEnv: []driver.EnvBinding{{
			Name:  endpoint.BearerTokenEnvVar,
			Value: token,
		}},
	}}}, nil
}

func (p *hostedToolProvider) bearerTokenEnvVar() string {
	if p == nil || p.runtime == nil {
		return ""
	}
	return p.runtime.BearerTokenEnvVar()
}

func (a *Agent) validateHostedToolsPreflight(eff *RunSettings, caps driver.MCPCapability) error {
	if a == nil || a.toolProvider == nil || eff == nil {
		return nil
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil || provider.bearerTokenEnvVar() == "" {
		return fmt.Errorf("host-defined Tool runtime has no credential carrier")
	}
	server := driver.MCPServerSpec{
		Key:               toolruntime.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               "http://127.0.0.1:1/mcp",
		BearerTokenEnvVar: provider.bearerTokenEnvVar(),
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	payload, err := engine.ResolveMCPPayloadWithRuntime(
		eff.engineMCPConfig(),
		nil,
		[]driver.RuntimeServiceRef{{ID: toolruntime.ServerKey, Name: toolruntime.ServerKey, MCP: &server}},
		caps,
	)
	if err != nil {
		return err
	}
	return a.validateHostedToolMCPAuthIsolation(payload)
}

// validateHostedToolMCPAuthIsolation prevents any other MCP endpoint from
// naming the private environment variable that carries this Agent's hosted
// Tool bearer token. It runs both in preflight and after runtime providers have
// attached, covering explicit WithMCP and typed runtime-service declarations.
func (a *Agent) validateHostedToolMCPAuthIsolation(payload driver.MCPPayload) error {
	if a == nil || a.toolProvider == nil {
		return nil
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil {
		return nil
	}
	ownedEnv := provider.bearerTokenEnvVar()
	for _, server := range payload.Servers {
		if server.BearerTokenEnvVar == ownedEnv && server.Key != toolruntime.ServerKey {
			return fmt.Errorf("%w: MCP server %q aliases the Agent-owned hosted Tool bearer environment variable", engine.ErrInvalidMCPConfig, server.Key)
		}
	}
	return nil
}

type hostedToolProfileClaim struct {
	driverType string
	dir        string
}

type hostedToolProfileSelection struct {
	execution       *driver.ProfileSelection
	compatibility   hostedToolProfileCompatibilityView
	ownedDir        string
	persistent      *hostedprofile.Claim
	projectionClean bool
	runGate         *hostedProfileGate
}

type hostedToolProfileCompatibilityView struct {
	Version                 string
	SourceDir               string
	MaterializedFingerprint string
	Requested               *driver.ProfileSelection
}

// prepareHostedToolProfile derives a private execution clone from the actual
// configured source profile. Each Agent/identity receives a unique directory,
// eliminating provider-profile write races both within and across processes.
func (a *Agent) prepareHostedToolProfile(ctx context.Context, eff *RunSettings) error {
	if eff == nil {
		return nil
	}
	if a == nil {
		eff.effectiveProfile = nil
		return nil
	}
	if a.toolProvider == nil {
		eff.effectiveProfile = engine.CloneProfileSelection(a.defaults.profile)
		return nil
	}
	driverType := a.driver.Descriptor().Type
	if !mcpruntime.SupportsHostedToolProfile(driverType) {
		eff.effectiveProfile = engine.CloneProfileSelection(a.defaults.profile)
		return nil
	}
	reporter, ok := a.driver.(driver.ProfileReporter)
	if !ok {
		return fmt.Errorf("driver %q persists MCP in a native profile but does not implement driver.ProfileReporter", driverType)
	}
	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}
	// ProfileReporter may materialize an explicitly selected clone. Serialize
	// the complete source-resolution and private-clone allocation path so two
	// first runs cannot race its check/copy/link sequence.
	if err := a.toolProfileMu.LockContext(ctx); err != nil {
		return err
	}
	defer a.toolProfileMu.Unlock()
	if a.defaults.profile != nil && a.defaults.profile.Mode == driver.ProfileModeDedicated {
		return a.preparePersistentHostedProfile(ctx, eff, reporter, driverType, identity)
	}
	source, err := reporter.GetProfile(ctx, nil, identity, engine.CloneProfileSelection(a.defaults.profile))
	if err != nil {
		return fmt.Errorf("resolve source profile for host-defined Tools: %w", err)
	}
	if source.Error != "" {
		return fmt.Errorf("resolve source profile for host-defined Tools: %s", source.Error)
	}
	if !source.Supported || strings.TrimSpace(source.Dir) == "" {
		return fmt.Errorf("driver %q did not report a usable source profile for host-defined Tools", driverType)
	}
	sourceDir, err := filepath.Abs(source.Dir)
	if err != nil {
		return fmt.Errorf("resolve source profile path for host-defined Tools: %w", err)
	}
	sourceDir = filepath.Clean(sourceDir)
	semanticKey := engine.StableHash(
		"adaptor/hosted-tool-profile-selection/v1",
		driverType,
		identity,
		sourceDir,
		a.defaults.profile,
	)

	if existing, ok := a.toolProfileSelections[semanticKey]; ok {
		eff.effectiveProfile = engine.CloneProfileSelection(existing.execution)
		return nil
	}
	ownedDir, err := os.MkdirTemp("", "agent-adaptor-tool-profile-")
	if err != nil {
		return fmt.Errorf("allocate isolated hosted Tool profile: %w", err)
	}
	if err := os.Chmod(ownedDir, 0o700); err != nil {
		_ = os.RemoveAll(ownedDir)
		return fmt.Errorf("secure isolated hosted Tool profile: %w", err)
	}
	execution := &driver.ProfileSelection{
		Mode: driver.ProfileModeClone,
		Dir:  ownedDir,
		From: sourceDir,
		Clone: &driver.CloneProfileOptions{
			IncludeSettings: true,
			IncludeMCP:      true,
			IncludeSkills:   true,
			AuthMode:        driver.CloneProfileAuthLink,
		},
	}
	selection := hostedToolProfileSelection{
		execution: execution,
		compatibility: hostedToolProfileCompatibilityView{
			Version:   "isolated-clone/v1",
			SourceDir: sourceDir,
			Requested: engine.CloneProfileSelection(a.defaults.profile),
		},
		ownedDir: ownedDir,
	}
	if a.toolProfileSelections == nil {
		a.toolProfileSelections = make(map[string]hostedToolProfileSelection)
	}
	a.toolProfileSelections[semanticKey] = selection
	eff.effectiveProfile = engine.CloneProfileSelection(execution)
	return nil
}

// lockHostedToolRun coordinates one actual execution directory. The Agent map
// gate is released before waiting, so distinct isolated profiles can run together.
func (a *Agent) lockHostedToolRun(ctx context.Context, selection *driver.ProfileSelection) (*hostedProfileGate, error) {
	if a.toolProvider == nil || selection == nil {
		return nil, nil
	}
	if err := a.toolProfileMu.LockContext(ctx); err != nil {
		return nil, err
	}
	var gate *hostedProfileGate
	var keys []string
	for key, current := range a.toolProfileSelections {
		if current.execution == nil || filepath.Clean(current.execution.Dir) != filepath.Clean(selection.Dir) {
			continue
		}
		keys = append(keys, key)
		if current.runGate != nil {
			if gate != nil && gate != current.runGate {
				a.toolProfileMu.Unlock()
				return nil, fmt.Errorf("%w: inconsistent hosted profile coordination", profile.ErrUnsafe)
			}
			gate = current.runGate
		}
	}
	if len(keys) > 0 {
		if gate == nil {
			gate = &hostedProfileGate{}
		}
		for _, key := range keys {
			current := a.toolProfileSelections[key]
			current.runGate = gate
			a.toolProfileSelections[key] = current
		}
	}
	a.toolProfileMu.Unlock()
	if gate == nil {
		if !mcpruntime.SupportsHostedToolProfile(a.driver.Descriptor().Type) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: hosted profile selection not owned", profile.ErrUnsafe)
	}
	if err := gate.LockContext(ctx); err != nil {
		return nil, err
	}
	return gate, nil
}

func (a *Agent) claimHostedToolProfile(ctx context.Context, identity driver.AgentIdentity, selection *driver.ProfileSelection) error {
	if a == nil || a.toolProvider == nil {
		return nil
	}
	driverType := a.driver.Descriptor().Type
	if !mcpruntime.SupportsHostedToolProfile(driverType) {
		return nil
	}
	reporter, ok := a.driver.(driver.ProfileReporter)
	if !ok {
		return fmt.Errorf("driver %q persists MCP in a native profile but does not implement driver.ProfileReporter", driverType)
	}
	// GetProfile materializes clone selections in every built-in Driver. Keep it
	// under the same Agent lock as ownership registration so concurrent first
	// runs cannot both create auth links or copy the same target.
	if err := a.toolProfileMu.LockContext(ctx); err != nil {
		return err
	}
	defer a.toolProfileMu.Unlock()
	var selectedKey string
	var selected hostedToolProfileSelection
	for k, v := range a.toolProfileSelections {
		if v.execution != nil && selection != nil && filepath.Clean(v.execution.Dir) == filepath.Clean(selection.Dir) {
			selectedKey, selected = k, v
			break
		}
	}
	absDir := ""
	if selected.persistent != nil {
		if err := selected.persistent.Validate(ctx); err != nil {
			return err
		}
		absDir = selected.persistent.Dir()
	} else {
		p, err := reporter.GetProfile(ctx, nil, identity, engine.CloneProfileSelection(selection))
		if err != nil {
			return fmt.Errorf("resolve hosted Tool profile: %w", err)
		}
		if !p.Supported || strings.TrimSpace(p.Dir) == "" {
			return fmt.Errorf("driver %q did not report usable hosted profile", driverType)
		}
		absDir, err = filepath.Abs(p.Dir)
		if err != nil {
			return err
		}
		absDir = filepath.Clean(absDir)
	}
	key := engine.StableHash("adaptor/hosted-tool-profile/v1", driverType, absDir)
	if a.toolProfiles == nil {
		a.toolProfiles = make(map[string]hostedToolProfileClaim)
	}
	a.toolProfiles[key] = hostedToolProfileClaim{driverType: driverType, dir: absDir}
	if selected.persistent != nil && !selected.projectionClean {
		if _, _, err := mcpruntime.HostedCompatibilityBaseline(driverType, absDir); err != nil {
			return err
		}
		if err := mcpruntime.RemoveHostedToolProfile(ctx, driverType, absDir); err != nil {
			return err
		}
		selected.projectionClean = true
		a.toolProfileSelections[selectedKey] = selected
	}
	// Early validation proves ownership and safety only; the immutable run view
	// is captured after the sole ResolveSkills/InjectSkills phase.
	if _, err := hostedToolProfileFingerprint(driverType, absDir, nil, false); err != nil {
		return fmt.Errorf("validate isolated hosted Tool profile: %w", err)
	}
	if a.toolProfiles == nil {
		a.toolProfiles = make(map[string]hostedToolProfileClaim)
	}
	a.toolProfiles[key] = hostedToolProfileClaim{driverType: driverType, dir: absDir}
	if selected.persistent != nil {
		return selected.persistent.BeginUse(ctx)
	}
	return nil
}

// cleanHostedToolProjections runs only after all admitted runs and provider
// writers have stopped. Claims remain owned until gateway shutdown succeeds.
func (a *Agent) cleanHostedToolProjections(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if err := a.toolProfileMu.LockContext(ctx); err != nil {
		return err
	}
	defer a.toolProfileMu.Unlock()
	for key, c := range a.toolProfiles {
		for _, s := range a.toolProfileSelections {
			if s.persistent != nil && s.persistent.Dir() == c.dir {
				if err := s.persistent.Validate(ctx); err != nil {
					return err
				}
			}
		}
		if err := mcpruntime.RemoveHostedToolProfile(ctx, c.driverType, c.dir); err != nil {
			return fmt.Errorf("remove hosted Tool projection: %w", err)
		}
		delete(a.toolProfiles, key)
	}
	return nil
}

// releaseHostedToolProfiles is the final Close phase, after gateway shutdown.
func (a *Agent) releaseHostedToolProfiles(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if err := a.toolProfileMu.LockContext(ctx); err != nil {
		return err
	}
	defer a.toolProfileMu.Unlock()
	for key, s := range a.toolProfileSelections {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.persistent != nil {
			if err := s.persistent.ReleaseClean(ctx); err != nil {
				return fmt.Errorf("release hosted profile ownership: %w", err)
			}
		} else if err := removeHostedToolProfileDir(s.ownedDir); err != nil {
			return err
		}
		delete(a.toolProfileSelections, key)
	}
	return nil
}

func removeHostedToolProfileDir(dir string) error {
	root, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, target = filepath.Clean(root), filepath.Clean(target)
	if target == root || filepath.Dir(target) != root || !strings.HasPrefix(filepath.Base(target), "agent-adaptor-tool-profile-") {
		return fmt.Errorf("refusing to remove non-owned profile path %q", target)
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove non-directory hosted Tool profile %q", target)
	}
	return os.RemoveAll(target)
}

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
			if configFiles[path] && strings.HasSuffix(path, ".json") {
				raw, err = mcpruntime.StrictProfileJSON(raw)
			} else if configFiles[path] && strings.HasSuffix(path, ".toml") {
				var v map[string]any
				err = toml.Unmarshal(raw, &v)
				if err == nil {
					if len(v) == 0 {
						raw = nil
					} else {
						raw, err = json.Marshal(v)
					}
				}
			}
			if err != nil {
				return err
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
		entries = append(entries, hostedToolProfileFingerprintEntry{Path: "skills", Mode: fs.ModeDir | 0755, Fingerprint: "directory"})
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

func (*hostedToolProvider) DetachRun(context.Context, string) error { return nil }

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

var (
	_ RunServiceProvider = (*hostedToolProvider)(nil)
	_ ownedToolRuntime   = (*toolruntime.Runtime)(nil)
)

// hostedProfileGate serializes first materialization with cancellation while
// preserving short map access through the familiar Lock/Unlock methods.
type hostedProfileGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *hostedProfileGate) LockContext(ctx context.Context) error {
	g.once.Do(func() { g.token = make(chan struct{}, 1); g.token <- struct{}{} })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.token <- struct{}{}
			return err
		}
		return nil
	}
}
func (g *hostedProfileGate) Lock()   { _ = g.LockContext(context.Background()) }
func (g *hostedProfileGate) Unlock() { g.token <- struct{}{} }
func (a *Agent) preparePersistentHostedProfile(ctx context.Context, eff *RunSettings, reporter driver.ProfileReporter, driverType string, identity driver.AgentIdentity) error {
	source, _, err := hostedprofile.CanonicalSource(a.defaults.profile.Dir)
	if err != nil {
		return err
	}
	key := engine.StableHash("hosted-profile-selection/v1", driverType, identity, source)
	selected, ok := a.toolProfileSelections[key]
	if !ok {
		claim, err := hostedprofile.Acquire(ctx, hostedprofile.Spec{DriverType: driverType, SourceDir: source, Identity: identity})
		if err != nil {
			return err
		}
		selected = hostedToolProfileSelection{persistent: claim, execution: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: claim.Dir()}, compatibility: hostedToolProfileCompatibilityView{Version: "persistent-clone/v1", SourceDir: source, Requested: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: source}}}
		if a.toolProfileSelections == nil {
			a.toolProfileSelections = make(map[string]hostedToolProfileSelection)
		}
		a.toolProfileSelections[key] = selected // Publish ownership before any fallible initialization.
	}
	if err := selected.persistent.Validate(ctx); err != nil {
		return err
	}
	if err := selected.persistent.Initialize(ctx, func(ctx context.Context, dst string) error {
		p, err := reporter.GetProfile(ctx, nil, identity, &driver.ProfileSelection{Mode: driver.ProfileModeClone, Dir: dst, From: source, Clone: &driver.CloneProfileOptions{IncludeSettings: true, IncludeMCP: true, IncludeSkills: true, AuthMode: driver.CloneProfileAuthLink}})
		if err != nil {
			return err
		}
		if !p.Supported || filepath.Clean(p.Dir) != dst || p.Error != "" {
			return fmt.Errorf("%w: driver returned an unexpected seed directory", profile.ErrUnsafe)
		}
		return nil
	}); err != nil {
		return err
	}
	eff.effectiveProfile = engine.CloneProfileSelection(selected.execution)
	return nil
}
