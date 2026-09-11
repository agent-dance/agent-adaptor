package sessionrecorder

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
)

// strictJSON rejects data that encoding/json would otherwise silently repair
// or merge. Unknown event/envelope fields cannot disappear on replay.
func strictJSON(data []byte, out any) error {
	if !utf8.Valid(data) {
		return errors.New("sessionrecorder: invalid UTF-8")
	}
	if err := validateJSONStrings(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkJSON(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("sessionrecorder: trailing JSON value")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func walkJSON(dec *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("sessionrecorder: JSON nesting limit")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return errors.New("sessionrecorder: invalid object key")
			}
			if keys[key] {
				return errors.New("sessionrecorder: duplicate JSON key")
			}
			keys[key] = true
			if err := walkJSON(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkJSON(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("sessionrecorder: invalid JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

// Reject unpaired UTF-16 escapes before the standard decoder replaces them.
func validateJSONStrings(data []byte) error {
	inside := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inside = !inside
			continue
		}
		if !inside || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			break
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return errors.New("sessionrecorder: truncated Unicode escape")
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return errors.New("sessionrecorder: unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("sessionrecorder: unpaired Unicode surrogate")
			}
			i += 6
		} else if value >= 0xdc00 && value <= 0xdfff {
			return errors.New("sessionrecorder: unpaired Unicode surrogate")
		}
	}
	return nil
}

// requiredObject decodes once and retains the fields for nested validation.
// strictJSON has already checked the complete event's syntax and closed shape.
func requiredObject(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	for _, field := range fields {
		value := bytes.TrimSpace(values[field])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return nil, fmt.Errorf("sessionrecorder: required %s field missing or null", field)
		}
	}
	return values, nil
}
func validText(value string, min, max int, content bool) bool {
	if len(value) < min || len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(content && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}
func validUTC(at time.Time) bool { _, offset := at.Zone(); return !at.IsZero() && offset == 0 }
func validateParent(scope, parentScope, parentID, id string) error {
	for _, s := range []string{scope, parentScope, parentID} {
		if !validText(s, 0, 2048, false) {
			return errors.New("sessionrecorder: invalid parent coordinates")
		}
	}
	if (parentID == "" && parentScope != "") || (id != "" && parentID == id && scope == parentScope) {
		return errors.New("sessionrecorder: invalid parent reference")
	}
	return nil
}
func validateCapability(v capability.Invocation) error {
	invalid := errors.New("sessionrecorder: invalid capability invocation")
	if !validText(v.InvocationID, 1, 2048, false) || !validText(v.Ref.Key, 1, 512, false) || !validText(v.Ref.Operation, 1, 256, false) || !validUTC(v.OccurredAt) {
		return invalid
	}
	if err := validateParent(v.ScopeID, v.ParentScopeID, v.ParentToolCallID, ""); err != nil {
		return err
	}
	switch v.Ref.Kind {
	case capability.Skill:
		if v.Ref.Operation != "activate" {
			return invalid
		}
	case capability.Subagent:
		if v.Ref.Operation != "spawn" {
			return invalid
		}
	case capability.MCP:
	default:
		return invalid
	}
	switch v.Phase {
	case capability.Started:
		if v.Duration != nil || v.ErrorCode != "" {
			return invalid
		}
	case capability.Completed:
		if v.ErrorCode != "" {
			return invalid
		}
	case capability.Failed, capability.Cancelled, capability.Interrupted:
	default:
		return invalid
	}
	if v.Duration != nil && *v.Duration < 0 {
		return invalid
	}
	switch v.ErrorCode {
	case "", capability.ToolFailed, capability.RunCancelled, capability.RunInterrupted, capability.ProtocolError, capability.DelegationFailed:
	default:
		return invalid
	}
	switch v.Source {
	case capability.Provider:
		if v.Evidence != capability.ProviderProtocol && v.Evidence != capability.NativeInputAccepted {
			return invalid
		}
	case capability.Host:
		if v.Evidence != capability.HostLifecycle {
			return invalid
		}
	case capability.Relay:
		if v.Evidence != capability.Relayed {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
func validateTodo(v todo.Snapshot) error {
	invalid := errors.New("sessionrecorder: invalid todo snapshot")
	if v.Items == nil || len(v.Items) > 128 || v.Revision == 0 || !validUTC(v.OccurredAt) || (v.Source != todo.ToolResult && v.Source != todo.PlanUpdate) {
		return invalid
	}
	if err := validateParent(v.ScopeID, v.ParentScopeID, v.ParentToolCallID, ""); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, item := range v.Items {
		if !validText(item.ID, 1, 2048, false) || !validText(item.Content, 1, 4096, true) || seen[item.ID] {
			return invalid
		}
		seen[item.ID] = true
		switch item.Status {
		case todo.Pending, todo.InProgress, todo.Completed, todo.Cancelled:
		default:
			return invalid
		}
	}
	return nil
}
func validateSource(source *eventSourceMetaWire, depth int) error {
	if source == nil {
		return nil
	}
	if depth >= 8 {
		return errors.New("sessionrecorder: source depth exceeded")
	}
	for _, s := range []string{source.RunID, source.ThreadID, source.TurnID, source.ScopeID, source.ToolCallID, source.InvocationID, source.DelegationID} {
		if !validText(s, 0, 2048, false) {
			return errors.New("sessionrecorder: invalid source coordinate")
		}
	}
	return validateSource(source.Upstream, depth+1)
}

func validateEventSource(source *adaptor.EventSourceMeta) error {
	for depth := 0; source != nil; depth++ {
		if depth >= 8 {
			return errors.New("sessionrecorder: source depth exceeded")
		}
		for _, s := range []string{source.RunID, source.ThreadID, source.TurnID, source.ScopeID, source.ToolCallID, source.InvocationID, source.DelegationID} {
			if !validText(s, 0, 2048, false) {
				return errors.New("sessionrecorder: invalid source coordinate")
			}
		}
		source = source.Upstream
	}
	return nil
}
