package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func permissionRules(value any) ([]permissionRule, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var rules []permissionRule
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&rules); err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if rule.Permission == "" || rule.Pattern == "" || (rule.Action != "ask" && rule.Action != "allow" && rule.Action != "deny") {
			return nil, errors.New("opencode: invalid resolved agent permission")
		}
	}
	return rules, nil
}

// Normalize only provably equivalent transformations: native last-match
// overrides, and commuting adjacent rules. Wildcard permission names with
// different actions retain their relative order. No wildcard subsumption is
// guessed, and permission patterns themselves remain exact.
func effectivePermissionRules(rules []permissionRule) []permissionRule {
	out := []permissionRule{{Permission: "*", Pattern: "*", Action: "ask"}}
	for _, rule := range rules {
		if rule.Permission == "*" && rule.Pattern == "*" {
			out = out[:0]
		}
		kept := out[:0]
		for _, prior := range out {
			if prior.Permission != rule.Permission || prior.Pattern != rule.Pattern {
				kept = append(kept, prior)
			}
		}
		out = append(kept, rule)
	}
	for changed := true; changed; {
		changed = false
		for i := 1; i < len(out); i++ {
			a, b := out[i-1], out[i]
			commute := a.Action == b.Action || (a.Permission != b.Permission && !strings.ContainsAny(a.Permission+b.Permission, "*?"))
			left, _ := json.Marshal(a)
			right, _ := json.Marshal(b)
			if commute && bytes.Compare(left, right) > 0 {
				out[i-1], out[i] = b, a
				changed = true
			}
		}
	}
	return out
}

// Render the official ordered ConfigPermission object. Flattened native rules
// can interleave wildcard permission groups; if JSON grouping cannot preserve
// their precedence, refuse that shape before delivering any prompt.
func gateAgentPermissions(value any, internalOutputGlob string) (json.RawMessage, []permissionRule, error) {
	rules, err := permissionRules(value)
	if err != nil {
		return nil, nil, err
	}
	for i := range rules {
		// Agent initialization unconditionally appends this native cache rule
		// unless explicitly denied. Preserve it; read/bash/etc still require
		// their own approval, including operations on cached tool output.
		if rules[i].Permission == "external_directory" && rules[i].Pattern == internalOutputGlob {
			continue
		}
		if rules[i].Action == "allow" {
			rules[i].Action = "ask"
		}
	}
	wanted := effectivePermissionRules(rules)
	groups := map[string][]permissionRule{}
	var order []string
	for _, rule := range wanted {
		if groups[rule.Permission] == nil {
			order = append(order, rule.Permission)
		}
		groups[rule.Permission] = append(groups[rule.Permission], rule)
	}
	var grouped []permissionRule
	var raw bytes.Buffer
	raw.WriteByte('{')
	for i, name := range order {
		if i > 0 {
			raw.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		raw.Write(key)
		raw.WriteByte(':')
		if len(groups[name]) == 1 && groups[name][0].Pattern == "*" {
			rule := groups[name][0]
			action, _ := json.Marshal(rule.Action)
			raw.Write(action)
			grouped = append(grouped, rule)
			continue
		}
		raw.WriteByte('{')
		for j, rule := range groups[name] {
			if j > 0 {
				raw.WriteByte(',')
			}
			pattern, _ := json.Marshal(rule.Pattern)
			action, _ := json.Marshal(rule.Action)
			raw.Write(pattern)
			raw.WriteByte(':')
			raw.Write(action)
			grouped = append(grouped, rule)
		}
		raw.WriteByte('}')
	}
	raw.WriteByte('}')
	if !reflect.DeepEqual(effectivePermissionRules(grouped), wanted) {
		return nil, nil, fmt.Errorf("opencode: native agent permission precedence cannot be represented by a config overlay: %w", errors.ErrUnsupported)
	}
	escaped := bytes.ReplaceAll(raw.Bytes(), []byte("{env:"), []byte(`\u007benv:`))
	escaped = bytes.ReplaceAll(escaped, []byte("{file:"), []byte(`\u007bfile:`))
	return escaped, wanted, nil
}

func normalizeAgentPermissions(state *nativeAgentState) error {
	for _, agent := range state.agents {
		rules, err := permissionRules(agent["permission"])
		if err != nil {
			return err
		}
		agent["permission"] = effectivePermissionRules(rules)
	}
	return nil
}
