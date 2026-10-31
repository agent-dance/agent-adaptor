package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func agentProfileLayout(root string) profilejson.Layout {
	return profilejson.Layout{Provider: DriverType, Root: root, File: "opencode.jsonc", Field: "agent", Kind: driver.ProfileResourceAgents, Materialization: driver.ProfileResourceMaterializationNativeManaged, Ordered: true}
}

// Portable agents become native subagents, leaving selection/delegation to the
// host and the provider. This does not introduce an SDK execution entry point.
func agentProfilePlan(root string, payload driver.AgentPayload) (profilejson.Layout, map[string]json.RawMessage, error) {
	layout := agentProfileLayout(root)
	layout.Metadata = map[string]map[string]string{}
	entries := map[string]json.RawMessage{}
	keys := map[string]bool{}
	for _, spec := range payload.Agents {
		if strings.TrimSpace(spec.Key) == "" || keys[spec.Key] {
			return layout, nil, errors.New("opencode: profile agent keys must be nonempty and unique")
		}
		keys[spec.Key] = true
		name := spec.RuntimeName
		if name == "" {
			name = spec.Key
		}
		if !validAgentName(name) || entries[name] != nil {
			return layout, nil, fmt.Errorf("opencode: invalid, reserved or duplicate profile agent name %q", name)
		}
		if spec.SourcePath != "" {
			raw, err := importAgentSource(spec)
			if err != nil {
				return layout, nil, err
			}
			entries[name] = raw
			layout.Metadata[name] = map[string]string{"resource_key": spec.Key}
			continue
		}
		if spec.SandboxMode != "" || len(spec.Hooks) != 0 || len(spec.MCPServers) != 0 || len(spec.Skills) != 0 {
			return layout, nil, fmt.Errorf("opencode: agent %q sandbox, hooks, MCP references and skill preloading are not mapped: %w", spec.Key, errors.ErrUnsupported)
		}
		// Clone through JSON so captured host maps are never mutated and only
		// values representable by the official configuration can be delivered.
		entry := map[string]any{}
		if len(spec.Native) != 0 {
			raw, err := json.Marshal(spec.Native)
			if err != nil {
				return layout, nil, fmt.Errorf("opencode: agent %q native values must be JSON: %w", spec.Key, err)
			}
			if err := decodeNativeObject(raw, &entry); err != nil {
				return layout, nil, err
			}
			for key := range entry {
				switch key {
				case "temperature", "top_p", "steps", "hidden", "color", "options":
				default:
					return layout, nil, fmt.Errorf("opencode: agent %q native field %q is unsupported; use the portable field when available: %w", spec.Key, key, errors.ErrUnsupported)
				}
			}
		}
		entry["mode"] = "subagent"
		entry["disable"] = false
		entry["description"] = spec.Description
		if spec.Description == "" {
			entry["description"] = "SDK-managed profile agent " + spec.Key
		}
		entry["prompt"] = spec.Instructions
		if spec.Instructions == "" {
			entry["prompt"] = "Follow the host-provided role description for this agent."
		}
		if spec.Model != "" && spec.Model != "inherit" {
			provider, model, ok := strings.Cut(spec.Model, "/")
			if !ok || provider == "" || model == "" {
				return layout, nil, fmt.Errorf("opencode: agent %q model must have provider/model form", spec.Key)
			}
			entry["model"] = spec.Model
		}
		if spec.ReasoningEffort != "" {
			if entry["model"] == nil {
				return layout, nil, fmt.Errorf("opencode: agent %q reasoning variant requires an explicit model", spec.Key)
			}
			entry["variant"] = spec.ReasoningEffort
		}
		permission := map[string]string{}
		switch spec.PermissionMode {
		case "", "default":
		case "acceptEdits":
			permission["edit"] = "allow"
		case "bypassPermissions":
			permission["*"] = "allow"
		case "dontAsk":
			permission["*"] = "deny"
		default:
			return layout, nil, fmt.Errorf("opencode: agent %q permission mode %q is not mapped: %w", spec.Key, spec.PermissionMode, errors.ErrUnsupported)
		}
		if spec.ToolPolicy != nil {
			if len(spec.ToolPolicy.Allow) > 0 {
				permission["*"] = "deny"
			}
			for _, group := range []struct {
				names  []string
				action string
			}{{spec.ToolPolicy.Allow, "allow"}, {spec.ToolPolicy.Deny, "deny"}} {
				for _, tool := range group.names {
					name, err := nativeAgentTool(tool)
					if err != nil {
						return layout, nil, fmt.Errorf("opencode: agent %q: %w", spec.Key, err)
					}
					permission[name] = group.action
				}
			}
		}
		if len(permission) > 0 {
			entry["permission"] = permission
		}
		if err := validateAgentNativeOptions(entry); err != nil {
			return layout, nil, fmt.Errorf("opencode: agent %q: %w", spec.Key, err)
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return layout, nil, err
		}
		// Portable instructions/options are literals, never native file/env
		// templates. Keep their escaped spelling through every config overlay.
		raw = bytes.ReplaceAll(raw, []byte("{env:"), []byte(`\u007benv:`))
		raw = bytes.ReplaceAll(raw, []byte("{file:"), []byte(`\u007bfile:`))
		entries[name] = raw
		layout.Metadata[name] = map[string]string{"resource_key": spec.Key}
	}
	return layout, entries, nil
}

func validAgentName(name string) bool {
	switch name {
	case "", "build", "plan", "general", "explore", "title", "summary", "compaction", "__proto__", "constructor", "prototype":
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func nativeAgentTool(name string) (string, error) {
	// OpenCode groups all file mutations under edit. A portable deny for any
	// member must deny the group; allow-only lists cannot safely split it.
	switch name {
	case "Bash":
		return "bash", nil
	case "Read":
		return "read", nil
	case "Edit", "Write", "MultiEdit", "write", "patch", "apply_patch":
		return "", fmt.Errorf("tool %q shares OpenCode's edit permission; use native tool name edit for the group: %w", name, errors.ErrUnsupported)
	case "Glob":
		return "glob", nil
	case "Grep":
		return "grep", nil
	case "WebFetch":
		return "webfetch", nil
	case "WebSearch":
		return "websearch", nil
	case "Task", "Agent":
		return "task", nil
	case "Skill":
		return "skill", nil
	}
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "*?()[]{}\\/ \t\r\n") {
		return "", fmt.Errorf("tool policy requires exact native tool names: %w", errors.ErrUnsupported)
	}
	return name, nil
}

func validateAgentNativeOptions(entry map[string]any) error {
	for _, key := range []string{"temperature", "top_p", "steps"} {
		if value, exists := entry[key]; exists {
			n, ok := value.(json.Number)
			if !ok {
				return fmt.Errorf("native %s must be a number", key)
			}
			if _, err := n.Float64(); err != nil {
				return fmt.Errorf("native %s must be a finite number", key)
			}
			if key == "steps" {
				if i, err := n.Int64(); err != nil || i <= 0 {
					return errors.New("native steps must be a positive integer")
				}
			}
		}
	}
	if value, exists := entry["hidden"]; exists {
		if _, ok := value.(bool); !ok {
			return errors.New("native hidden must be a boolean")
		}
	}
	if value, exists := entry["options"]; exists {
		if _, ok := value.(map[string]any); !ok {
			return errors.New("native options must be an object")
		}
	}
	if value, exists := entry["color"]; exists {
		color, ok := value.(string)
		if !ok || !nativeAgentColor.MatchString(color) {
			return errors.New("native color must be a six-digit hex color or a native theme color")
		}
	}
	return nil
}

var nativeAgentColor = regexp.MustCompile(`^(#[0-9a-fA-F]{6}|primary|secondary|accent|success|warning|error|info)$`)

type preparedAgentProfile struct {
	layout  profilejson.Layout
	entries map[string]json.RawMessage
}

func observeProfileAgents(root string, payload driver.AgentPayload, prepared ...preparedAgentProfile) (profilejson.State, error) {
	var layout profilejson.Layout
	var desired map[string]json.RawMessage
	var err error
	if len(prepared) > 0 {
		layout, desired = prepared[0].layout, prepared[0].entries
		layout.Root = root
	} else {
		layout, desired, err = agentProfilePlan(root, payload)
		if err != nil {
			return profilejson.State{}, err
		}
	}
	state, err := profilejson.Observe(layout, desired, payload.Fingerprint)
	if err != nil {
		return state, err
	}
	external, err := externalProfileAgents(root)
	if err != nil {
		return state, err
	}
	for key := range external {
		// A native Markdown or lower JSON definition is merged with JSONC,
		// rather than replaced. Its ownership cannot be claimed by the SDK.
		if state.Managed[key] {
			delete(state.Managed, key)
			delete(state.Owners, key)
		}
	}
	state.Snapshot.Managed = nil
	state.Snapshot.External = nil
	for key := range state.Entries {
		if state.Managed[key] {
			resourceKey := state.Owners[key].Metadata["resource_key"]
			if resourceKey == "" {
				return state, errors.New("opencode: persisted agent has no resource key")
			}
			state.Snapshot.Managed = append(state.Snapshot.Managed, resourceKey)
		} else {
			external[key] = true
		}
	}
	for key := range external {
		state.Snapshot.External = append(state.Snapshot.External, key)
	}
	sort.Strings(state.Snapshot.Managed)
	sort.Strings(state.Snapshot.External)
	if len(state.Managed) == 0 {
		state.Snapshot.Materialization = driver.ProfileResourceMaterializationNotMaterialized
	}
	return state, nil
}

func externalProfileAgents(root string) (map[string]bool, error) {
	entries, err := profilejson.ReadEntries(root, "opencode.json", "agent")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for key := range entries {
		out[key] = true
	}
	for _, directory := range []string{"agent", "agents"} {
		base := filepath.Join(root, directory)
		if err := scanExternalAgentNames(base, base, nil, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Native discovery follows links. Traverse their logical names while checking
// directory identities for cycles, without claiming ownership of their targets.
func scanExternalAgentNames(base, path string, ancestors []os.FileInfo, out map[string]bool) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() && filepath.Ext(path) == ".md" {
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			out[strings.TrimSuffix(filepath.ToSlash(rel), ".md")] = true
		}
		return nil
	}
	for _, prior := range ancestors {
		if os.SameFile(prior, info) {
			return nil
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := scanExternalAgentNames(base, filepath.Join(path, entry.Name()), append(ancestors, info), out); err != nil {
			return err
		}
	}
	return nil
}

type agentInvocation struct {
	Entries  map[string]json.RawMessage
	Disabled []string
}

func (p agentInvocation) active() bool { return len(p.Entries) > 0 || len(p.Disabled) > 0 }

func resolveProfileAgents(req driver.Request, root string, prepared ...map[string]json.RawMessage) (agentInvocation, error) {
	state, err := observeProfileAgents(root, driver.AgentPayload{})
	if err != nil {
		return agentInvocation{}, err
	}
	plan := agentInvocation{Entries: map[string]json.RawMessage{}}
	for key := range state.Owners {
		plan.Entries[key] = state.Entries[key]
	}
	if !req.ProfilePayload.Declared.Agents && len(req.ProfilePayload.Agents.Agents) == 0 {
		return plan, nil
	}
	var entries map[string]json.RawMessage
	if len(prepared) > 0 {
		entries = prepared[0]
	} else {
		_, entries, err = agentProfilePlan(root, req.ProfilePayload.Agents)
		if err != nil {
			return plan, err
		}
	}
	for key := range entries {
		for _, external := range state.Snapshot.External {
			if key == external {
				return plan, fmt.Errorf("opencode: profile agent %q conflicts with external native configuration", key)
			}
		}
	}
	for key := range plan.Entries {
		if entries[key] == nil {
			plan.Disabled = append(plan.Disabled, key)
		}
	}
	sort.Strings(plan.Disabled)
	plan.Entries = entries
	return plan, nil
}

func (p agentInvocation) environment(env []driver.EnvBinding) ([]driver.EnvBinding, error) {
	if !p.active() {
		return env, nil
	}
	var config map[string]json.RawMessage
	raw, _ := driverutil.ResolvedEnvValue(env, "OPENCODE_CONFIG_CONTENT")
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, err
	}
	agents := map[string]json.RawMessage{}
	if prior := config["agent"]; prior != nil {
		if err := decodeNativeObject(prior, &agents); err != nil {
			return nil, err
		}
	}
	for _, key := range p.Disabled {
		agents[key] = json.RawMessage(`{"disable":true}`)
	}
	for key, value := range p.Entries {
		agents[key] = value
	}
	config["agent"], _ = json.Marshal(agents)
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return skillruntime.WithBinding(env, "OPENCODE_CONFIG_CONTENT", string(encoded)), nil
}

func (p agentInvocation) verify(ctx context.Context, client *serverClient) error {
	if !p.active() {
		return nil
	}
	raw, err := client.call(ctx, "GET", "/config", nil)
	if err != nil {
		return err
	}
	var config struct {
		Agent map[string]map[string]any `json:"agent"`
	}
	if err := decodeNativeObject(raw, &config); err != nil {
		return err
	}
	var literal struct {
		Agent map[string]map[string]json.RawMessage `json:"agent"`
	}
	if err := json.Unmarshal(raw, &literal); err != nil {
		return err
	}
	for _, key := range p.Disabled {
		if config.Agent[key]["disable"] != true {
			return fmt.Errorf("opencode: profile agent %q was not disabled before prompt delivery", key)
		}
	}
	for key, raw := range p.Entries {
		var expected map[string]any
		if err := decodeNativeObject(raw, &expected); err != nil {
			return err
		}
		actual := config.Agent[key]
		var expectedLiteral map[string]json.RawMessage
		if err := json.Unmarshal(raw, &expectedLiteral); err != nil {
			return err
		}
		if wanted := expectedLiteral["permission"]; wanted != nil {
			left, err := configPermissionRules(wanted)
			if err != nil {
				return err
			}
			right, err := configPermissionRules(literal.Agent[key]["permission"])
			if err != nil || !reflect.DeepEqual(effectivePermissionRules(left), effectivePermissionRules(right)) {
				return fmt.Errorf("opencode: profile agent %q permission precedence changed before prompt delivery", key)
			}
		}
		// Official ConfigAgent.normalize always supplies options, and native
		// Markdown supplies name. All other fields must match exactly: partial
		// deep merges must never silently retain old model/permission settings.
		for _, field := range []string{"options", "permission"} {
			if value, ok := actual[field].(map[string]any); ok && len(value) == 0 && expected[field] == nil {
				delete(actual, field)
			}
		}
		if actual["name"] == key {
			delete(actual, "name")
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("opencode: profile agent %q differs from its declared configuration before prompt delivery", key)
		}
	}
	return p.verifyVariants(ctx, client)
}

// OpenCode silently ignores an agent.variant missing from the model's variant
// map. A declared reasoning request must instead fail before prompt delivery.
func (p agentInvocation) verifyVariants(ctx context.Context, client *serverClient) error {
	type variantRequest struct{ Model, Variant string }
	requested := map[string]variantRequest{}
	for key, raw := range p.Entries {
		var entry variantRequest
		if err := json.Unmarshal(raw, &entry); err != nil {
			return err
		}
		if entry.Variant != "" {
			requested[key] = entry
		}
	}
	if len(requested) == 0 {
		return nil
	}
	raw, err := client.call(ctx, "GET", "/provider", nil)
	if err != nil {
		return err
	}
	var providers struct {
		All []struct {
			ID     string `json:"id"`
			Models map[string]struct {
				Variants map[string]json.RawMessage `json:"variants"`
			} `json:"models"`
		} `json:"all"`
	}
	if err := decodeNativeObject(raw, &providers); err != nil {
		return err
	}
	for key, request := range requested {
		providerID, modelID, _ := strings.Cut(request.Model, "/")
		found := false
		for _, provider := range providers.All {
			if provider.ID == providerID {
				variant := provider.Models[modelID].Variants[request.Variant]
				found = len(variant) > 0 && !bytes.Equal(bytes.TrimSpace(variant), []byte("null"))
			}
		}
		if !found {
			return fmt.Errorf("opencode: profile agent %q requests an unavailable model reasoning variant: %w", key, errors.ErrUnsupported)
		}
	}
	return nil
}
