package profilejson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/profilekind"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/tailscale/hujson"
)

// ClonePlan selects top-level fields from a group of native config files. The
// group is seeded only when none of its target files exist, so a partially
// populated destination never acquires new lower-precedence settings. Resource
// ownership is copied only for selected fields with verified source content.
type ClonePlan struct {
	Files     []string
	KeepField func(string) bool
	Resources []Layout
}

func Clone(ctx context.Context, source, target string, plan ClonePlan) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source == "" || target == "" || plan.KeepField == nil {
		return errors.New("profile JSON clone requires source, destination and field selection")
	}
	for _, file := range plan.Files {
		if err := (Layout{Root: source, File: file, Provider: "clone", Field: "clone", Kind: "clone"}).validate(); err != nil {
			return err
		}
	}
	for _, layout := range plan.Resources {
		layout.Root = source
		if err := layout.validate(); err != nil {
			return err
		}
	}
	sourceInfo, err := os.Stat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return errors.New("profile JSON clone source must be a directory")
	}
	targetInfo, statErr := os.Stat(target)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if profilekind.SamePath(source, target) || statErr == nil && os.SameFile(sourceInfo, targetInfo) {
		return nil
	}
	// Canonical paths provide a consistent lock order even through directory
	// links. The actual lock file remains shared with profile synchronization.
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	roots := make([]string, 0, 2)
	for _, root := range []string{source, target} {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		roots = append(roots, resolved)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, acquireErr := profilestate.AcquireLock(ctx, root, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
		if acquireErr != nil {
			return acquireErr
		}
		defer func() { err = errors.Join(err, lock.Release()) }()
	}
	for _, file := range plan.Files {
		if _, err := os.Lstat(filepath.Join(target, file)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	sourceManifest, err := profilestate.LoadManifest(source)
	if err != nil {
		return err
	}
	manifest, err := profilestate.LoadManifest(target)
	if err != nil {
		return err
	}
	documents := map[string]document{}
	outputs := map[string][]byte{}
	for _, file := range plan.Files {
		doc, err := readDocument(filepath.Join(source, file))
		if err != nil {
			return err
		}
		if !doc.exists {
			continue
		}
		documents[file] = doc
		fields := map[string]json.RawMessage{}
		for _, member := range doc.value.Value.(*hujson.Object).Members {
			name := member.Name.Value.(hujson.Literal).String()
			if plan.KeepField(name) {
				value := member.Value.Clone()
				value.Standardize()
				fields[name] = value.Pack()
			}
		}
		if len(fields) > 0 {
			// Canonical output deliberately omits comments: comments adjacent to
			// selected fields can contain values from excluded resource families.
			outputs[file], err = json.MarshalIndent(fields, "", "  ")
			if err != nil {
				return err
			}
			outputs[file] = append(outputs[file], '\n')
		}
	}
	owners := 0
	for _, layout := range plan.Resources {
		if outputs[layout.File] == nil || !plan.KeepField(layout.Field) {
			continue
		}
		layout.Root = source
		state, err := observe(layout, documents[layout.File], sourceManifest, nil, "")
		if err != nil {
			return err
		}
		for _, entry := range sourceManifest.KindEntries(string(layout.Kind)) {
			if entry.Metadata["provider"] != layout.Provider {
				continue
			}
			if _, exists := state.Entries[entry.Key]; exists && !state.Managed[entry.Key] {
				return fmt.Errorf("profile JSON clone: %s entry %q has unverifiable source ownership", layout.Kind, entry.Key)
			}
		}
		for _, entry := range state.Owners {
			if _, exists := manifest.Entry(entry.Kind, entry.Key); exists {
				return fmt.Errorf("profile JSON clone: destination already records %s entry %q", entry.Kind, entry.Key)
			}
			entry.Path = filepath.Join(target, layout.File)
			manifest.Set(entry)
			owners++
		}
	}
	// Check non-cooperating source writers before publishing any file.
	for file, doc := range documents {
		current, err := readDocument(filepath.Join(source, file))
		if err != nil || !bytes.Equal(current.raw, doc.raw) {
			return errors.Join(err, errors.New("profile JSON clone source changed during preparation"))
		}
	}
	written := map[string][]byte{}
	committed := false
	defer func() {
		if committed {
			return
		}
		for file, raw := range written {
			path := filepath.Join(target, file)
			current, readErr := readDocument(path)
			if readErr != nil || !current.exists || !bytes.Equal(current.raw, raw) {
				err = errors.Join(err, errors.New("profile JSON clone rollback refused: destination changed externally"))
				continue
			}
			err = errors.Join(err, os.Remove(path))
		}
	}()
	for _, file := range plan.Files {
		raw := outputs[file]
		if raw == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		created, err := publishNewFile(filepath.Join(target, file), raw)
		if created {
			written[file] = raw
		}
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if owners > 0 {
		if err := profilestate.SaveManifest(target, manifest); err != nil {
			return err
		}
	}
	committed = true
	return nil
}

// Publish a complete private copy without ever replacing an existing target.
// The temporary hard link is local to the destination, never to the source.
func publishNewFile(path string, raw []byte) (created bool, err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".profile-clone-*")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, os.Remove(file.Name())) }()
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return false, err
	}
	err = os.Link(file.Name(), path)
	return err == nil, err
}
