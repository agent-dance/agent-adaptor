package adaptor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/profile"
)

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

// hostedProfileGate serializes first materialization with cancellation while
// preserving short map access through the familiar Lock/Unlock methods.
type hostedProfileGate struct {
	once  sync.Once
	token chan struct{}
}

func (g *hostedProfileGate) LockContext(ctx context.Context) error {
	g.once.Do(func() {
		g.token = make(chan struct{}, 1)
		g.token <- struct{}{}
	})
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
		selected = hostedToolProfileSelection{
			persistent: claim,
			execution:  &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: claim.Dir()},
			compatibility: hostedToolProfileCompatibilityView{
				Version:   "persistent-clone/v1",
				SourceDir: source,
				Requested: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: source},
			},
		}
		if a.toolProfileSelections == nil {
			a.toolProfileSelections = make(map[string]hostedToolProfileSelection)
		}
		a.toolProfileSelections[key] = selected // Publish ownership before any fallible initialization.
	}
	if err := selected.persistent.Validate(ctx); err != nil {
		return err
	}
	if err := selected.persistent.Initialize(ctx, func(ctx context.Context, dst string) error {
		p, err := reporter.GetProfile(ctx, nil, identity, &driver.ProfileSelection{
			Mode: driver.ProfileModeClone,
			Dir:  dst,
			From: source,
			Clone: &driver.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
				AuthMode:        driver.CloneProfileAuthLink,
			},
		})
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
