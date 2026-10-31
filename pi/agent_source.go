package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/agentmarkdown"
)

// SourcePath imports the official subagent example's name/description/tools/
// model frontmatter and Markdown system prompt. Host keys and runtime names
// remain authoritative; the source name never selects another host resource.
func importAgentSource(spec driver.AgentSpec) (driver.AgentSpec, error) {
	if spec.Description != "" || spec.Instructions != "" || spec.Model != "" || spec.ReasoningEffort != "" || spec.ToolPolicy != nil || spec.PermissionMode != "" || spec.SandboxMode != "" || len(spec.MCPServers)+len(spec.Skills)+len(spec.Hooks)+len(spec.Native) > 0 {
		return spec, errors.New("pi: SourcePath defines the complete agent; do not combine it with inline agent fields")
	}
	doc, err := agentmarkdown.Read(spec.SourcePath)
	if err != nil {
		return spec, fmt.Errorf("pi: agent source: %w", err)
	}
	for key := range doc.Fields {
		switch key {
		case "name", "description", "model", "tools":
		default:
			return spec, fmt.Errorf("pi: unsupported agent frontmatter field %q: %w", key, errors.ErrUnsupported)
		}
	}
	name, err := agentmarkdown.String(doc.Fields, "name", true)
	if err != nil {
		return spec, err
	}
	if !agentNamePattern.MatchString(name) {
		return spec, errors.New("pi: invalid source agent name")
	}
	spec.Description, err = agentmarkdown.String(doc.Fields, "description", true)
	if err != nil {
		return spec, err
	}
	spec.Model, err = agentmarkdown.String(doc.Fields, "model", false)
	if err != nil {
		return spec, err
	}
	if raw, ok := doc.Fields["tools"]; ok {
		var values []string
		if len(raw) > 0 && raw[0] == '"' {
			var value string
			_ = json.Unmarshal(raw, &value)
			values = strings.Split(value, ",")
		} else {
			var items []json.RawMessage
			if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
				return spec, errors.New("pi: source tools must be a comma-separated string or string array")
			}
			for _, item := range items {
				var value string
				if len(item) == 0 || item[0] != '"' || json.Unmarshal(item, &value) != nil {
					return spec, errors.New("pi: source tools array must contain only strings")
				}
				values = append(values, value)
			}
		}
		policy := &driver.AgentToolPolicy{}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				policy.Allow = append(policy.Allow, value)
			}
		}
		// The official example treats an empty tools list as inheritance.
		if len(policy.Allow) > 0 {
			spec.ToolPolicy = policy
		}
	}
	spec.Instructions = doc.Body
	spec.SourcePath = ""
	spec.SourceFingerprint, err = driver.CanonicalSessionConfigFingerprint("pi/agent-source/v1", []string{spec.SourceFingerprint, doc.Fingerprint})
	return spec, err
}

// Capture once, before profile initialization. Subsequent normalization and
// persisted discovery only see resolved values, never a mutable source path.
func prepareAgentPayload(payload driver.AgentPayload) (driver.AgentPayload, error) {
	resolved := make([]driver.AgentSpec, 0, len(payload.Agents))
	for _, spec := range payload.Agents {
		value, err := normalizeAgent(spec)
		if err != nil {
			return payload, err
		}
		resolved = append(resolved, value)
	}
	payload.Agents = resolved
	return payload, nil
}
