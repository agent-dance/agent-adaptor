package pi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
)

const agentProfileFile = "agent-adaptor-agents.json"
const subagentTool = "adaptor_subagent"

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func agentProfileLayout(root string) profilejson.Layout {
	return profilejson.Layout{Provider: DriverType, Root: root, File: agentProfileFile, Field: "agents", Kind: driver.ProfileResourceAgents, Materialization: driver.ProfileResourceMaterializationFileManaged}
}

func nativeAgentTool(name string) (string, error) {
	aliases := map[string]string{"Read": "read", "Bash": "bash", "Edit": "edit", "Write": "write", "Grep": "grep", "Glob": "find", "LS": "ls"}
	if alias := aliases[name]; alias != "" {
		name = alias
	}
	switch name {
	case "read", "bash", "edit", "write", "grep", "find", "ls", reviewPlanTool, askUserTool:
		return name, nil
	default:
		return "", fmt.Errorf("pi: subagent tool %q has no exact supported mapping: %w", name, errors.ErrUnsupported)
	}
}

func normalizeAgent(spec driver.AgentSpec) (driver.AgentSpec, error) {
	if spec.SourcePath != "" {
		var err error
		spec, err = importAgentSource(spec)
		if err != nil {
			return spec, err
		}
	}
	if spec.Key == "" {
		return spec, errors.New("pi: subagent key is empty")
	}
	if spec.RuntimeName == "" {
		spec.RuntimeName = spec.Key
	}
	if !agentNamePattern.MatchString(spec.RuntimeName) {
		return spec, errors.New("pi: subagent runtime name requires ASCII letters, digits, hyphens or underscores")
	}
	if len(spec.Hooks) != 0 || len(spec.Native) != 0 || spec.SandboxMode != "" || (spec.PermissionMode != "" && spec.PermissionMode != "default") {
		return spec, fmt.Errorf("pi: subagent hooks, native fields, sandbox and permission-mode overrides are unsupported: %w", errors.ErrUnsupported)
	}
	for _, values := range []*[]string{&spec.Skills, &spec.MCPServers} {
		seen := map[string]bool{}
		var normalized []string
		for _, name := range *values {
			if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\r\n") {
				return spec, errors.New("pi: invalid subagent resource reference")
			}
			if !seen[name] {
				normalized = append(normalized, name)
				seen[name] = true
			}
		}
		sort.Strings(normalized)
		*values = normalized
	}
	if spec.Model == "inherit" {
		spec.Model = ""
	}
	if strings.TrimSpace(spec.Model) != spec.Model || strings.ContainsAny(spec.Model, "\x00\r\n") {
		return spec, errors.New("pi: invalid subagent model")
	}
	if provider, model, found := strings.Cut(spec.Model, "/"); found && (provider == "" || model == "") {
		return spec, errors.New("pi: subagent model requires provider/model")
	}
	if err := (configuredDriver{cfg: Config{Thinking: spec.ReasoningEffort}}).ValidateConfig(nil); err != nil {
		return spec, err
	}
	if spec.ToolPolicy != nil {
		policy := &driver.AgentToolPolicy{}
		for _, pair := range []struct {
			source []string
			dest   *[]string
		}{{spec.ToolPolicy.Allow, &policy.Allow}, {spec.ToolPolicy.Deny, &policy.Deny}} {
			seen := map[string]bool{}
			for _, name := range pair.source {
				native, err := nativeAgentTool(name)
				if err != nil {
					return spec, err
				}
				if !seen[native] {
					*pair.dest = append(*pair.dest, native)
					seen[native] = true
				}
			}
			sort.Strings(*pair.dest)
		}
		spec.ToolPolicy = policy
	}
	return spec, nil
}

func agentProfileEntries(payload driver.AgentPayload) (map[string]json.RawMessage, error) {
	entries := map[string]json.RawMessage{}
	names := map[string]bool{}
	for _, spec := range payload.Agents {
		value, err := normalizeAgent(spec)
		if err != nil {
			return nil, err
		}
		if names[value.RuntimeName] || entries[value.Key] != nil {
			return nil, errors.New("pi: duplicate subagent key or runtime name")
		}
		names[value.RuntimeName] = true
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		entries[value.Key] = raw
	}
	return entries, nil
}

func resolveProfileAgents(req driver.Request, root string) (driver.AgentPayload, error) {
	payload := req.ProfilePayload.Agents
	if !req.ProfilePayload.Declared.Agents {
		entries, err := profilejson.ReadEntries(root, agentProfileFile, "agents")
		if err != nil {
			return payload, err
		}
		for key, raw := range entries {
			var spec driver.AgentSpec
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&spec); err != nil {
				return payload, fmt.Errorf("pi: invalid stored subagent %q: %w", key, err)
			}
			if spec.Key != key {
				return payload, errors.New("pi: stored subagent key mismatch")
			}
			payload.Agents = append(payload.Agents, spec)
		}
	}
	entries, err := agentProfileEntries(payload)
	if err != nil {
		return payload, err
	}
	payload.Agents = nil
	for _, raw := range entries {
		var spec driver.AgentSpec
		_ = json.Unmarshal(raw, &spec)
		payload.Agents = append(payload.Agents, spec)
	}
	sort.Slice(payload.Agents, func(i, j int) bool { return payload.Agents[i].Key < payload.Agents[j].Key })
	payload.Fingerprint, err = driver.CanonicalSessionConfigFingerprint("pi/profile-agents/v1", payload.Agents)
	return payload, err
}
