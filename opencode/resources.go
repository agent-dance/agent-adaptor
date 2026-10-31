package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func resolveProfile(ctx context.Context, c Config, selection *driver.ProfileSelection, initialize bool) (skillruntime.ProfileResolution, error) {
	if err := ctx.Err(); err != nil {
		return skillruntime.ProfileResolution{}, err
	}
	if selection != nil && selection.Mode == driver.ProfileModeClone && selection.Clone != nil && selection.Clone.AuthMode != driver.CloneProfileAuthNone {
		return skillruntime.ProfileResolution{}, errors.New("opencode: profile auth cloning is unsupported; auth belongs to XDG_DATA_HOME")
	}
	native := nativeProfileDir(c)
	r, err := skillruntime.ResolveProfile(skillruntime.ProfileResolveOptions{Bindings: c.Env, Selection: selection, EnvVar: "OPENCODE_CONFIG_DIR", DefaultDir: native, NativeSharedDir: native, DedicatedSubdirs: []string{"skills"}, SkillsDirs: []string{"skills"}, SkipInitialize: !initialize})
	// OpenCode stores auth under XDG_DATA_HOME, independently of its config
	// directory. Do not pretend a profile clone copied or isolated credentials.
	r.Profile.DriverType = DriverType
	if err == nil && initialize && selection != nil && selection.Mode == driver.ProfileModeClone && selection.Clone != nil && (selection.Clone.IncludeSettings || selection.Clone.IncludeMCP) {
		source := r.CloneSource
		// Bound lock acquisition while retaining caller cancellation.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = profilejson.Clone(ctx, source, r.Profile.Dir, profilejson.ClonePlan{
			Files: []string{"opencode.json", "opencode.jsonc"},
			KeepField: func(field string) bool {
				if field == "mcp" {
					return selection.Clone.IncludeMCP
				}
				return selection.Clone.IncludeSettings
			},
			Resources: []profilejson.Layout{mcpProfileLayout(source), agentProfileLayout(source)},
		})
	}
	return r, err
}

// runEnvironment overlays native JSON through the official environment entry
// point. Per-run capabilities never rewrite a shared profile or leave stale
// host-tool endpoints behind after Agent.Close.
func runEnvironment(req driver.Request, bindings []driver.EnvBinding, server bool, disabledMCP ...string) ([]driver.EnvBinding, func(), error) {
	cleanup := func() {}
	env, err := driverutil.RuntimeEnvBindings(bindings, req.Runtime)
	if err != nil {
		return nil, cleanup, err
	}
	config := map[string]any{}
	if raw, _ := driverutil.ResolvedEnvValue(env, "OPENCODE_CONFIG_CONTENT"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			return nil, cleanup, fmt.Errorf("opencode: OPENCODE_CONFIG_CONTENT must be a JSON object: %w", err)
		}
		if config == nil {
			return nil, cleanup, errors.New("opencode: OPENCODE_CONFIG_CONTENT cannot be null")
		}
	}
	if len(req.MCP.Servers) > 0 || len(disabledMCP) > 0 {
		servers, err := objectField(config, "mcp")
		if err != nil {
			return nil, cleanup, err
		}
		for _, key := range disabledMCP {
			servers[key] = map[string]any{"enabled": false}
		}
		for _, spec := range req.MCP.Servers {
			entry, err := nativeMCPEntry(spec, env, false)
			if err != nil {
				return nil, cleanup, err
			}
			// Native config layers deep-merge nested maps. Disable these slots
			// during startup; the server API installs exact connection values
			// before any prompt, without inheriting old headers or environments.
			entry["enabled"] = false
			raw, err := marshalNativeMCP(entry, "")
			if err != nil {
				return nil, cleanup, err
			}
			servers[spec.Key] = json.RawMessage(raw)
		}
	}
	if len(req.Skills.Entries) > 0 {
		skills, err := objectField(config, "skills")
		if err != nil {
			return nil, cleanup, err
		}
		paths, err := stringArray(skills["paths"])
		if err != nil {
			return nil, cleanup, err
		}
		for _, skill := range req.Skills.Entries {
			if _, err := os.Stat(filepath.Join(skill.SourcePath, "SKILL.md")); err != nil {
				return nil, cleanup, fmt.Errorf("opencode skill %q: %w", skill.Key, err)
			}
			paths = append(paths, skill.SourcePath)
		}
		skills["paths"] = paths
	}
	policy := driver.EffectiveHumanDecisionPolicy(req.Policy.HumanDecision)
	if server && (policy.OnReject == driver.FailureContinue || policy.OnTimeout == driver.FailureContinue) {
		experimental, err := objectField(config, "experimental")
		if err != nil {
			return nil, cleanup, err
		}
		experimental["continue_loop_on_deny"] = true
	}
	if ref := req.Instructions; ref != nil && !server {
		paths, err := stringArray(config["instructions"])
		if err != nil {
			return nil, cleanup, err
		}
		content := ref.Content
		file, err := os.CreateTemp("", "agent-adaptor-opencode-instructions-*.md")
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.Remove(file.Name()) }
		_, writeErr := file.WriteString(content)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		config["instructions"] = append(paths, file.Name())
	}
	raw, err := json.Marshal(config)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	env = skillruntime.WithBinding(env, "OPENCODE_CONFIG_CONTENT", string(raw))
	return env, cleanup, nil
}
func objectField(root map[string]any, key string) (map[string]any, error) {
	if value, ok := root[key]; ok {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("opencode: %s must be an object", key)
		}
		return object, nil
	}
	object := map[string]any{}
	root[key] = object
	return object, nil
}
func stringArray(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("opencode: native resource paths must be an array")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("opencode: native resource path must be a string")
		}
		out = append(out, s)
	}
	return out, nil
}
