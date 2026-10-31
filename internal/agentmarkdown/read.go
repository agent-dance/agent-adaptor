// Package agentmarkdown reads bounded Markdown and JSON-compatible YAML.
// Provider field interpretation belongs to the importing Driver.
package agentmarkdown

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"
)

const maxSize = 4 << 20

type Document struct {
	Fields            map[string]json.RawMessage
	Body, Fingerprint string
}

// Read follows an explicitly selected source symlink, but accepts only regular,
// nonempty UTF-8 files. The returned content is independent of future file edits.
func Read(path string) (Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Document{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return Document{}, errors.New("agent source must be a regular file of at most 4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return Document{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Document{}, err
	}
	if !opened.Mode().IsRegular() {
		return Document{}, errors.New("agent source is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return Document{}, err
	}
	return parse(raw)
}

func parse(raw []byte) (Document, error) {
	if len(raw) == 0 || len(raw) > maxSize || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return Document{}, errors.New("agent source must contain nonempty UTF-8 text of at most 4 MiB without NUL")
	}
	sum := sha256.Sum256(raw)
	doc := Document{Fields: map[string]json.RawMessage{}, Fingerprint: "sha256:" + hex.EncodeToString(sum[:])}
	content := strings.ReplaceAll(strings.TrimPrefix(string(raw), "\ufeff"), "\r\n", "\n")
	doc.Body = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---\n") {
		return doc, nil
	}
	lines := strings.Split(content[4:], "\n")
	end := -1
	for i, line := range lines {
		if line == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Document{}, errors.New("agent source has unterminated YAML frontmatter")
	}
	doc.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	header := strings.Join(lines[:end], "\n")
	if strings.TrimSpace(header) == "" {
		return doc, nil
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(header))
	if err := decoder.Decode(&node); err != nil {
		return Document{}, errors.New("agent source has invalid YAML frontmatter")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, errors.New("agent source requires one YAML mapping")
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return Document{}, errors.New("agent source frontmatter must be a mapping")
	}
	encoded, err := nodeJSON(node.Content[0], 0)
	if err != nil {
		return Document{}, err
	}
	if err := json.Unmarshal(encoded, &doc.Fields); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Keep mapping order: native permission objects may use last-match semantics.
// Reject aliases, merge keys and non-JSON tags rather than expanding ambiguous
// or unbounded graphs. Duplicate keys never become silent policy overrides.
func nodeJSON(n *yaml.Node, depth int) ([]byte, error) {
	if depth > 64 {
		return nil, errors.New("agent frontmatter nesting exceeds 64 levels")
	}
	var b bytes.Buffer
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return nil, errors.New("unsupported YAML mapping tag")
		}
		seen := map[string]bool{}
		b.WriteByte('{')
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || seen[k.Value] || k.Value == "<<" {
				return nil, errors.New("agent frontmatter requires unique string keys without YAML merges")
			}
			seen[k.Value] = true
			if i > 0 {
				b.WriteByte(',')
			}
			key, _ := json.Marshal(k.Value)
			b.Write(key)
			b.WriteByte(':')
			value, err := nodeJSON(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			b.Write(value)
		}
		b.WriteByte('}')
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return nil, errors.New("unsupported YAML sequence tag")
		}
		b.WriteByte('[')
		for i, child := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			value, err := nodeJSON(child, depth+1)
			if err != nil {
				return nil, err
			}
			b.Write(value)
		}
		b.WriteByte(']')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null":
		default:
			return nil, errors.New("agent frontmatter has a non-JSON YAML scalar")
		}
		var value any
		if err := n.Decode(&value); err != nil {
			return nil, errors.New("invalid agent frontmatter scalar")
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, errors.New("agent frontmatter scalar is not finite JSON")
		}
		return raw, nil
	default:
		return nil, errors.New("agent frontmatter aliases are unsupported")
	}
	return b.Bytes(), nil
}

func String(fields map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := fields[key]
	if !ok && !required {
		return "", nil
	}
	var value string
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", fmt.Errorf("agent frontmatter %s must be a string", key)
	}
	return value, nil
}
