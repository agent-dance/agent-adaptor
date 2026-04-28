package mcpruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

const resourceKind = string(agentadaptor.ProfileResourceMCP)

type providerLayout struct {
	driverType string
	profileDir string
	path       string
	field      string
	format     string
	render     func(agentadaptor.MCPServerSpec) (map[string]any, error)
}

func SnapshotResource(driverType, profileDir string, payload agentadaptor.MCPPayload, synced bool) (agentadaptor.ResourceSnapshot, error) {
	out := agentadaptor.ResourceSnapshot{
		Kind:            agentadaptor.ProfileResourceMCP,
		Fingerprint:     payload.Fingerprint,
		Support:         agentadaptor.ProfileResourceSupportPortableCore,
		Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	if !synced {
		if len(payload.Servers) > 0 {
			out.Warnings = append(out.Warnings, "mcp resources are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
		}
		return out, nil
	}
	layout, err := layoutFor(driverType, profileDir)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	root, err := readStructuredRoot(layout)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	managed, external, err := snapshotState(layout, manifest, root)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	out.Managed = managed
	out.External = external
	if len(managed) > 0 || len(external) > 0 {
		out.Materialization = agentadaptor.ProfileResourceMaterializationNativeManaged
	}
	return out, nil
}

func SyncResource(ctx context.Context, driverType, profileDir string, kind ProfileKind, payload agentadaptor.MCPPayload) (agentadaptor.ResourceSnapshot, error) {
	layout, err := layoutFor(driverType, profileDir)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	root, err := readStructuredRoot(layout)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	current, err := sectionMap(root, layout.field)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	desired, err := desiredServers(layout, payload.Servers)
	if err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	next := cloneAnyMap(current)
	for key, value := range desired {
		next[key] = value
	}
	for _, server := range payload.Servers {
		manifest.Set(profilestate.ManifestEntry{
			Kind:        resourceKind,
			Key:         server.Key,
			Path:        layout.path,
			Fingerprint: serverFingerprint(server),
			Metadata: map[string]string{
				"provider":  layout.driverType,
				"transport": string(server.Transport),
			},
		})
	}
	if kind == ProfileKindHostManaged {
		pruneManagedServers(layout.path, desired, &manifest, next)
	}

	if err := writeSection(layout, root, next); err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return agentadaptor.ResourceSnapshot{}, err
	}
	return SnapshotResource(driverType, profileDir, payload, true)
}

func layoutFor(driverType, profileDir string) (providerLayout, error) {
	profileDir = filepath.Clean(strings.TrimSpace(profileDir))
	if profileDir == "." || profileDir == "" {
		return providerLayout{}, fmt.Errorf("mcp profile materialization requires profile directory")
	}
	switch driverType {
	case "codex":
		return providerLayout{
			driverType: driverType,
			profileDir: profileDir,
			path:       filepath.Join(profileDir, "config.toml"),
			field:      "mcp_servers",
			format:     "toml",
			render:     codexServerConfig,
		}, nil
	case "claude":
		return providerLayout{
			driverType: driverType,
			profileDir: profileDir,
			path:       filepath.Join(profileDir, ".claude.json"),
			field:      "mcpServers",
			format:     "json",
			render:     claudeServerConfig,
		}, nil
	case "cursor":
		return providerLayout{
			driverType: driverType,
			profileDir: profileDir,
			path:       filepath.Join(profileDir, "mcp.json"),
			field:      "mcpServers",
			format:     "json",
			render:     cursorServerConfig,
		}, nil
	default:
		return providerLayout{}, fmt.Errorf("mcp profile materialization is unsupported by adapter %q", driverType)
	}
}

func readStructuredRoot(layout providerLayout) (map[string]any, error) {
	switch layout.format {
	case "json":
		return readJSONObject(layout.path)
	case "toml":
		return readTOMLObject(layout.path)
	default:
		return nil, fmt.Errorf("unsupported mcp profile format %q", layout.format)
	}
}

func writeStructuredRoot(layout providerLayout, root map[string]any) error {
	switch layout.format {
	case "json":
		return writeJSONObject(layout.path, root)
	case "toml":
		return writeTOMLObject(layout.path, root)
	default:
		return fmt.Errorf("unsupported mcp profile format %q", layout.format)
	}
}

func desiredServers(layout providerLayout, servers []agentadaptor.MCPServerSpec) (map[string]any, error) {
	desired := make(map[string]any, len(servers))
	for _, server := range servers {
		if _, exists := desired[server.Key]; exists {
			return nil, fmt.Errorf("mcp server %q is declared more than once", server.Key)
		}
		entry, err := layout.render(server)
		if err != nil {
			return nil, err
		}
		desired[server.Key] = entry
	}
	return desired, nil
}

func sectionMap(root map[string]any, field string) (map[string]any, error) {
	value, ok := root[field]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	typed := mapFromAny(value)
	if typed == nil {
		return nil, fmt.Errorf("mcp config field %q must be an object", field)
	}
	return typed, nil
}

func writeSection(layout providerLayout, root, section map[string]any) error {
	if len(section) == 0 {
		delete(root, layout.field)
	} else {
		root[layout.field] = section
	}
	if len(root) == 0 {
		if err := os.Remove(layout.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeStructuredRoot(layout, root)
}

func pruneManagedServers(path string, desired map[string]any, manifest *profilestate.Manifest, section map[string]any) {
	for _, entry := range manifest.KindEntries(resourceKind) {
		if filepath.Clean(entry.Path) != filepath.Clean(path) {
			continue
		}
		if _, keep := desired[entry.Key]; keep {
			continue
		}
		delete(section, entry.Key)
		manifest.Remove(resourceKind, entry.Key)
	}
}

func snapshotState(layout providerLayout, manifest profilestate.Manifest, root map[string]any) ([]string, []string, error) {
	section, err := sectionMap(root, layout.field)
	if err != nil {
		return nil, nil, err
	}
	managedSet := map[string]struct{}{}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if filepath.Clean(entry.Path) != filepath.Clean(layout.path) {
			continue
		}
		if _, exists := section[entry.Key]; exists {
			managedSet[entry.Key] = struct{}{}
		}
	}
	managed := make([]string, 0, len(managedSet))
	for key := range managedSet {
		managed = append(managed, key)
	}
	external := make([]string, 0, len(section))
	for key := range section {
		if _, managedKey := managedSet[key]; managedKey {
			continue
		}
		external = append(external, key)
	}
	sort.Strings(managed)
	sort.Strings(external)
	return managed, external, nil
}

func serverFingerprint(server agentadaptor.MCPServerSpec) string {
	raw, err := json.Marshal(server)
	if err != nil {
		raw = []byte(server.Key + string(server.Transport) + server.Command + server.URL + server.BearerTokenEnvVar)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
