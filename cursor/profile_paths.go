package cursor

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// The CLI has three independent roots: cli-config.json, chats/projects, and
// user extensibility under HOME/.cursor. CURSOR_HOME is an SDK selector, not a
// CLI variable. Explicit SDK profiles use a local plugin and an empty private
// HOME so unrelated native user resources cannot enter the selected profile.
const cursorPrivateHomeName = ".agent-adaptor-home"
const cursorPrivateHomeMarker = ".agent-adaptor-owner"
const cursorPrivateHomeOwner = "agent-adaptor/cursor-empty-home/v1\n"

func cursorEnv(bindings []driver.EnvBinding, name string) string {
	for i := len(bindings) - 1; i >= 0; i-- {
		if bindings[i].Name == name || runtime.GOOS == "windows" && strings.EqualFold(bindings[i].Name, name) {
			return bindings[i].Value
		}
	}
	return os.Getenv(name)
}

func cursorUserHome(bindings []driver.EnvBinding) string {
	// An explicit SDK HOME remains a supported selection on every platform;
	// bind both platform spellings before launch. Otherwise follow Node's
	// Windows USERPROFILE home convention.
	for _, name := range []string{"HOME", "USERPROFILE"} {
		if value := skillruntime.ResolveBinding(bindings, name); strings.TrimSpace(value) != "" {
			return filepath.Clean(value)
		}
	}
	if runtime.GOOS == "windows" {
		if value := os.Getenv("USERPROFILE"); strings.TrimSpace(value) != "" {
			return filepath.Clean(value)
		}
	}
	return skillruntime.ResolveHome(bindings)
}

func resolveCursorConfigDir(bindings []driver.EnvBinding) string {
	if value := cursorEnv(bindings, "CURSOR_CONFIG_DIR"); strings.TrimSpace(value) != "" {
		return filepath.Clean(value)
	}
	if value := cursorEnv(bindings, "XDG_CONFIG_HOME"); strings.TrimSpace(value) != "" {
		return filepath.Join(value, "cursor")
	}
	return filepath.Join(cursorUserHome(bindings), ".cursor")
}

func resolveCursorDataDir(bindings []driver.EnvBinding) string {
	if value := cursorEnv(bindings, "CURSOR_DATA_DIR"); strings.TrimSpace(value) != "" {
		return filepath.Clean(value)
	}
	return filepath.Join(cursorUserHome(bindings), ".cursor")
}

func cursorIsolatedProfile(config driver.CommonConfig, selection *driver.ProfileSelection) bool {
	if selection != nil {
		switch selection.Mode {
		case driver.ProfileModeDedicated, driver.ProfileModeClone:
			return true
		case driver.ProfileModeNative:
			return strings.TrimSpace(os.Getenv("CURSOR_HOME")) != ""
		}
	}
	return strings.TrimSpace(skillruntime.ResolveBinding(config.Env, "CURSOR_HOME")) != "" || strings.TrimSpace(os.Getenv("CURSOR_HOME")) != ""
}

func cursorBindings(config driver.CommonConfig, selection *driver.ProfileSelection, skipInitialize bool) ([]driver.EnvBinding, error) {
	profile, err := resolveCursorProfileWithOptions(config, selection, skipInitialize)
	if err != nil {
		return nil, err
	}
	bindings := skillruntime.WithBinding(config.Env, "CURSOR_HOME", profile.Dir)
	for _, name := range []string{"HOME", "USERPROFILE"} {
		bindings = skillruntime.WithBinding(bindings, name, cursorUserHome(config.Env))
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		bindings = skillruntime.WithBinding(bindings, name, cursorEnv(config.Env, name))
	}
	configDir, dataDir := resolveCursorConfigDir(config.Env), resolveCursorDataDir(config.Env)
	if cursorIsolatedProfile(config, selection) {
		configDir, dataDir = profile.Dir, profile.Dir
		home := filepath.Join(profile.Dir, cursorPrivateHomeName)
		for _, name := range []string{"HOME", "USERPROFILE"} {
			bindings = skillruntime.WithBinding(bindings, name, home)
		}
		for name, suffix := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"} {
			bindings = skillruntime.WithBinding(bindings, name, filepath.Join(home, suffix))
		}
	}
	for name, dir := range map[string]string{"CURSOR_CONFIG_DIR": configDir, "CURSOR_DATA_DIR": dataDir} {
		dir, err = engine.NormalizeProfileDir(dir)
		if err != nil {
			return nil, fmt.Errorf("cursor %s: %w", name, err)
		}
		bindings = skillruntime.WithBinding(bindings, name, dir)
	}
	return bindings, nil
}

// This directory contains no imported credentials or native user resources.
// Unix permissions are restricted; Windows creates protected owner/SYSTEM
// DACLs at creation and verifies them before any content write or reuse.
func prepareCursorPrivateHome(profileDir string) (returnErr error) {
	info, err := os.Lstat(profileDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor private HOME requires a real selected profile directory: %s", profileDir)
	}
	parent, err := os.OpenRoot(profileDir)
	if err != nil {
		return err
	}
	defer parent.Close()
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("cursor selected profile identity changed")
	}
	if _, err := parent.Lstat(cursorPrivateHomeName); errors.Is(err, os.ErrNotExist) {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		stage := cursorPrivateHomeName + ".pending-" + hex.EncodeToString(nonce[:])
		if err := cursorMakePrivateDir(parent, stage); err != nil {
			return err
		}
		owned, err := parent.OpenRoot(stage)
		if err != nil {
			return errors.Join(err, parent.Remove(stage))
		}
		defer owned.Close()
		identity, err := owned.Stat(".")
		if err != nil {
			return err
		}
		// Publish the complete directory with no replacement. Parallel first runs
		// can observe only a complete marker, and an empty foreign directory is
		// never replaced. A losing publisher removes only its own two-node stage.
		defer func() {
			current, e := parent.Lstat(stage)
			if errors.Is(e, os.ErrNotExist) {
				return
			} // Published by this call.
			if e != nil || !os.SameFile(identity, current) || current.Mode()&os.ModeSymlink != 0 {
				returnErr = errors.Join(returnErr, fmt.Errorf("cursor private HOME staging identity changed"))
				return
			}
			if e := owned.Remove(cursorPrivateHomeMarker); e != nil && !errors.Is(e, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, e)
			}
			returnErr = errors.Join(returnErr, parent.Remove(stage))
		}()
		if err := cursorWritePrivateFile(owned, cursorPrivateHomeMarker, []byte(cursorPrivateHomeOwner), 0600); err != nil {
			return err
		}
		if err := publishCursorPrivateHome(parent, stage, cursorPrivateHomeName); err != nil {
			if _, exists := parent.Lstat(cursorPrivateHomeName); exists != nil {
				return fmt.Errorf("cursor private HOME publication: %w", err)
			}
		}
	} else if err != nil {
		return err
	}
	info, err = parent.Lstat(cursorPrivateHomeName)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cursor private HOME is not an owned directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		return fmt.Errorf("cursor private HOME permissions must be 0700")
	}
	home, err := parent.OpenRoot(cursorPrivateHomeName)
	if err != nil {
		return err
	}
	defer home.Close()
	current, err := home.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("cursor private HOME identity changed")
	}
	if err := verifyCursorPrivateRoot(home); err != nil {
		return err
	}
	return verifyCursorOwner(home, cursorPrivateHomeOwner, nil)
}

// Marker reads are bounded and tied to both the path and opened file identity.
func verifyCursorOwner(root *os.Root, owner string, expected os.FileInfo) error {
	info, err := root.Lstat(cursorPrivateHomeMarker)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(owner)) || (expected != nil && !os.SameFile(expected, info)) {
		return fmt.Errorf("cursor private HOME ownership conflict")
	}
	f, err := root.Open(cursorPrivateHomeMarker)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := verifyCursorPrivateObject(f, false); err != nil {
		return err
	}
	opened, err := f.Stat()
	current, pathErr := root.Lstat(cursorPrivateHomeMarker)
	if err != nil || pathErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		return fmt.Errorf("cursor private HOME ownership identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(owner))+1))
	if err != nil || string(data) != owner {
		return fmt.Errorf("cursor private HOME ownership conflict")
	}
	return nil
}

// Native resources and native CLI settings/auth may live in different roots.
// Resolve both sources before any clone materialization; chats/projects never
// belong to the clone seed. An explicit non-native From is already a complete
// SDK profile and retains the normal single-directory Clone semantics.
func resolveCursorProfileRoots(opts skillruntime.ProfileResolveOptions, config driver.CommonConfig) (skillruntime.ProfileResolution, error) {
	resolution, err := resolveCursorProfileSources(opts, config)
	if err != nil || opts.SkipInitialize || opts.Selection == nil || opts.Selection.Mode != driver.ProfileModeClone || opts.Selection.Clone == nil || !opts.Selection.Clone.IncludeSettings {
		return resolution, err
	}
	source := opts.Selection.From
	nativeOpts := opts
	nativeOpts.Selection = &driver.ProfileSelection{Mode: driver.ProfileModeNative}
	nativeOpts.SkipInitialize = true
	native, err := skillruntime.ResolveProfile(nativeOpts)
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	if source != "" {
		source, err = engine.NormalizeProfileDir(source)
		if err != nil {
			return skillruntime.ProfileResolution{}, err
		}
	}
	if source == "" || source == native.Profile.Dir {
		source = resolveCursorConfigDir(config.Env)
		if strings.TrimSpace(os.Getenv("CURSOR_HOME")) != "" {
			source = native.Profile.Dir
		}
	}
	// AuthLink/Copy may already have materialized the mixed native file. For
	// AuthNone, copy only official static settings, never authInfo or caches.
	target := filepath.Join(resolution.Profile.Dir, "cli-config.json")
	if _, err := os.Lstat(target); err == nil {
		return resolution, nil
	} else if !os.IsNotExist(err) {
		return skillruntime.ProfileResolution{}, err
	}
	data, err := cursorStaticConfig([]driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: source}})
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	if string(data) == "{}" {
		return resolution, nil
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	return resolution, nil
}

func resolveCursorProfileSources(opts skillruntime.ProfileResolveOptions, config driver.CommonConfig) (skillruntime.ProfileResolution, error) {
	if opts.Selection == nil || opts.Selection.Mode != driver.ProfileModeClone {
		return skillruntime.ResolveProfile(opts)
	}
	nativeOpts := opts
	nativeOpts.Selection = &driver.ProfileSelection{Mode: driver.ProfileModeNative}
	nativeOpts.SkipInitialize = true
	native, err := skillruntime.ResolveProfile(nativeOpts)
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	from := opts.Selection.From
	if from != "" {
		from, err = engine.NormalizeProfileDir(from)
		if err != nil {
			return skillruntime.ProfileResolution{}, err
		}
	}
	if from != "" && from != native.Profile.Dir {
		return skillruntime.ResolveProfile(opts)
	}
	configDir := resolveCursorConfigDir(config.Env)
	if strings.TrimSpace(os.Getenv("CURSOR_HOME")) != "" {
		configDir = native.Profile.Dir
	}
	configDir, err = engine.NormalizeProfileDir(configDir)
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	if configDir == native.Profile.Dir {
		return skillruntime.ResolveProfile(opts)
	}
	clone := driver.CloneProfileOptions{}
	if opts.Selection.Clone != nil {
		clone = *opts.Selection.Clone
	}
	resources := opts
	resourceSelection := *opts.Selection
	resourceSelection.From = native.Profile.Dir
	resourceOptions := clone
	resourceOptions.IncludeSettings = false
	resourceOptions.AuthMode = driver.CloneProfileAuthNone
	resourceSelection.Clone = &resourceOptions
	resources.Selection = &resourceSelection
	resolution, err := skillruntime.ResolveProfile(resources)
	if err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	settings := opts
	settingsSelection := *opts.Selection
	settingsSelection.From = configDir
	settingsOptions := clone
	settingsOptions.IncludeMCP = false
	settingsOptions.IncludeSkills = false
	settingsSelection.Clone = &settingsOptions
	settings.Selection = &settingsSelection
	if _, err := skillruntime.ResolveProfile(settings); err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	return resolution, nil
}
