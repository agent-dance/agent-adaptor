package mcpruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
)

const hostedToolsTestBearerEnv = toolidentity.BearerTokenEnvVarPrefix + "0123456789ABCDEF0123456789ABCDEF"

func TestHostedToolsRejectExternalReservedKeyAcrossProviderProfiles(t *testing.T) {
	for _, driverType := range []string{"codex", "claude", "cursor", "codebuddy"} {
		t.Run(driverType, func(t *testing.T) {
			profileDir := t.TempDir()
			layout, err := layoutFor(driverType, profileDir)
			if err != nil {
				t.Fatal(err)
			}
			root := map[string]any{
				layout.field: map[string]any{
					toolidentity.ServerKey: map[string]any{"command": "external-server"},
				},
			}
			if err := writeStructuredRoot(layout, root); err != nil {
				t.Fatalf("write native profile: %v", err)
			}
			before, err := os.ReadFile(layout.path)
			if err != nil {
				t.Fatal(err)
			}

			_, err = SyncResource(context.Background(), driverType, profileDir, ProfileKindShared, hostedToolsPayload("http://127.0.0.1:12345/mcp"))
			if !errors.Is(err, engine.ErrInvalidMCPConfig) {
				t.Fatalf("SyncResource error = %v, want ErrInvalidMCPConfig", err)
			}
			after, readErr := os.ReadFile(layout.path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Fatal("external reserved-key server was modified on collision")
			}
			manifest, manifestErr := profilestate.LoadManifest(profileDir)
			if manifestErr != nil {
				t.Fatal(manifestErr)
			}
			if _, exists := manifest.Entry(resourceKind, toolidentity.ServerKey); exists {
				t.Fatal("collision created an SDK ownership manifest entry")
			}
		})
	}
}

func TestHostedToolsMayReplaceItsOwnManagedStaleEndpoint(t *testing.T) {
	profileDir := t.TempDir()
	layout, err := layoutFor("cursor", profileDir)
	if err != nil {
		t.Fatal(err)
	}
	root := map[string]any{
		layout.field: map[string]any{
			toolidentity.ServerKey: map[string]any{"type": "http", "url": "http://127.0.0.1:1/mcp"},
		},
	}
	if err := writeStructuredRoot(layout, root); err != nil {
		t.Fatal(err)
	}
	manifest := profilestate.Manifest{}
	manifest.Set(profilestate.ManifestEntry{
		Kind: resourceKind,
		Key:  toolidentity.ServerKey,
		Path: layout.path,
		Metadata: map[string]string{
			"owner":                toolidentity.ManifestOwner,
			"rendered_fingerprint": renderedServerFingerprint(root[layout.field].(map[string]any)[toolidentity.ServerKey]),
		},
	})
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		t.Fatal(err)
	}

	if _, err := SyncResource(context.Background(), "cursor", profileDir, ProfileKindShared, hostedToolsPayload("http://127.0.0.1:2/mcp")); err != nil {
		t.Fatalf("replace managed endpoint: %v", err)
	}
	raw, err := os.ReadFile(filepath.Clean(layout.path))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || !containsAll(string(raw), "127.0.0.1:2", hostedToolsTestBearerEnv) {
		t.Fatalf("managed hosted Tool endpoint was not updated: %s", raw)
	}
}

func TestRemoveHostedToolProfileRemovesOnlyOwnedUnchangedEntry(t *testing.T) {
	profileDir := t.TempDir()
	payload := hostedToolsPayload("http://127.0.0.1:12345/mcp")
	if _, err := SyncResource(context.Background(), "cursor", profileDir, ProfileKindShared, payload); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHostedToolProfile(context.Background(), "cursor", profileDir); err != nil {
		t.Fatal(err)
	}
	layout, _ := layoutFor("cursor", profileDir)
	root, err := readStructuredRoot(layout)
	if err != nil {
		t.Fatal(err)
	}
	section, err := sectionMap(root, layout.field)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := section[toolidentity.ServerKey]; exists {
		t.Fatal("owned hosted Tool entry remained after cleanup")
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := manifest.Entry(resourceKind, toolidentity.ServerKey); exists {
		t.Fatal("owned hosted Tool manifest entry remained after cleanup")
	}
}

func TestRemoveHostedToolProfilePreservesEntryChangedAfterMaterialization(t *testing.T) {
	profileDir := t.TempDir()
	payload := hostedToolsPayload("http://127.0.0.1:12345/mcp")
	if _, err := SyncResource(context.Background(), "cursor", profileDir, ProfileKindShared, payload); err != nil {
		t.Fatal(err)
	}
	layout, _ := layoutFor("cursor", profileDir)
	root, err := readStructuredRoot(layout)
	if err != nil {
		t.Fatal(err)
	}
	section, err := sectionMap(root, layout.field)
	if err != nil {
		t.Fatal(err)
	}
	section[toolidentity.ServerKey] = map[string]any{"command": "user-replacement"}
	if err := writeSection(layout, root, section); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHostedToolProfile(context.Background(), "cursor", profileDir); err != nil {
		t.Fatal(err)
	}
	root, err = readStructuredRoot(layout)
	if err != nil {
		t.Fatal(err)
	}
	section, err = sectionMap(root, layout.field)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(fmt.Sprint(section[toolidentity.ServerKey]), "user-replacement") {
		t.Fatalf("changed external entry was removed: %#v", section)
	}
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := manifest.Entry(resourceKind, toolidentity.ServerKey); exists {
		t.Fatal("stale ownership marker was not relinquished")
	}
}

func hostedToolsPayload(endpoint string) driver.MCPPayload {
	server := driver.MCPServerSpec{
		Key:               toolidentity.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               endpoint,
		BearerTokenEnvVar: hostedToolsTestBearerEnv,
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	return driver.MCPPayload{Servers: []driver.MCPServerSpec{server}, Fingerprint: serverFingerprint(server)}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
