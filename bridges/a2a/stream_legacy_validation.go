package a2a

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

var parentWireFields = []string{"scope_id", "parent_scope_id", "parent_tool_call_id"}
var sourceWireFields = []string{"scope_id", "tool_call_id", "invocation_id", "delegation_id", "upstream"}

// The published legacy DTO accepted any parseable optional timestamp, including
// time.Time's zero value. Observation timestamps use the stricter wireTime.
func legacyWireTime(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
func typedNewSource(s *AdapterEventSourceMetaV1) bool {
	return s != nil && (s.ScopeID != "" || s.ToolCallID != "" || s.InvocationID != "" || s.DelegationID != "" || s.Upstream != nil)
}

// Only added fields are inspected in an existing kind's structured input. Tool
// args, diagnostic maps and text keep their pre-extension JSON behavior.
func validLegacyStructuredFields(data any) error {
	event := structuredField(reflect.ValueOf(data), "event")
	for _, key := range parentWireFields {
		if err := validStructuredJSON(structuredField(event, key), 0); err != nil {
			return err
		}
	}
	source := structuredField(structuredField(event, "meta"), "source")
	for _, key := range sourceWireFields {
		field := structuredField(source, key)
		if field.IsValid() && !field.IsZero() {
			return validStructuredJSON(source, 0)
		}
	}
	return nil
}
func structuredField(value reflect.Value, name string) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}
		}
		iter := value.MapRange()
		for iter.Next() {
			if strings.EqualFold(iter.Key().String(), name) {
				return iter.Value()
			}
		}
	case reflect.Struct:
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "" {
				tag = field.Name
			}
			if strings.EqualFold(tag, name) {
				return value.Field(i)
			}
		}
	}
	return reflect.Value{}
}

// Read selected object members without applying a new parser to unrelated
// legacy payloads. Duplicate selected keys are still rejected. RawMessage
// preserves the exact bytes for the strict parser of each added field.
func selectedWireFields(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errAdapterPayload
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errAdapterPayload
		}
		key, ok := token.(string)
		if !ok {
			return nil, errAdapterPayload
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errAdapterPayload
		}
		for _, name := range names {
			if strings.EqualFold(key, name) {
				if _, duplicate := out[name]; duplicate {
					return nil, errAdapterPayload
				}
				out[name] = value
				break
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errAdapterPayload
	}
	return out, nil
}
func legacySecurityShape(raw []byte) (map[string]any, bool, error) {
	envelope, err := selectedWireFields(raw, "event")
	if err != nil {
		return nil, false, err
	}
	names := append([]string{"capability", "todo", "meta"}, parentWireFields...)
	fields, err := selectedWireFields(envelope["event"], names...)
	if err != nil {
		return nil, false, err
	}
	event := map[string]any{}
	for _, key := range append([]string{"capability", "todo"}, parentWireFields...) {
		if bytes, present := fields[key]; present {
			value, err := strictJSON(bytes)
			if err != nil {
				return nil, false, err
			}
			event[key] = value
		}
	}
	newSource := false
	if meta, present := fields["meta"]; present && string(meta) != "null" {
		fields, err := selectedWireFields(meta, "source")
		if err != nil {
			return nil, false, err
		}
		if source, present := fields["source"]; present && string(source) != "null" {
			added, err := selectedWireFields(source, sourceWireFields...)
			if err != nil {
				return nil, false, err
			}
			if len(added) > 0 {
				value, err := strictJSON(source)
				if err != nil {
					return nil, false, err
				}
				event["meta"] = map[string]any{"source": value}
				newSource = true
			}
		}
	}
	return map[string]any{"event": event}, newSource, nil
}
