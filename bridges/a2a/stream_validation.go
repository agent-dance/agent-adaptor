package a2a

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	errAdapterPayload = errors.New("invalid_payload")
	errAdapterSize    = errors.New("payload_too_large")
	errAdapterKind    = errors.New("unsupported_event_kind")
	errAdapterDepth   = errors.New("relay_depth_exceeded")
)

// RawMessage retains the exact wire bytes. Structured input has already lost
// duplicate-key evidence, but must not let Marshal repair invalid UTF-8.
func decodeAdapterStreamEventWire(data any) (AdapterStreamEventV1, bool, error) {
	var raw []byte
	var inputErr error
	if original, ok := data.(json.RawMessage); ok {
		raw = original
	} else {
		inputErr = validStructuredJSON(reflect.ValueOf(data), 0)
		var err error
		raw, err = json.Marshal(data)
		if err != nil {
			switch value := data.(type) {
			case map[string]any:
				if value["schema"] == AdapterStreamSchemaV1 {
					return AdapterStreamEventV1{}, true, errAdapterPayload
				}
			case AdapterStreamEnvelopeV1:
				if value.Schema == AdapterStreamSchemaV1 {
					return AdapterStreamEventV1{}, true, errAdapterPayload
				}
			case *AdapterStreamEnvelopeV1:
				if value != nil && value.Schema == AdapterStreamSchemaV1 {
					return AdapterStreamEventV1{}, true, errAdapterPayload
				}
			}
			return AdapterStreamEventV1{}, false, nil
		}
	}
	if !adapterSchemaRecognized(raw) {
		return AdapterStreamEventV1{}, false, nil
	}
	if len(raw) > adapterStreamMaxBytes {
		return AdapterStreamEventV1{}, true, errAdapterSize
	}
	if inputErr != nil {
		return AdapterStreamEventV1{}, true, errAdapterPayload
	}
	var envelope AdapterStreamEnvelopeV1
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return AdapterStreamEventV1{}, true, errAdapterPayload
	}
	if envelope.Schema != AdapterStreamSchemaV1 {
		return AdapterStreamEventV1{}, true, errAdapterPayload
	}
	if !supportedAdapterStreamKind(envelope.Event.Kind) {
		return AdapterStreamEventV1{}, true, errAdapterKind
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		return AdapterStreamEventV1{}, true, errAdapterPayload
	}
	e, _ := shape["event"].(map[string]any)
	strict := observationKind(envelope.Event.Kind) || hasNewWireFields(e)
	if strict {
		value, err := strictJSON(raw)
		if err != nil {
			return AdapterStreamEventV1{}, true, err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return AdapterStreamEventV1{}, true, errAdapterPayload
		}
		if err := validateWireShape(object, envelope.Event); err != nil {
			return AdapterStreamEventV1{}, true, err
		}
	}
	if err := validateAdapterEvent(envelope.Event); err != nil {
		return AdapterStreamEventV1{}, true, err
	}
	return envelope.Event, true, nil
}

// Recognize only a top-level schema field. Returning as soon as it is read
// makes a truncated event after a valid header an observable malformed payload.
func adapterSchemaRecognized(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return false
		}
		if key == "schema" {
			var schema string
			if d.Decode(&schema) != nil {
				return false
			}
			return schema == AdapterStreamSchemaV1
		}
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return false
		}
	}
	return false
}

func observationKind(kind string) bool {
	return kind == "capability.invocation" || kind == "todo.updated"
}
func hasNewWireFields(e map[string]any) bool {
	for _, key := range []string{"capability", "todo", "scope_id", "parent_scope_id", "parent_tool_call_id"} {
		if _, ok := e[key]; ok {
			return true
		}
	}
	meta, _ := e["meta"].(map[string]any)
	source, _ := meta["source"].(map[string]any)
	for _, key := range []string{"scope_id", "tool_call_id", "invocation_id", "delegation_id", "upstream"} {
		if _, ok := source[key]; ok {
			return true
		}
	}
	return false
}

func validStructuredJSON(v reflect.Value, depth int) error {
	if depth > 64 {
		return errAdapterPayload
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validStructuredJSON(v.Elem(), depth+1)
		}
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return errAdapterPayload
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return errAdapterPayload
		}
		it := v.MapRange()
		for it.Next() {
			if validStructuredJSON(it.Key(), depth+1) != nil || validStructuredJSON(it.Value(), depth+1) != nil {
				return errAdapterPayload
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if validStructuredJSON(v.Index(i), depth+1) != nil {
				return errAdapterPayload
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && validStructuredJSON(v.Field(i), depth+1) != nil {
				return errAdapterPayload
			}
		}
	case reflect.Float32, reflect.Float64:
		n := v.Float()
		if math.IsNaN(n) || math.IsInf(n, 0) || (math.Trunc(n) == n && math.Abs(n) > float64(maxA2AJSONInteger)) {
			return errAdapterPayload
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n < -int64(maxA2AJSONInteger) || n > int64(maxA2AJSONInteger) {
			return errAdapterPayload
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() > maxA2AJSONInteger {
			return errAdapterPayload
		}
	case reflect.Bool:
	default:
		return errAdapterPayload
	}
	return nil
}

// Strict parsing supplements encoding/json's intentional duplicate-key and
// replacement-rune behavior. It applies to the new closed security surface.
func strictJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) || !validJSONSurrogates(raw) {
		return nil, errAdapterPayload
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := strictJSONValue(d, 0)
	if err != nil {
		return nil, errAdapterPayload
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errAdapterPayload
	}
	return value, nil
}
func strictJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errAdapterPayload
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			object := map[string]any{}
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := token.(string)
				if !ok {
					return nil, errAdapterPayload
				}
				if _, ok := object[key]; ok {
					return nil, errAdapterPayload
				}
				v, err := strictJSONValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errAdapterPayload
			}
			return object, nil
		case '[':
			array := []any{}
			for d.More() {
				v, err := strictJSONValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errAdapterPayload
			}
			return array, nil
		default:
			return nil, errAdapterPayload
		}
	}
	return token, nil
}
func validJSONSurrogates(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}

// required and optional are exact, case-sensitive JSON keys. All listed
// values, including optional ones when present, must be non-null.
func closedObject(value any, required, optional string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errAdapterPayload
	}
	allowed := map[string]bool{}
	for _, key := range strings.Fields(required) {
		allowed[key] = true
		if object[key] == nil {
			return nil, errAdapterPayload
		}
	}
	for _, key := range strings.Fields(optional) {
		allowed[key] = true
	}
	for key, value := range object {
		if !allowed[key] || value == nil {
			return nil, errAdapterPayload
		}
	}
	return object, nil
}
func validateWireShape(object map[string]any, event AdapterStreamEventV1) error {
	e, _ := object["event"].(map[string]any)
	if observationKind(event.Kind) {
		if _, err := closedObject(object, "schema event", ""); err != nil {
			return err
		}
		field := "capability"
		if event.Kind == "todo.updated" {
			field = "todo"
		}
		if _, err := closedObject(e, "kind meta "+field, "run_id sequence timestamp thread_id turn_id"); err != nil {
			return err
		}
		if seq, ok := e["sequence"]; ok {
			n, valid := seq.(json.Number)
			if !valid || n.String() == "0" {
				return errAdapterPayload
			}
		}
		meta, err := closedObject(e["meta"], "run_id sequence time", "thread_key turn_id source")
		if err != nil {
			return err
		}
		for _, pair := range [][2]string{{"run_id", "run_id"}, {"turn_id", "turn_id"}} {
			if v, ok := e[pair[0]]; ok {
				expected, _ := meta[pair[1]].(string)
				if v != expected {
					return errAdapterPayload
				}
			}
		}
		if v, ok := e["thread_id"]; ok {
			if meta["thread_key"] == nil || meta["thread_key"] == "" || v != meta["thread_key"] {
				return errAdapterPayload
			}
		}
		if v, ok := e["timestamp"]; ok {
			actual, ok := v.(string)
			if !ok || !wireTime(actual, true) {
				return errAdapterPayload
			}
		}
		if _, ok := meta["source"]; ok {
			if err := validateSourceShape(meta["source"], 0); err != nil {
				return err
			}
		}
		if field == "capability" {
			if _, err := closedObject(e[field], "invocation_id kind key operation phase evidence source occurred_at", "scope_id parent_scope_id parent_tool_call_id duration_ns error_code"); err != nil {
				return err
			}
		} else {
			snap, err := closedObject(e[field], "items source revision occurred_at", "scope_id parent_scope_id parent_tool_call_id")
			if err != nil {
				return err
			}
			items, ok := snap["items"].([]any)
			if !ok {
				return errAdapterPayload
			}
			for _, item := range items {
				if _, err := closedObject(item, "id content status synthetic_id", ""); err != nil {
					return err
				}
			}
		}
	} else {
		for _, key := range []string{"capability", "todo"} {
			if _, ok := e[key]; ok {
				return errAdapterPayload
			}
		}
		for _, key := range []string{"scope_id", "parent_scope_id", "parent_tool_call_id"} {
			if v, ok := e[key]; ok {
				if !strings.HasPrefix(event.Kind, "tool_call.") || !wireText(event.ToolCallID, true, 2048, false) {
					return errAdapterPayload
				}
				if _, ok := v.(string); !ok {
					return errAdapterPayload
				}
			}
		}
		meta, _ := e["meta"].(map[string]any)
		if source, ok := meta["source"]; ok {
			if err := validateSourceShape(source, 0); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateSourceShape(value any, depth int) error {
	if depth >= 8 {
		return errAdapterDepth
	}
	s, err := closedObject(value, "", "run_id thread_id turn_id sequence timestamp scope_id tool_call_id invocation_id delegation_id upstream")
	if err != nil {
		return err
	}
	if next, ok := s["upstream"]; ok {
		return validateSourceShape(next, depth+1)
	}
	return nil
}

func wireText(value string, required bool, limit int, content bool) bool {
	if (required && value == "") || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(content && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}
func wireTime(value string, required bool) bool {
	if value == "" {
		return !required
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999
}
func wireParents(scope, parentScope, parentID, ownID string) bool {
	for _, v := range []string{scope, parentScope, parentID} {
		if !wireText(v, false, 2048, false) {
			return false
		}
	}
	return !(parentID == "" && parentScope != "") && !(ownID != "" && parentID == ownID && scope == parentScope)
}
func enum(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}
func validateWireCoordinates(e AdapterStreamEventV1) error {
	for _, v := range []string{e.RunID, e.ThreadID, e.TurnID} {
		if !wireText(v, false, 2048, false) {
			return errAdapterPayload
		}
	}
	if e.Sequence > maxA2AJSONInteger || !wireTime(e.Timestamp, false) {
		return errAdapterPayload
	}
	if m := e.Meta; m != nil {
		for _, v := range []string{m.RunID, m.ThreadKey, m.TurnID} {
			if !wireText(v, false, 2048, false) {
				return errAdapterPayload
			}
		}
		if m.Sequence > maxA2AJSONInteger || !wireTime(m.Time, false) {
			return errAdapterPayload
		}
		return validateSource(m.Source)
	}
	return nil
}

func validateAdapterEvent(e AdapterStreamEventV1) error {
	if e.Sequence > maxA2AJSONInteger || (e.Meta != nil && e.Meta.Sequence > maxA2AJSONInteger) {
		return errAdapterPayload
	}
	if !supportedAdapterStreamKind(e.Kind) {
		return errAdapterKind
	}
	if !wireTime(e.Timestamp, false) {
		return errAdapterPayload
	}
	if e.Meta != nil {
		if !wireTime(e.Meta.Time, false) {
			return errAdapterPayload
		}
		if err := validateSource(e.Meta.Source); err != nil {
			return err
		}
	}
	if e.ScopeID != "" || e.ParentScopeID != "" || e.ParentToolCallID != "" {
		if !strings.HasPrefix(e.Kind, "tool_call.") || !wireText(e.ToolCallID, true, 2048, false) || !wireParents(e.ScopeID, e.ParentScopeID, e.ParentToolCallID, e.ToolCallID) {
			return errAdapterPayload
		}
	}
	if !observationKind(e.Kind) {
		if e.Capability != nil || e.Todo != nil {
			return errAdapterPayload
		}
		return nil
	}
	if e.ScopeID != "" || e.ParentScopeID != "" || e.ParentToolCallID != "" || e.ToolCallID != "" || e.Name != "" || e.Delta != "" || e.Args != nil || e.Result != nil || e.HITL != nil || e.Raw != nil || e.Role != "" || e.MessageID != "" {
		return errAdapterPayload
	}
	m := e.Meta
	if m == nil || !wireText(m.RunID, true, 2048, false) || m.Sequence == 0 || m.Sequence > maxA2AJSONInteger || !wireTime(m.Time, true) {
		return errAdapterPayload
	}
	for _, v := range []string{m.ThreadKey, m.TurnID, e.RunID, e.ThreadID, e.TurnID} {
		if !wireText(v, false, 2048, false) {
			return errAdapterPayload
		}
	}
	if (e.RunID != "" && e.RunID != m.RunID) || (e.Sequence != 0 && e.Sequence != m.Sequence) || (e.TurnID != "" && e.TurnID != m.TurnID) || (e.ThreadID != "" && (m.ThreadKey == "" || e.ThreadID != m.ThreadKey)) {
		return errAdapterPayload
	}
	if e.Timestamp != "" {
		a, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		b, _ := time.Parse(time.RFC3339Nano, m.Time)
		if !a.Equal(b) {
			return errAdapterPayload
		}
	}
	if e.Kind == "capability.invocation" {
		if e.Todo != nil {
			return errAdapterPayload
		}
		return validateCapability(e.Capability)
	}
	if e.Capability != nil {
		return errAdapterPayload
	}
	return validateTodo(e.Todo)
}
func validateSource(source *AdapterEventSourceMetaV1) error {
	seen := map[*AdapterEventSourceMetaV1]bool{}
	for s := source; s != nil; s = s.Upstream {
		if seen[s] || len(seen) >= 8 {
			return errAdapterDepth
		}
		seen[s] = true
		for _, v := range []string{s.RunID, s.ThreadID, s.TurnID, s.ScopeID, s.ToolCallID, s.InvocationID, s.DelegationID} {
			if !wireText(v, false, 2048, false) {
				return errAdapterPayload
			}
		}
		if s.Sequence > maxA2AJSONInteger || !wireTime(s.Timestamp, false) {
			return errAdapterPayload
		}
	}
	return nil
}
func validateCapability(c *AdapterCapabilityInvocationV1) error {
	if c == nil || !wireText(c.InvocationID, true, 2048, false) || !wireText(c.Key, true, 512, false) || !wireText(c.Operation, true, 256, false) || !wireParents(c.ScopeID, c.ParentScopeID, c.ParentToolCallID, "") || !wireTime(c.OccurredAt, true) {
		return errAdapterPayload
	}
	if !enum(c.Kind, "skill", "mcp", "subagent") || !enum(c.Phase, "started", "completed", "failed", "cancelled", "interrupted") || !enum(c.ErrorCode, "", "tool_failed", "run_cancelled", "run_interrupted", "protocol_error", "delegation_failed") {
		return errAdapterPayload
	}
	if (c.Kind == "skill" && c.Operation != "activate") || (c.Kind == "subagent" && c.Operation != "spawn") {
		return errAdapterPayload
	}
	if !((c.Source == "provider" && enum(c.Evidence, "provider_protocol", "native_input_accepted")) || (c.Source == "host" && c.Evidence == "host_lifecycle") || (c.Source == "relay" && c.Evidence == "relayed")) {
		return errAdapterPayload
	}
	if c.DurationNS != nil && (*c.DurationNS < 0 || uint64(*c.DurationNS) > maxA2AJSONInteger) {
		return errAdapterPayload
	}
	if c.Phase == "started" && (c.DurationNS != nil || c.ErrorCode != "") || c.Phase == "completed" && c.ErrorCode != "" {
		return errAdapterPayload
	}
	return nil
}
func validateTodo(s *AdapterTodoSnapshotV1) error {
	if s == nil || s.Items == nil || len(s.Items) > 128 || s.Revision == 0 || s.Revision > maxA2AJSONInteger || !enum(s.Source, "tool_result", "plan_update") || !wireParents(s.ScopeID, s.ParentScopeID, s.ParentToolCallID, "") || !wireTime(s.OccurredAt, true) {
		return errAdapterPayload
	}
	ids := map[string]bool{}
	for _, item := range s.Items {
		if !wireText(item.ID, true, 2048, false) || ids[item.ID] || !wireText(item.Content, true, 4096, true) || !enum(item.Status, "pending", "in_progress", "completed", "cancelled") {
			return errAdapterPayload
		}
		ids[item.ID] = true
	}
	return nil
}
