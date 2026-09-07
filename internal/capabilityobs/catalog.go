// Package capabilityobs tracks normalized facts; it does not interpret any
// provider protocol or emit events. Parsers own dispatch ordering and aliases.
package capabilityobs

import (
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/capability"
)

type Entry struct {
	Kind        capability.Kind
	RuntimeName string
	Key         string
}
type catalogKey struct {
	kind capability.Kind
	name string
}
type Catalog struct{ names map[catalogKey]string }

var ErrInvalid = errors.New("capabilityobs: invalid value")
var ErrAmbiguous = errors.New("capabilityobs: ambiguous name")
var ErrUnknown = errors.New("capabilityobs: unknown name")

func ValidText(s string, limit int, required bool) bool {
	if (required && s == "") || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ValidKind(k capability.Kind) bool {
	return k == capability.Skill || k == capability.MCP || k == capability.Subagent
}
func NewCatalog(entries []Entry) (*Catalog, error) {
	c := &Catalog{names: map[catalogKey]string{}}
	for _, e := range entries {
		if !ValidKind(e.Kind) || !ValidText(e.RuntimeName, 2048, true) || !ValidText(e.Key, 512, true) {
			return nil, ErrInvalid
		}
		k := catalogKey{e.Kind, e.RuntimeName}
		old, exists := c.names[k]
		if !exists {
			c.names[k] = e.Key
		} else if old != e.Key {
			c.names[k] = ""
		}
	}
	return c, nil
}
func (c *Catalog) Lookup(kind capability.Kind, name string) (string, error) {
	if !ValidKind(kind) || !ValidText(name, 2048, true) {
		return "", ErrInvalid
	}
	if c == nil {
		return "", ErrUnknown
	}
	value, ok := c.names[catalogKey{kind, name}]
	if !ok {
		return "", ErrUnknown
	}
	if value == "" {
		return "", ErrAmbiguous
	}
	return value, nil
}
