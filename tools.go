package adaptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
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

func (a *Agent) validateHostedToolsPreflight(eff *RunSettings, caps driver.MCPCapability) error {
	if a == nil || a.toolProvider == nil || eff == nil {
		return nil
	}
	server := driver.MCPServerSpec{
		Key:               toolruntime.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               "http://127.0.0.1:1/mcp",
		BearerTokenEnvVar: toolidentity.BearerTokenEnvVar,
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	_, err := engine.ResolveMCPPayloadWithRuntime(
		eff.engineMCPConfig(),
		nil,
		[]driver.RuntimeServiceRef{{ID: toolruntime.ServerKey, Name: toolruntime.ServerKey, MCP: &server}},
		caps,
	)
	return err
}

type hostedToolProfileClaim struct {
	driverType string
	dir        string
}

type hostedToolProfileSelection struct {
	execution     *driver.ProfileSelection
	compatibility hostedToolProfileCompatibilityView
	ownedDir      string
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
	a.toolProfileMu.Lock()
	defer a.toolProfileMu.Unlock()
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
	a.toolProfileMu.Lock()
	defer a.toolProfileMu.Unlock()
	profile, err := reporter.GetProfile(ctx, nil, identity, engine.CloneProfileSelection(selection))
	if err != nil {
		return fmt.Errorf("resolve hosted Tool profile: %w", err)
	}
	if !profile.Supported || strings.TrimSpace(profile.Dir) == "" {
		return fmt.Errorf("driver %q did not report a usable profile for host-defined Tools", driverType)
	}
	absDir, err := filepath.Abs(profile.Dir)
	if err != nil {
		return fmt.Errorf("resolve hosted Tool profile path: %w", err)
	}
	absDir = filepath.Clean(absDir)
	key := engine.StableHash("adaptor/hosted-tool-profile/v1", driverType, absDir)
	if _, exists := a.toolProfiles[key]; exists {
		return nil
	}
	materializedFingerprint, err := hostedToolMaterializedProfileFingerprint(driverType, absDir)
	if err != nil {
		return fmt.Errorf("fingerprint isolated hosted Tool profile: %w", err)
	}
	for selectionKey, selected := range a.toolProfileSelections {
		if selected.execution == nil || filepath.Clean(selected.execution.Dir) != absDir {
			continue
		}
		selected.compatibility.MaterializedFingerprint = materializedFingerprint
		a.toolProfileSelections[selectionKey] = selected
	}
	if a.toolProfiles == nil {
		a.toolProfiles = make(map[string]hostedToolProfileClaim)
	}
	a.toolProfiles[key] = hostedToolProfileClaim{driverType: driverType, dir: absDir}
	return nil
}

func (a *Agent) hostedToolProfileCompatibility(profile *driver.ProfileSelection) any {
	if a == nil || profile == nil || strings.TrimSpace(profile.Dir) == "" {
		return profile
	}
	wanted := filepath.Clean(profile.Dir)
	a.toolProfileMu.Lock()
	defer a.toolProfileMu.Unlock()
	for _, selection := range a.toolProfileSelections {
		if selection.execution != nil && filepath.Clean(selection.execution.Dir) == wanted {
			return selection.compatibility
		}
	}
	return profile
}

// releaseHostedToolProfiles removes the owned MCP projection and then the
// private clone directory for every identity used by this Agent.
func (a *Agent) releaseHostedToolProfiles(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.toolProfileMu.Lock()
	defer a.toolProfileMu.Unlock()
	for key, claim := range a.toolProfiles {
		if err := mcpruntime.RemoveHostedToolProfile(ctx, claim.driverType, claim.dir); err != nil {
			return fmt.Errorf("remove hosted Tool profile entry: %w", err)
		}
		delete(a.toolProfiles, key)
	}
	for key, selection := range a.toolProfileSelections {
		if err := removeHostedToolProfileDir(selection.ownedDir); err != nil {
			return fmt.Errorf("remove isolated hosted Tool profile: %w", err)
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
	roots, ok := map[string][]string{
		"codex":     {"config.json", "config.toml", "instructions.md", "skills"},
		"claude":    {"settings.json", "config.json", ".claude.json", "skills"},
		"cursor":    {"config.json", "settings.json", "mcp.json", "skills"},
		"codebuddy": {"settings.json", ".mcp.json", "mcp.json", "skills"},
	}[driverType]
	if !ok {
		return "", fmt.Errorf("unsupported hosted Tool profile driver %q", driverType)
	}
	sort.Strings(roots)
	entries := make([]hostedToolProfileFingerprintEntry, 0)
	const maxEntries = 20_000
	const maxBytes = int64(64 << 20)
	var totalBytes int64
	for _, name := range roots {
		logicalRoot := filepath.Join(dir, name)
		resolvedRoot, err := filepath.EvalSymlinks(logicalRoot)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		rootInfo, err := os.Stat(resolvedRoot)
		if err != nil {
			return "", err
		}
		if !rootInfo.IsDir() {
			raw, err := os.ReadFile(resolvedRoot)
			if err != nil {
				return "", err
			}
			totalBytes += int64(len(raw))
			if totalBytes > maxBytes {
				return "", fmt.Errorf("materialized profile exceeds %d bytes", maxBytes)
			}
			digest := sha256.Sum256(raw)
			entries = append(entries, hostedToolProfileFingerprintEntry{
				Path: name, Mode: rootInfo.Mode().Perm(), Fingerprint: hex.EncodeToString(digest[:]),
			})
			continue
		}
		err = filepath.WalkDir(resolvedRoot, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported profile entry %q", current)
			}
			raw, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			totalBytes += int64(len(raw))
			if totalBytes > maxBytes {
				return fmt.Errorf("materialized profile exceeds %d bytes", maxBytes)
			}
			rel, err := filepath.Rel(resolvedRoot, current)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(raw)
			entries = append(entries, hostedToolProfileFingerprintEntry{
				Path: filepath.ToSlash(filepath.Join(name, rel)),
				Mode: info.Mode().Perm(), Fingerprint: hex.EncodeToString(digest[:]),
			})
			if len(entries) > maxEntries {
				return fmt.Errorf("materialized profile exceeds %d entries", maxEntries)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return engine.StableHash("adaptor/hosted-tool-materialized-profile/v1", driverType, roots, entries), nil
}

func (*hostedToolProvider) DetachRun(context.Context, string) error { return nil }

// stabilizeHostedToolCompatibility separates the concrete connection used for
// this process from the semantic capability identity used by resumable
// sessions. The numeric loopback port is intentionally ephemeral across host
// restarts: Drivers must still materialize the real req.MCP URL, while Thread
// and provider session guards must see the stable catalog revision instead.
func (a *Agent) stabilizeHostedToolCompatibility(req *driver.Request) string {
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
		// Replacing only the volatile endpoint preserves every other normalized
		// MCP dimension, including external servers composed with WithTools.
		servers[index].URL = "agent-owned://host-defined-tools/" + provider.fingerprint
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
	req.ProfilePayload.Fingerprint = compatibleProfile.Fingerprint
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
}

var (
	_ RunServiceProvider = (*hostedToolProvider)(nil)
	_ ownedToolRuntime   = (*toolruntime.Runtime)(nil)
)
