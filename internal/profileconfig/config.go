package profileconfig

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilereconcile"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func Snapshot(driverType, profileDir string, payload driver.ProfileConfigPayload, synced bool) engine.ResourceSnapshot {
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceConfig,
		Fingerprint:     payload.Fingerprint,
		Support:         engine.ProfileResourceSupportUnsupported,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	nativeKeys, supportedCapabilityKeys, unsupportedCapabilityKeys, providerErrors, capabilityWarnings := classifyPatches(driverType, payload)
	out.Warnings = append(out.Warnings, capabilityWarnings...)
	managedKeys := append(cloneStrings(nativeKeys), supportedCapabilityKeys...)
	sort.Strings(managedKeys)
	switch {
	case len(nativeKeys) > 0:
		out.Support = engine.ProfileResourceSupportNativeEscape
		if synced {
			out.Managed = managedKeys
			out.Materialization = engine.ProfileResourceMaterializationNativeManaged
		} else {
			out.Warnings = append(out.Warnings, "config patches are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
		}
	case len(supportedCapabilityKeys) > 0:
		out.Support = engine.ProfileResourceSupportPortableExtended
		if synced {
			out.Managed = managedKeys
			out.Materialization = engine.ProfileResourceMaterializationNativeManaged
		} else {
			out.Warnings = append(out.Warnings, "config capability patches are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
		}
	case len(unsupportedCapabilityKeys) > 0:
		out.Warnings = append(out.Warnings, "config capability patches are not materialized by this adapter yet")
	}
	for _, key := range unsupportedCapabilityKeys {
		out.Warnings = append(out.Warnings, fmt.Sprintf("config capability patch %q is unsupported by this adapter", key))
	}
	out.Warnings = append(out.Warnings, providerErrors...)
	if synced && (len(unsupportedCapabilityKeys) > 0 || len(providerErrors) > 0) {
		parts := cloneStrings(providerErrors)
		if len(unsupportedCapabilityKeys) > 0 {
			parts = append(parts, "config capability patches are not materialized by this adapter yet")
		}
		out.Error = strings.Join(parts, "; ")
	}
	return out
}

func WithSnapshotResource(snapshot engine.ProfileSnapshot, resource engine.ResourceSnapshot) engine.ProfileSnapshot {
	for i := range snapshot.Resources {
		if snapshot.Resources[i].Kind == resource.Kind {
			snapshot.Resources[i] = resource
			return snapshot
		}
	}
	snapshot.Resources = append(snapshot.Resources, resource)
	return snapshot
}

func SyncNativePatches(ctx context.Context, driverType, profileDir string, payload driver.ProfileConfigPayload) (engine.ResourceSnapshot, error) {
	type plannedPatch struct {
		key    string
		native driver.NativeConfigPatch
	}
	planned := make([]plannedPatch, 0, len(payload.Patches))
	for _, patch := range payload.Patches {
		native, ok, err := effectivePatchForDriver(driverType, patch)
		if err != nil {
			return engine.ResourceSnapshot{}, err
		}
		if !ok {
			if strings.TrimSpace(patch.Capability) != "" {
				return engine.ResourceSnapshot{}, fmt.Errorf("config capability patch %q is unsupported by adapter %q", patch.Key, driverType)
			}
			continue
		}
		planned = append(planned, plannedPatch{key: patch.Key, native: native})
	}
	if len(planned) == 0 {
		return Snapshot(driverType, profileDir, payload, true), nil
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	defer lock.Release()

	for _, patch := range planned {
		target, err := targetPath(profileDir, patch.native.Path)
		if err != nil {
			return engine.ResourceSnapshot{}, fmt.Errorf("config patch %q: %w", patch.key, err)
		}
		kind, err := structuredKind(patch.native.FileKind)
		if err != nil {
			return engine.ResourceSnapshot{}, fmt.Errorf("config patch %q: %w", patch.key, err)
		}
		if err := profilereconcile.ApplyStructuredPatch(profilereconcile.StructuredPatch{
			FileKind: kind,
			Path:     target,
			Section:  patch.native.Section,
			Values:   patch.native.Values,
		}); err != nil {
			return engine.ResourceSnapshot{}, fmt.Errorf("config patch %q: %w", patch.key, err)
		}
	}
	return Snapshot(driverType, profileDir, payload, true), nil
}

func effectivePatchForDriver(driverType string, patch driver.ProfileConfigPatch) (driver.NativeConfigPatch, bool, error) {
	if strings.TrimSpace(patch.Capability) != "" {
		native, ok, _, err := capabilityPatchForDriver(driverType, patch)
		return native, ok, err
	}
	return nativePatchForDriver(driverType, patch)
}

func nativePatchForDriver(driverType string, patch driver.ProfileConfigPatch) (driver.NativeConfigPatch, bool, error) {
	if strings.TrimSpace(patch.Capability) != "" {
		return driver.NativeConfigPatch{}, false, nil
	}
	native := driver.NativeConfigPatch{
		FileKind: patch.FileKind,
		Path:     patch.Path,
		Section:  patch.Section,
		Values:   cloneAnyMap(patch.Values),
	}
	if patch.Native != nil {
		native = *patch.Native
		native.Values = cloneAnyMap(patch.Native.Values)
		if len(native.Values) == 0 {
			native.Values = cloneAnyMap(patch.Values)
		}
	}
	if strings.TrimSpace(native.Provider) != "" && strings.TrimSpace(native.Provider) != driverType {
		return driver.NativeConfigPatch{}, false, fmt.Errorf("native config patch %q targets provider %q, not %q", patch.Key, native.Provider, driverType)
	}
	if native.FileKind == "" && strings.TrimSpace(native.Path) == "" {
		return driver.NativeConfigPatch{}, false, nil
	}
	return native, true, nil
}

func classifyPatches(driverType string, payload driver.ProfileConfigPayload) ([]string, []string, []string, []string, []string) {
	nativeKeys := make([]string, 0)
	supportedCapabilityKeys := make([]string, 0)
	unsupportedCapabilityKeys := make([]string, 0)
	providerErrors := make([]string, 0)
	capabilityWarnings := make([]string, 0)
	for _, patch := range payload.Patches {
		if strings.TrimSpace(patch.Capability) != "" {
			_, ok, warnings, err := capabilityPatchForDriver(driverType, patch)
			capabilityWarnings = append(capabilityWarnings, warnings...)
			if err != nil {
				providerErrors = append(providerErrors, err.Error())
				continue
			}
			if ok {
				supportedCapabilityKeys = append(supportedCapabilityKeys, patch.Key)
			} else {
				unsupportedCapabilityKeys = append(unsupportedCapabilityKeys, patch.Key)
			}
			continue
		}
		_, ok, err := nativePatchForDriver(driverType, patch)
		if err != nil {
			providerErrors = append(providerErrors, err.Error())
			continue
		}
		if ok {
			nativeKeys = append(nativeKeys, patch.Key)
		}
	}
	sort.Strings(nativeKeys)
	sort.Strings(supportedCapabilityKeys)
	sort.Strings(unsupportedCapabilityKeys)
	sort.Strings(providerErrors)
	sort.Strings(capabilityWarnings)
	return nativeKeys, supportedCapabilityKeys, unsupportedCapabilityKeys, providerErrors, capabilityWarnings
}

func capabilityPatchForDriver(driverType string, patch driver.ProfileConfigPatch) (driver.NativeConfigPatch, bool, []string, error) {
	capability := strings.ToLower(strings.TrimSpace(patch.Capability))
	values := cloneAnyMap(patch.Values)
	warnings := make([]string, 0)
	switch driverType {
	case "codex":
		switch capability {
		case "model":
			model, ok := stringValue(values, "model", "id", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires model value", patch.Key)
			}
			return tomlPatch("config.toml", "", map[string]any{"model": model}), true, nil, nil
		case "reasoning", "reasoning_effort":
			effort, ok := stringValue(values, "effort", "reasoning_effort", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires reasoning effort value", patch.Key)
			}
			return tomlPatch("config.toml", "", map[string]any{"model_reasoning_effort": effort}), true, nil, nil
		case "sandbox", "isolation":
			mode, ok := stringValue(values, "mode", "sandbox_mode", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires sandbox mode value", patch.Key)
			}
			warnings = appendUnsupportedFields(warnings, patch.Key, capability, values, "mode", "sandbox_mode", "value")
			return tomlPatch("config.toml", "", map[string]any{"sandbox_mode": mode}), true, warnings, nil
		case "approval", "permission":
			mode, ok := stringValue(values, "mode", "policy", "approval_policy", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires approval policy value", patch.Key)
			}
			return tomlPatch("config.toml", "", map[string]any{"approval_policy": mode}), true, nil, nil
		}
	case "claude":
		switch capability {
		case "model":
			model, ok := stringValue(values, "model", "id", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires model value", patch.Key)
			}
			return jsonPatch("settings.json", "", map[string]any{"model": model}), true, nil, nil
		case "effort", "reasoning", "reasoning_effort":
			effort, ok := stringValue(values, "effort", "reasoning_effort", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires effort value", patch.Key)
			}
			return jsonPatch("settings.json", "", map[string]any{"effort": effort}), true, nil, nil
		case "permission", "approval":
			mode, ok := stringValue(values, "mode", "permissionMode", "permission_mode", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires permission mode value", patch.Key)
			}
			return jsonPatch("settings.json", "", map[string]any{"permissionMode": mode}), true, nil, nil
		case "env":
			if len(values) == 0 {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires env values", patch.Key)
			}
			return jsonPatch("settings.json", "env", values), true, nil, nil
		}
	case "cursor":
		switch capability {
		case "sandbox", "isolation":
			if len(values) == 0 {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires sandbox values", patch.Key)
			}
			return jsonPatch("cli-config.json", "sandbox", values), true, nil, nil
		case "approval", "permission":
			mode, ok := stringValue(values, "mode", "approvalMode", "approval_mode", "value")
			if !ok {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires approval mode value", patch.Key)
			}
			return jsonPatch("cli-config.json", "", map[string]any{"approvalMode": mode}), true, nil, nil
		case "permissions":
			if len(values) == 0 {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires permission values", patch.Key)
			}
			return jsonPatch("cli-config.json", "permissions", values), true, nil, nil
		case "display", "ui":
			if len(values) == 0 {
				return driver.NativeConfigPatch{}, false, nil, fmt.Errorf("config capability patch %q requires display values", patch.Key)
			}
			return jsonPatch("cli-config.json", "display", values), true, nil, nil
		}
	}
	return driver.NativeConfigPatch{}, false, nil, nil
}

func tomlPatch(path, section string, values map[string]any) driver.NativeConfigPatch {
	return driver.NativeConfigPatch{FileKind: driver.ProfileConfigFileTOML, Path: path, Section: section, Values: cloneAnyMap(values)}
}

func jsonPatch(path, section string, values map[string]any) driver.NativeConfigPatch {
	return driver.NativeConfigPatch{FileKind: driver.ProfileConfigFileJSON, Path: path, Section: section, Values: cloneAnyMap(values)}
}

func stringValue(values map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			typed = strings.TrimSpace(typed)
			if typed != "" {
				return typed, true
			}
		case fmt.Stringer:
			text := strings.TrimSpace(typed.String())
			if text != "" {
				return text, true
			}
		}
	}
	return "", false
}

func appendUnsupportedFields(warnings []string, patchKey, capability string, values map[string]any, supported ...string) []string {
	allowed := map[string]struct{}{}
	for _, key := range supported {
		allowed[key] = struct{}{}
	}
	for key := range values {
		if _, ok := allowed[key]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("config capability patch %q field %q is not mapped for capability %q", patchKey, key, capability))
	}
	return warnings
}

func structuredKind(kind driver.ProfileConfigFileKind) (profilereconcile.StructuredFileKind, error) {
	switch kind {
	case driver.ProfileConfigFileJSON:
		return profilereconcile.StructuredJSON, nil
	case driver.ProfileConfigFileTOML:
		return profilereconcile.StructuredTOML, nil
	default:
		return "", fmt.Errorf("unsupported native config file kind %q", kind)
	}
}

func targetPath(profileDir, relPath string) (string, error) {
	profileDir = filepath.Clean(strings.TrimSpace(profileDir))
	relPath = filepath.Clean(strings.TrimSpace(relPath))
	if profileDir == "." || profileDir == "" {
		return "", fmt.Errorf("profile directory is required")
	}
	if relPath == "." || relPath == "" {
		return "", fmt.Errorf("native config path is required")
	}
	if filepath.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("native config path %q escapes profile root", relPath)
	}
	target := filepath.Join(profileDir, relPath)
	if !pathWithin(profileDir, target) {
		return "", fmt.Errorf("native config path %q escapes profile root", relPath)
	}
	return target, nil
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
