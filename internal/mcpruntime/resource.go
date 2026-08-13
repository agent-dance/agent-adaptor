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

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
)

const resourceKind = string(engine.ProfileResourceMCP)

type providerLayout struct {
	driverType string
	profileDir string
	path       string
	field      string
	format     string
	render     func(driver.MCPServerSpec) (map[string]any, error)
}

func SnapshotResource(driverType, profileDir string, payload driver.MCPPayload, synced bool) (engine.ResourceSnapshot, error) {
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceMCP,
		Fingerprint:     payload.Fingerprint,
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
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
		return engine.ResourceSnapshot{}, err
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	root, err := readStructuredRoot(layout)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	managed, external, err := snapshotState(layout, manifest, root)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	out.Managed = managed
	out.External = external
	if len(managed) > 0 || len(external) > 0 {
		out.Materialization = engine.ProfileResourceMaterializationNativeManaged
	}
	return out, nil
}

func SyncResource(ctx context.Context, driverType, profileDir string, kind ProfileKind, payload driver.MCPPayload) (engine.ResourceSnapshot, error) {
	layout, err := layoutFor(driverType, profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	// An empty desired set with no SDK-owned entries is observational, not a
	// sync mutation. In particular, rewriting a user's Codex config.toml just
	// to preserve an empty MCP section can reformat or discard TOML constructs
	// outside the MCP surface. Leave the provider file byte-for-byte intact.
	if len(payload.Servers) == 0 && len(manifest.KindEntries(resourceKind)) == 0 {
		return SnapshotResource(driverType, profileDir, payload, true)
	}
	root, err := readStructuredRoot(layout)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	current, err := sectionMap(root, layout.field)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	desired, err := desiredServers(layout, payload)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	if err := rejectHostedToolCollisions(layout, payload.Servers, current, manifest); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	next := cloneAnyMap(current)
	for key, value := range desired {
		next[key] = value
	}
	for _, server := range payload.Servers {
		metadata := map[string]string{
			"provider":  layout.driverType,
			"transport": string(server.Transport),
		}
		if isHostedToolServer(server) {
			metadata["owner"] = toolidentity.ManifestOwner
			metadata["rendered_fingerprint"] = renderedServerFingerprint(desired[server.Key])
		}
		manifest.Set(profilestate.ManifestEntry{
			Kind:        resourceKind,
			Key:         server.Key,
			Path:        layout.path,
			Fingerprint: serverFingerprint(server),
			Metadata:    metadata,
		})
	}
	if kind == ProfileKindHostManaged {
		pruneManagedServers(layout.path, desired, &manifest, next)
	}

	if err := writeSection(layout, root, next); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	return SnapshotResource(driverType, profileDir, payload, true)
}

// rejectHostedToolCollisions protects a user's native profile entry from the
// one SDK-reserved key. Ordinary WithMCP declarations retain their established
// overlay behavior; the Agent-owned Tool runtime is stricter because it would
// otherwise silently replace an unrelated server and leave a stale local
// endpoint in a shared profile.
func rejectHostedToolCollisions(layout providerLayout, servers []driver.MCPServerSpec, current map[string]any, manifest profilestate.Manifest) error {
	for _, server := range servers {
		if !isHostedToolServer(server) {
			continue
		}
		if _, exists := current[server.Key]; !exists {
			continue
		}
		entry, managed := manifest.Entry(resourceKind, server.Key)
		if managed && filepath.Clean(entry.Path) == filepath.Clean(layout.path) &&
			entry.Metadata["owner"] == toolidentity.ManifestOwner &&
			entry.Metadata["rendered_fingerprint"] == renderedServerFingerprint(current[server.Key]) {
			continue
		}
		return fmt.Errorf("%w: hosted Tool MCP key %q already belongs to an external profile server", engine.ErrInvalidMCPConfig, server.Key)
	}
	return nil
}

// SupportsHostedToolProfile reports whether the built-in provider has a
// profile materializer whose Agent-owned Tool entry can be cleaned safely.
// Third-party Drivers consume the normalized MCP payload directly and retain
// ownership of any provider-specific persistence they choose to perform.
func SupportsHostedToolProfile(driverType string) bool {
	switch driverType {
	case "codex", "claude", "cursor", "codebuddy":
		return true
	default:
		return false
	}
}

// RemoveHostedToolProfile removes only the exact profile entry last written
// by the Agent-owned Tool materializer. The owner marker prevents adopting a
// pre-existing user entry, while rendered_fingerprint prevents deleting an
// entry that the user or another process changed after materialization.
func RemoveHostedToolProfile(ctx context.Context, driverType, profileDir string) error {
	if _, err := os.Stat(profileDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	layout, err := layoutFor(driverType, profileDir)
	if err != nil {
		return err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return err
	}
	entry, managed := manifest.Entry(resourceKind, toolidentity.ServerKey)
	if !managed || filepath.Clean(entry.Path) != filepath.Clean(layout.path) ||
		entry.Metadata["owner"] != toolidentity.ManifestOwner {
		return nil
	}
	root, err := readStructuredRoot(layout)
	if err != nil {
		return err
	}
	current, err := sectionMap(root, layout.field)
	if err != nil {
		return err
	}
	if value, exists := current[toolidentity.ServerKey]; exists {
		if entry.Metadata["rendered_fingerprint"] != renderedServerFingerprint(value) {
			// Relinquish the stale ownership record but preserve the changed
			// provider-native entry byte-for-byte at the semantic level.
			manifest.Remove(resourceKind, toolidentity.ServerKey)
			return profilestate.SaveManifest(profileDir, manifest)
		}
		delete(current, toolidentity.ServerKey)
		if err := writeSection(layout, root, current); err != nil {
			return err
		}
	}
	manifest.Remove(resourceKind, toolidentity.ServerKey)
	return profilestate.SaveManifest(profileDir, manifest)
}

func isHostedToolServer(server driver.MCPServerSpec) bool {
	return server.Key == toolidentity.ServerKey &&
		server.Transport == driver.MCPTransportHTTP &&
		toolidentity.IsBearerTokenEnvVar(server.BearerTokenEnvVar) &&
		server.Required && server.RequiredReason == toolidentity.RequiredReason
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
	case "codebuddy":
		return providerLayout{
			driverType: driverType,
			profileDir: profileDir,
			// CodeBuddy USER 作用域按优先级读取 .mcp.json > mcp.json（已废弃）>
			// ~/.codebuddy.json，且不合并同作用域多个文件。写最高优先级的
			// .mcp.json 可保证始终被 CLI 读到，不被遗留文件遮蔽。
			path:   filepath.Join(profileDir, ".mcp.json"),
			field:  "mcpServers",
			format: "json",
			render: codebuddyServerConfig,
		}, nil
	default:
		return providerLayout{}, fmt.Errorf("mcp profile materialization is unsupported by driver %q", driverType)
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

func desiredServers(layout providerLayout, payload driver.MCPPayload) (map[string]any, error) {
	desired := make(map[string]any, len(payload.Servers))
	for _, server := range payload.Servers {
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

func serverFingerprint(server driver.MCPServerSpec) string {
	raw, err := json.Marshal(server)
	if err != nil {
		raw = []byte(server.Key + string(server.Transport) + server.Command + server.URL + server.BearerTokenEnvVar)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func renderedServerFingerprint(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(fmt.Sprintf("%T:%v", value, value))
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
