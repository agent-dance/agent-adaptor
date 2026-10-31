package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/agentmarkdown"
)

func importAgentSource(spec driver.AgentSpec) (json.RawMessage, error) {
	if spec.Description != "" || spec.Instructions != "" || spec.Model != "" || spec.ReasoningEffort != "" || spec.ToolPolicy != nil || spec.PermissionMode != "" || spec.SandboxMode != "" || len(spec.MCPServers)+len(spec.Skills)+len(spec.Hooks)+len(spec.Native) > 0 {
		return nil, errors.New("opencode: SourcePath defines the complete agent; do not combine it with inline agent fields")
	}
	doc, err := agentmarkdown.Read(spec.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("opencode: agent source: %w", err)
	}
	entry := doc.Fields
	for key := range entry {
		switch key {
		case "name", "description", "model", "variant", "prompt", "mode", "disable", "temperature", "top_p", "steps", "maxSteps", "hidden", "color", "options", "permission", "tools":
		default:
			return nil, fmt.Errorf("opencode: unsupported agent frontmatter field %q; provider options belong under options: %w", key, errors.ErrUnsupported)
		}
	}
	for _, key := range []string{"name", "description", "model", "variant", "prompt", "mode"} {
		if _, err := agentmarkdown.String(entry, key, false); err != nil {
			return nil, err
		}
	}
	mode, _ := agentmarkdown.String(entry, "mode", false)
	if mode != "" && mode != "subagent" {
		return nil, fmt.Errorf("opencode: source must declare a subagent: %w", errors.ErrUnsupported)
	}
	if raw, ok := entry["disable"]; ok && !bytes.Equal(raw, []byte("false")) {
		return nil, errors.New("opencode: source subagent must be enabled")
	}
	model, _ := agentmarkdown.String(entry, "model", false)
	if model != "" {
		provider, id, ok := strings.Cut(model, "/")
		if !ok || provider == "" || id == "" {
			return nil, errors.New("opencode: source agent model requires provider/model")
		}
	}
	variant, _ := agentmarkdown.String(entry, "variant", false)
	if variant != "" && model == "" {
		return nil, errors.New("opencode: source agent variant requires an explicit model")
	}
	// Native Markdown takes its prompt from the body; frontmatter prompt is
	// deliberately superseded. Host RuntimeName/Key owns the imported identity.
	delete(entry, "name")
	entry["prompt"], _ = json.Marshal(doc.Body)
	entry["mode"] = json.RawMessage(`"subagent"`)
	entry["disable"] = json.RawMessage(`false`)
	if _, ok := entry["description"]; !ok {
		entry["description"], _ = json.Marshal("SDK-managed profile agent " + spec.Key)
	}
	if legacy, ok := entry["maxSteps"]; ok {
		var legacyOptions map[string]any
		raw, _ := json.Marshal(map[string]json.RawMessage{"steps": legacy})
		if err := decodeNativeObject(raw, &legacyOptions); err != nil {
			return nil, err
		}
		if err := validateAgentNativeOptions(legacyOptions); err != nil {
			return nil, err
		}
		if _, set := entry["steps"]; !set {
			entry["steps"] = legacy
		}
		delete(entry, "maxSteps")
	}
	permission, err := sourcePermissions(entry["tools"], entry["permission"])
	if err != nil {
		return nil, err
	}
	delete(entry, "tools")
	if permission != nil {
		entry["permission"] = permission
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var options map[string]any
	if err := decodeNativeObject(raw, &options); err != nil {
		return nil, err
	}
	if err := validateAgentNativeOptions(options); err != nil {
		return nil, err
	}
	// Import is literal. Resolving templates here would depend on later process
	// environment/files and escape both capture and compatibility fingerprints.
	raw = bytes.ReplaceAll(raw, []byte("{env:"), []byte(`\u007benv:`))
	raw = bytes.ReplaceAll(raw, []byte("{file:"), []byte(`\u007bfile:`))
	return raw, nil
}

type orderedField struct {
	name  string
	value json.RawMessage
}

func orderedObject(raw json.RawMessage) ([]orderedField, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("opencode: native rule must be an object")
	}
	seen := map[string]bool{}
	var fields []orderedField
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok || name == "" || seen[name] {
			return nil, errors.New("opencode: native rule keys must be nonempty and unique")
		}
		seen[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields = append(fields, orderedField{name, value})
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("opencode: trailing native rule data")
	}
	return fields, nil
}

func orderedJSON(fields []orderedField) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, field := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(field.name)
		b.Write(key)
		b.WriteByte(':')
		b.Write(field.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func permissionAction(raw json.RawMessage) (string, error) {
	var action string
	if json.Unmarshal(raw, &action) != nil || (action != "ask" && action != "allow" && action != "deny") {
		return "", errors.New("opencode: permission action must be ask, allow or deny")
	}
	return action, nil
}

// ConfigAgent normalizes deprecated tools first, then Object.assigns explicit
// permission values. Updating a key keeps its original insertion position.
func sourcePermissions(tools, permission json.RawMessage) (json.RawMessage, error) {
	if tools == nil && permission == nil {
		return nil, nil
	}
	var fields []orderedField
	set := func(field orderedField) {
		for i := range fields {
			if fields[i].name == field.name {
				fields[i] = field
				return
			}
		}
		fields = append(fields, field)
	}
	if tools != nil {
		legacy, err := orderedObject(tools)
		if err != nil {
			return nil, err
		}
		for _, field := range legacy {
			action := `"deny"`
			switch string(field.value) {
			case "true":
				action = `"allow"`
			case "false":
			default:
				return nil, errors.New("opencode: source tools values must be booleans")
			}
			if field.name == "write" || field.name == "patch" {
				field.name = "edit"
			}
			set(orderedField{field.name, json.RawMessage(action)})
		}
	}
	if permission != nil {
		if len(permission) > 0 && permission[0] == '"' {
			if _, err := permissionAction(permission); err != nil {
				return nil, err
			}
			set(orderedField{"*", permission})
		} else {
			declared, err := orderedObject(permission)
			if err != nil {
				return nil, err
			}
			for _, field := range declared {
				set(field)
			}
		}
	}
	raw := orderedJSON(fields)
	if _, err := configPermissionRules(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func configPermissionRules(raw json.RawMessage) ([]permissionRule, error) {
	fields, err := orderedObject(raw)
	if err != nil {
		return nil, err
	}
	var rules []permissionRule
	for _, field := range fields {
		if len(field.value) > 0 && field.value[0] == '"' {
			action, err := permissionAction(field.value)
			if err != nil {
				return nil, err
			}
			rules = append(rules, permissionRule{field.name, "*", action})
			continue
		}
		switch field.name {
		case "todowrite", "question", "webfetch", "websearch", "doom_loop":
			return nil, errors.New("opencode: this permission requires a single action")
		}
		patterns, err := orderedObject(field.value)
		if err != nil {
			return nil, err
		}
		for _, pattern := range patterns {
			action, err := permissionAction(pattern.value)
			if err != nil {
				return nil, err
			}
			rules = append(rules, permissionRule{field.name, pattern.name, action})
		}
	}
	return rules, nil
}
