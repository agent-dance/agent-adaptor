package toolruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
)

// outputProjection keeps the public Tool value separate from its MCP wire
// representation. A catalog fixes this once so list and call cannot disagree.
type outputProjection struct {
	schema  json.RawMessage
	wrapped bool
}

func projectOutput(schema json.RawMessage) (outputProjection, error) {
	var document map[string]json.RawMessage
	boolean := string(schema) == "true" || string(schema) == "false"
	if !boolean {
		if err := json.Unmarshal(schema, &document); err != nil || document == nil {
			return outputProjection{}, ErrInvalidCatalog
		}
		var kind string
		if json.Unmarshal(document["type"], &kind) == nil && kind == "object" {
			wire, err := projectObjectProperties(schema)
			return outputProjection{schema: wire}, err
		}
	}

	digest := sha256.Sum256(schema)
	key := hex.EncodeToString(digest[:])
	var valueSchema json.RawMessage
	if boolean {
		// Older MCP clients require each properties value to be a schema
		// object too. Keep the boolean constraint inside an applicator.
		valueSchema = json.RawMessage(`{"allOf":[` + string(schema) + `]}`)
	} else {
		// Give the original root its own resource boundary. Its fragment
		// references then still target that root, not the enclosing object.
		// Only the root ID changes; schema-looking annotation/instance data,
		// nested resources, anchors and all references remain untouched.
		// The hash is in the host so root-relative and parent paths cannot
		// erase its namespace. An explicit //authority still belongs to the
		// author, and resolves as an absolute HTTPS resource identity.
		base, _ := url.Parse("https://" + key[:32] + "." + key[32:] + ".tool-output.agent-adaptor.invalid/schema.json")
		var identity string
		if raw, exists := document["$id"]; exists {
			if json.Unmarshal(raw, &identity) != nil {
				return outputProjection{}, ErrInvalidCatalog
			}
		}
		id, err := url.Parse(identity)
		if err != nil {
			return outputProjection{}, ErrInvalidCatalog
		}
		if !id.IsAbs() {
			resolved := base.ResolveReference(id)
			resolved.Fragment = ""
			resolved.RawFragment = ""
			document["$id"], _ = json.Marshal(resolved.String())
		}
		valueSchema, err = json.Marshal(document)
		if err != nil {
			return outputProjection{}, ErrInvalidCatalog
		}
	}
	properties, _ := json.Marshal(map[string]json.RawMessage{"result": valueSchema})
	identity, _ := json.Marshal("https://" + key[:32] + "." + key[32:] + ".tool-envelope.agent-adaptor.invalid/schema.json")
	wire, err := json.Marshal(map[string]json.RawMessage{
		"$schema":              json.RawMessage(`"https://json-schema.org/draft/2020-12/schema"`),
		"$id":                  identity,
		"type":                 json.RawMessage(`"object"`),
		"properties":           properties,
		"required":             json.RawMessage(`["result"]`),
		"additionalProperties": json.RawMessage(`false`),
	})
	if err != nil {
		return outputProjection{}, ErrInvalidCatalog
	}
	return outputProjection{schema: wire, wrapped: true}, nil
}

func (p outputProjection) structured(output json.RawMessage) json.RawMessage {
	if !p.wrapped {
		return slices.Clone(output)
	}
	// The existing output limit applies to the validated public value. This
	// projection adds exactly 11 bytes and never decodes/re-encodes its value.
	wire := make(json.RawMessage, 0, len(output)+11)
	wire = append(wire, `{"result":`...)
	wire = append(wire, output...)
	return append(wire, '}')
}

// projectObjectProperties adapts only direct properties at a known wire schema
// root. Older MCP clients require these values to be objects; nested schemas and
// schema-looking instance/annotation data are deliberately left untouched.
func projectObjectProperties(schema json.RawMessage) (json.RawMessage, error) {
	var document map[string]json.RawMessage
	if json.Unmarshal(schema, &document) != nil || document == nil {
		return nil, ErrInvalidCatalog
	}
	var properties map[string]json.RawMessage
	raw, exists := document["properties"]
	if !exists {
		return slices.Clone(schema), nil
	}
	if json.Unmarshal(raw, &properties) != nil {
		return nil, ErrInvalidCatalog
	}
	changed := false
	for name, constraint := range properties {
		if string(constraint) == "true" || string(constraint) == "false" {
			properties[name] = json.RawMessage(`{"allOf":[` + string(constraint) + `]}`)
			changed = true
		}
	}
	if !changed {
		return slices.Clone(schema), nil
	}
	document["properties"], _ = json.Marshal(properties)
	wire, err := json.Marshal(document)
	if err != nil {
		return nil, ErrInvalidCatalog
	}
	return wire, nil
}
