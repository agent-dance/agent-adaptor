package hostedprofile

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type seedEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Digest string `json:"digest"`
}
type seedRecord struct {
	Version     int         `json:"version"`
	DriverType  string      `json:"driver_type"`
	SourceDir   string      `json:"source_dir"`
	Entries     []seedEntry `json:"entries"`
	Fingerprint string      `json:"fingerprint"`
}

const seedOwnerName = ".agent-adaptor-seed-owner.json"

// ResourceRoots are the closed provider-visible configuration roots. Sessions,
// authentication contents and volatile SDK controls are deliberately absent.
func ResourceRoots(driverType string) []string {
	switch driverType {
	case "codex":
		return []string{"config.json", "config.toml", "instructions.md", "skills"}
	case "claude":
		return []string{"settings.json", "config.json", ".claude.json", "skills"}
	case "cursor":
		return []string{"config.json", "settings.json", "mcp.json", "skills"}
	case "codebuddy":
		return []string{"settings.json", ".mcp.json", "mcp.json", "skills"}
	}
	return nil
}
func authFiles(driverType string) []string {
	switch driverType {
	case "codex":
		return []string{"auth.json"}
	case "claude", "codebuddy":
		return []string{".credentials.json", "credentials.json"}
	case "cursor":
		return []string{"cli-config.json", "auth.json", "credentials.json"}
	}
	return nil
}
func (c *Claim) Initialize(ctx context.Context, seed func(context.Context, string) error) (returnErr error) {
	if c == nil {
		return unsafe("nil claim", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.root == nil || c.lock == nil || c.released || c.unlocked || c.closing {
		return unsafe("released claim", nil)
	}
	if c.state.Phase != "unseeded" {
		return c.validateReady()
	}
	if c.initializeAttempted {
		return c.initializeErr
	}
	if seed == nil {
		return unsafe("nil seed callback", nil)
	}
	if _, err := c.root.Lstat("profile"); !errors.Is(err, os.ErrNotExist) {
		return unsafe("unseeded profile already exists", err)
	}
	entries, err := fs.ReadDir(c.root.FS(), ".")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".seed-") {
			continue
		}
		g := strings.TrimPrefix(e.Name(), ".seed-")
		if !validHex(g, 32) {
			return unsafe("unknown seed staging", nil)
		}
		if err := c.removeSeedStage(e.Name(), g); err != nil {
			return err
		}
	}
	g, err := generation()
	if err != nil {
		return err
	}
	name := ".seed-" + g
	if err := makePrivateDir(c.root, name); err != nil {
		return err
	}
	r, err := checkedRoot(c.root, name)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := writeRecord(r, seedOwnerName, seedOwner{Version: 1, KeyHash: c.owner.KeyHash, Generation: g}); err != nil {
		return err
	}
	cleanup := true
	markerRemoved := false
	defer func() {
		if cleanup {
			if markerRemoved {
				returnErr = errors.Join(returnErr, writeRecord(r, seedOwnerName, seedOwner{Version: 1, KeyHash: c.owner.KeyHash, Generation: g}))
			}
			returnErr = errors.Join(returnErr, c.removeSeedStage(name, g))
		}
		if c.initializeAttempted && returnErr != nil {
			c.initializeErr = returnErr
		}
	}()
	c.initializeAttempted = true
	if err := seed(ctx, filepath.Join(filepath.Dir(c.dir), name)); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	collected, err := c.collectSeed(r)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(collected)
	record := seedRecord{Version: 1, DriverType: c.spec.DriverType, SourceDir: c.spec.SourceDir, Entries: collected, Fingerprint: hashBytes(raw)}
	if err := r.Remove(seedOwnerName); err != nil {
		return err
	}
	markerRemoved = true
	// Publish both durable artifacts before ready. A crash between publications
	// remains unseeded with an existing profile and therefore cannot re-seed it.
	if err := syncRoot(r); err != nil {
		return err
	}
	if err := publishWithin(c.root, name, "profile"); err != nil {
		return err
	}
	cleanup = false
	if err := writeRecord(c.root, "seed.json", record); err != nil {
		return err
	}
	c.seed = record
	c.state = stateRecord{Version: 1, Phase: "ready"}
	return c.writeState()
}
func (c *Claim) removeSeedStage(name, g string) error {
	r, err := checkedRoot(c.root, name)
	if err != nil {
		return err
	}
	defer r.Close()
	var owner seedOwner
	if err := readRecord(r, seedOwnerName, &owner); err != nil {
		return err
	}
	if owner != (seedOwner{Version: 1, KeyHash: c.owner.KeyHash, Generation: g}) {
		return unsafe("seed staging identity mismatch", nil)
	}
	before, _ := r.Stat(".")
	now, err := c.root.Lstat(name)
	if err != nil || !os.SameFile(before, now) {
		return unsafe("seed staging replaced", err)
	}
	return c.root.RemoveAll(name)
}
func (c *Claim) collectSeed(r *os.Root) ([]seedEntry, error) {
	out := []seedEntry{}
	var size int64
	for _, root := range ResourceRoots(c.spec.DriverType) {
		if _, err := r.Lstat(root); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := fs.WalkDir(r.FS(), root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			i, err := r.Lstat(path)
			if err != nil {
				return err
			}
			entry := seedEntry{Path: path, Mode: uint32(i.Mode().Perm()), Kind: "directory"}
			if !i.IsDir() {
				if !i.Mode().IsRegular() {
					return unsafe("non-regular seed resource", nil)
				}
				raw, err := ReadResourceFile(r, path, (64<<20)-size)
				if err != nil {
					return err
				}
				size += int64(len(raw))
				entry.Kind = "file"
				entry.Digest = hashBytes(raw)
			}
			out = append(out, entry)
			if len(out) > 20000 || size > 64<<20 {
				return unsafe("seed resource limit exceeded", nil)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range authFiles(c.spec.DriverType) {
		info, err := r.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := c.validateAuth(r, name); err != nil {
			return nil, err
		}
		out = append(out, seedEntry{Path: name, Kind: "auth-link", Mode: uint32(info.Mode().Perm())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (c *Claim) validateAuth(r *os.Root, name string) error {
	info, err := r.Lstat(name)
	if err != nil {
		return err
	}
	source := filepath.Join(c.spec.SourceDir, name)
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return unsafe("auth source unavailable", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return unsafe("auth source must be regular", nil)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := r.Readlink(name)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(r.Name(), target)
		}
		if filepath.Clean(target) != source {
			return unsafe("auth link target changed", nil)
		}
		return nil
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, sourceInfo) {
		return unsafe("auth file is not shared with canonical source", nil)
	}
	return nil
}
func (c *Claim) validateReady() error {
	r, err := checkedRoot(c.root, "profile")
	if err != nil {
		return err
	}
	defer r.Close()
	var record seedRecord
	if err := readRecord(c.root, "seed.json", &record); err != nil {
		return err
	}
	if record.Version != 1 || record.DriverType != c.spec.DriverType || record.SourceDir != c.spec.SourceDir || !validHex(record.Fingerprint, 64) || record.Entries == nil || len(record.Entries) > 20000 {
		return unsafe("invalid seed record", nil)
	}
	prev := ""
	for _, e := range record.Entries {
		if !safeRelative(e.Path) || e.Path <= prev || e.Mode > 0777 {
			return unsafe("invalid seed path or mode", nil)
		}
		prev = e.Path
		switch e.Kind {
		case "file":
			if !validHex(e.Digest, 64) {
				return unsafe("invalid seed digest", nil)
			}
		case "directory":
			if e.Digest != "" {
				return unsafe("invalid directory digest", nil)
			}
		case "auth-link":
			if e.Digest != "" {
				return unsafe("invalid auth digest", nil)
			}
			allowed := false
			for _, name := range authFiles(c.spec.DriverType) {
				if name == e.Path {
					allowed = true
				}
			}
			if !allowed {
				return unsafe("unrecognized auth link", nil)
			}
			if err := c.validateAuth(r, e.Path); err != nil {
				return err
			}
		default:
			return unsafe("invalid seed kind", nil)
		}
	}
	raw, _ := json.Marshal(record.Entries)
	if hashBytes(raw) != record.Fingerprint {
		return unsafe("seed fingerprint mismatch", nil)
	}
	c.seed = record
	return nil
}

// SeedPaths returns an immutable list of initial non-auth resource boundaries.
func (c *Claim) SeedPaths() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []string{}
	for _, e := range c.seed.Entries {
		if e.Kind != "auth-link" {
			out = append(out, e.Path)
		}
	}
	return out
}
