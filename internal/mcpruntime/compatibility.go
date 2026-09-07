package mcpruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/hostedprofile"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/profile"
)

// HostedCompatibilityBaseline observes the actual provider root, removing only
// the hosted entry whose owner, layout and rendered bytes all prove ownership.
// It does not alter desired payloads or write a provider configuration.
func HostedCompatibilityBaseline(driverType, dir string) (string, []byte, error) {
	l, err := layoutFor(driverType, dir)
	if err != nil {
		return "", nil, err
	}
	for _, name := range []string{l.path, filepath.Join(dir, profilestate.ManifestName)} {
		if info, err := os.Lstat(name); err == nil {
			if info.Size() > 64<<20 {
				return "", nil, fmt.Errorf("%w: oversized profile control", profile.ErrUnsafe)
			}
			if !info.Mode().IsRegular() {
				return "", nil, fmt.Errorf("%w: non-regular profile control", profile.ErrUnsafe)
			}
		} else if !os.IsNotExist(err) {
			return "", nil, err
		}
	}
	m, err := ReadHostedManifest(dir)
	if err != nil {
		return "", nil, err
	}
	root, err := readStructuredRoot(l)
	if err != nil {
		return "", nil, err
	}
	section, err := sectionMap(root, l.field)
	if err != nil {
		return "", nil, err
	}
	if value, exists := section[toolidentity.ServerKey]; exists {
		e, owned := m.Entry(resourceKind, toolidentity.ServerKey)
		if !owned || e.Metadata["owner"] != toolidentity.ManifestOwner || e.Metadata["provider"] != driverType || filepath.Clean(e.Path) != l.path {
			return "", nil, fmt.Errorf("%w: hosted key ownership conflict", engine.ErrInvalidMCPConfig)
		}
		if e.Metadata["rendered_fingerprint"] != renderedServerFingerprint(value) {
			return "", nil, fmt.Errorf("%w: hosted rendered value changed", profile.ErrUnsafe)
		}
		server := mapFromAny(value)
		carrier := ""
		if driverType == "codex" {
			carrier, _ = server["bearer_token_env_var"].(string)
		} else {
			headers := mapFromAny(server["headers"])
			auth, _ := headers["Authorization"].(string)
			if driverType == "cursor" {
				carrier = strings.TrimSuffix(strings.TrimPrefix(auth, "Bearer ${env:"), "}")
			} else {
				carrier = strings.TrimSuffix(strings.TrimPrefix(auth, "Bearer ${"), "}")
			}
		}
		if !toolidentity.IsBearerTokenEnvVar(carrier) {
			return "", nil, fmt.Errorf("%w: hosted credential carrier changed", profile.ErrUnsafe)
		}
		for key, v := range section {
			if key == toolidentity.ServerKey {
				continue
			}
			other := mapFromAny(v)
			if other["bearer_token_env_var"] == carrier {
				return "", nil, fmt.Errorf("%w: profile server aliases hosted carrier", engine.ErrInvalidMCPConfig)
			}
			for _, h := range mapFromAny(other["headers"]) {
				if s, ok := h.(string); ok && (strings.Contains(s, "${"+carrier+"}") || strings.Contains(s, "${env:"+carrier+"}")) {
					return "", nil, fmt.Errorf("%w: profile server aliases hosted carrier", engine.ErrInvalidMCPConfig)
				}
			}
		}
		delete(section, toolidentity.ServerKey)
	}
	if len(section) == 0 {
		delete(root, l.field)
	} else {
		root[l.field] = section
	}
	if len(root) == 0 {
		return filepath.Base(l.path), nil, nil
	}
	raw, err := json.Marshal(root)
	return filepath.Base(l.path), raw, err
}

// StrictProfileJSON preserves numeric values and rejects duplicate keys in the
// explicit provider configuration surface, without interpreting provider output.
func StrictProfileJSON(raw []byte) ([]byte, error) {
	var value map[string]any
	if err := hostedprofile.DecodeJSON(raw, &value, false); err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, nil
	}
	return json.Marshal(value)
}

// ReadHostedManifest rejects ambiguous control JSON before any ownership proof.
// Unknown resource kinds remain data and are included by the core snapshot.
func ReadHostedManifest(dir string) (profilestate.Manifest, error) {
	path := filepath.Join(dir, profilestate.ManifestName)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return profilestate.Manifest{Version: 1}, nil
	}
	if err != nil {
		return profilestate.Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return profilestate.Manifest{}, fmt.Errorf("%w: profile manifest must be regular", profile.ErrUnsafe)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return profilestate.Manifest{}, err
	}
	if len(raw) > 64<<20 {
		return profilestate.Manifest{}, fmt.Errorf("%w: oversized profile manifest", profile.ErrUnsafe)
	}
	var m profilestate.Manifest
	if err := hostedprofile.DecodeJSON(raw, &m, true); err != nil {
		return profilestate.Manifest{}, err
	}
	if m.Version != 1 {
		return profilestate.Manifest{}, fmt.Errorf("%w: profile manifest version", profile.ErrUnsafe)
	}
	return m, nil
}
