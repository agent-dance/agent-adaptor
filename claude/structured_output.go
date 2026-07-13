package claude

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func prepareClaudeJSONSchema(raw json.RawMessage) (json.RawMessage, error) {
	// 1. Decode without converting schema numbers through float64.
	var root any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "parse Claude JSON Schema", Cause: err}
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude JSON Schema must be an object"}
	}

	// 2. Inline only local references found in schema-bearing keyword positions.
	prepared, err := inlineClaudeLocalReferences(root, root, map[string]bool{}, true)
	if err != nil {
		return nil, err
	}
	object, ok := prepared.(map[string]any)
	if !ok {
		return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude JSON Schema root reference must resolve to an object"}
	}
	// 3. Remove root metadata and definitions after every reachable local ref is expanded.
	delete(object, "$schema")
	delete(object, "$id")
	delete(object, "$defs")
	delete(object, "definitions")
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "marshal Claude JSON Schema", Cause: err}
	}
	return encoded, nil
}

func inlineClaudeLocalReferences(value, root any, active map[string]bool, rootLevel bool) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if !rootLevel {
			if _, ok := typed["$id"].(string); ok {
				return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude native structured output does not support nested $id reference scopes"}
			}
		}
		if ref, ok := typed["$ref"].(string); ok && strings.HasPrefix(ref, "#") {
			if ref == "#" {
				return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude native structured output does not support recursive local reference #"}
			}
			if !strings.HasPrefix(ref, "#/") {
				return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude native structured output does not support local anchor reference " + ref}
			}
			if active[ref] {
				return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "Claude native structured output does not support recursive local reference " + ref}
			}
			target, ok := resolveClaudeJSONPointer(root, ref)
			if !ok {
				return nil, &agentadaptor.InvalidOutputSchemaError{Reason: "resolve Claude JSON Schema reference " + ref}
			}
			active[ref] = true
			expanded, err := inlineClaudeLocalReferences(target, root, active, false)
			delete(active, ref)
			if err != nil {
				return nil, err
			}
			if len(typed) == 1 {
				return expanded, nil
			}
			siblings := make(map[string]any, len(typed)-1)
			for key, child := range typed {
				if key == "$ref" {
					continue
				}
				next, err := inlineClaudeSchemaKeyword(key, child, root, active)
				if err != nil {
					return nil, err
				}
				siblings[key] = next
			}
			return map[string]any{"allOf": []any{expanded, siblings}}, nil
		}

		out := make(map[string]any, len(typed))
		for key, child := range typed {
			next, err := inlineClaudeSchemaKeyword(key, child, root, active)
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, schema := range typed {
			next, err := inlineClaudeLocalReferences(schema, root, active, false)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	default:
		return value, nil
	}
}

func inlineClaudeSchemaKeyword(key string, value, root any, active map[string]bool) (any, error) {
	switch key {
	case "$defs", "definitions", "properties", "patternProperties", "dependentSchemas":
		object, ok := value.(map[string]any)
		if !ok {
			return value, nil
		}
		out := make(map[string]any, len(object))
		for name, schema := range object {
			next, err := inlineClaudeLocalReferences(schema, root, active, false)
			if err != nil {
				return nil, err
			}
			out[name] = next
		}
		return out, nil
	case "allOf", "anyOf", "oneOf", "prefixItems":
		items, ok := value.([]any)
		if !ok {
			return value, nil
		}
		out := make([]any, len(items))
		for i, schema := range items {
			next, err := inlineClaudeLocalReferences(schema, root, active, false)
			if err != nil {
				return nil, err
			}
			out[i] = next
		}
		return out, nil
	case "not", "if", "then", "else", "contains", "contentSchema", "propertyNames", "additionalProperties", "unevaluatedProperties", "items", "additionalItems", "unevaluatedItems":
		return inlineClaudeLocalReferences(value, root, active, false)
	case "dependencies":
		object, ok := value.(map[string]any)
		if !ok {
			return value, nil
		}
		out := make(map[string]any, len(object))
		for name, dependency := range object {
			if _, ok := dependency.(map[string]any); !ok {
				out[name] = dependency
				continue
			}
			next, err := inlineClaudeLocalReferences(dependency, root, active, false)
			if err != nil {
				return nil, err
			}
			out[name] = next
		}
		return out, nil
	default:
		return value, nil
	}
}

func resolveClaudeJSONPointer(root any, ref string) (any, bool) {
	current := root
	fragment, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil || !strings.HasPrefix(fragment, "/") {
		return nil, false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}
