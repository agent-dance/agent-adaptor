package aguisession

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// Message is a schema-shaped AG-UI Message. Optional fields are absent rather
// than null (notably content on tool-only assistant messages).
type Message map[string]any

// Fold applies one projected event using @ag-ui/client 1.0.3's default apply
// reducer (Se), tool-parent helper xe, and metadata helper L. It mutates messages;
// callers must own the slice. Tool failure metadata additionally sets error for
// assistant-ui's fromAgUiMessages. Subscriber-specific mutations are not replayed.
func Fold(messages []Message, event events.Event) []Message {
	raw, err := event.ToJSON()
	if err != nil {
		return messages
	}
	var e map[string]any
	if json.Unmarshal(raw, &e) != nil {
		return messages
	}
	id := str(e, "messageId")
	index := func(id string) int {
		for i, m := range messages {
			if str(m, "id") == id {
				return i
			}
		}
		return -1
	}
	findTool := func(id string) (Message, map[string]any) {
		for _, m := range messages {
			if str(m, "role") != "assistant" {
				continue
			}
			calls, _ := m["toolCalls"].([]any)
			for _, c := range calls {
				call, _ := c.(map[string]any)
				if str(call, "id") == id {
					return m, call
				}
			}
		}
		return nil, nil
	}
	merge := func(m map[string]any) {
		if metadata, ok := e["metadata"].(map[string]any); ok {
			old, _ := m["metadata"].(map[string]any)
			m["metadata"] = mergeMetadata(old, metadata)
		}
	}
	attribution := func(m map[string]any) {
		if sub := str(e, "subagentRunId"); sub != "" {
			m["subagentRunId"] = sub
		}
	}
	switch str(e, "type") {
	case "RUN_STARTED":
		input, _ := e["input"].(map[string]any)
		ms, _ := input["messages"].([]any)
		for _, value := range ms {
			m, ok := value.(map[string]any)
			if !ok || index(str(m, "id")) >= 0 {
				continue
			}
			if m["subagentRunId"] == nil {
				delete(m, "subagentRunId")
			}
			messages = append(messages, Message(m))
		}
	case "TEXT_MESSAGE_START", "REASONING_MESSAGE_START":
		i := index(id)
		if i >= 0 && str(messages[i], "role") == "activity" {
			break
		}
		if i < 0 {
			role := str(e, "role")
			if str(e, "type") == "REASONING_MESSAGE_START" {
				role = "reasoning"
			}
			if role == "" {
				role = "assistant"
			}
			m := Message{"id": id, "role": role, "content": ""}
			if name, ok := e["name"]; ok && str(e, "type") == "TEXT_MESSAGE_START" {
				m["name"] = name
			}
			attribution(m)
			messages = append(messages, m)
			i = len(messages) - 1
		}
		merge(messages[i])
	case "TEXT_MESSAGE_CONTENT", "REASONING_MESSAGE_CONTENT":
		if i := index(id); i >= 0 && str(messages[i], "role") != "activity" {
			messages[i]["content"] = str(messages[i], "content") + str(e, "delta")
			merge(messages[i])
		}
	case "TEXT_MESSAGE_END", "REASONING_MESSAGE_END":
		if i := index(id); i >= 0 && str(messages[i], "role") != "activity" {
			merge(messages[i])
		}
	case "TOOL_CALL_START":
		toolID := str(e, "toolCallId")
		_, call := findTool(toolID)
		if call != nil {
			call["function"].(map[string]any)["name"] = str(e, "toolCallName")
			merge(call)
			break
		}
		parent := str(e, "parentMessageId")
		i := index(parent)
		if parent == "" || i >= 0 && str(messages[i], "role") != "assistant" {
			parent = toolID
			i = -1
		}
		if i < 0 {
			m := Message{"id": parent, "role": "assistant", "toolCalls": []any{}}
			attribution(m)
			messages = append(messages, m)
			i = len(messages) - 1
		}
		call = map[string]any{"id": toolID, "type": "function", "function": map[string]any{"name": str(e, "toolCallName"), "arguments": ""}}
		merge(call)
		calls, _ := messages[i]["toolCalls"].([]any)
		messages[i]["toolCalls"] = append(calls, call)
	case "TOOL_CALL_ARGS", "TOOL_CALL_END":
		_, call := findTool(str(e, "toolCallId"))
		if call != nil {
			if str(e, "type") == "TOOL_CALL_ARGS" {
				f := call["function"].(map[string]any)
				f["arguments"] = str(f, "arguments") + str(e, "delta")
			}
			merge(call)
		}
	case "TOOL_CALL_RESULT":
		toolID := str(e, "toolCallId")
		// Corrections retain the old fields and accumulated metadata, but an
		// absent attribution explicitly returns ownership to the parent.
		i := -1
		m := Message{}
		for j, old := range messages {
			if str(old, "id") == id && str(old, "role") == "tool" && str(old, "toolCallId") == toolID {
				i = j
				for k, v := range old {
					m[k] = v
				}
				break
			}
		}
		m["id"], m["role"], m["toolCallId"], m["content"] = id, "tool", toolID, str(e, "content")
		delete(m, "subagentRunId")
		attribution(m)
		merge(m)
		// This is the sole deliberate addition to the client reducer. A result
		// correction also clears a previously derived failure when it succeeds.
		metadata, _ := m["metadata"].(map[string]any)
		status, _ := metadata["agentAdaptor"].(map[string]any)
		if _, observed := status["status"]; observed {
			delete(m, "error")
		}
		if str(status, "status") == "failed" || str(status, "status") == "cancelled" {
			message := str(status, "error")
			if message == "" {
				message = str(e, "content")
			}
			if message == "" {
				message = str(status, "status")
			}
			m["error"] = message
		}
		if i >= 0 {
			messages[i] = m
			break
		}
		parent, _ := findTool(toolID)
		at := len(messages)
		if parent != nil {
			at = index(str(parent, "id")) + 1
			for at < len(messages) && str(messages[at], "role") == "tool" {
				at++
			}
		}
		messages = append(messages, nil)
		copy(messages[at+1:], messages[at:])
		messages[at] = m
	case "ACTIVITY_SNAPSHOT":
		i := index(id)
		replace, ok := e["replace"].(bool)
		if !ok {
			replace = true
		}
		if i >= 0 && !replace {
			// The client merges metadata only into existing activities. A
			// collision with a text message is ignored entirely when !replace.
			if str(messages[i], "role") == "activity" {
				merge(messages[i])
			}
			break
		}
		m := Message{}
		if i >= 0 && str(messages[i], "role") == "activity" {
			for k, v := range messages[i] {
				m[k] = v
			}
		}
		m["id"], m["role"], m["activityType"], m["content"] = id, "activity", str(e, "activityType"), e["content"]
		delete(m, "subagentRunId")
		attribution(m)
		merge(m)
		if i < 0 {
			messages = append(messages, m)
		} else {
			messages[i] = m
		}
	case "ACTIVITY_DELTA":
		i := index(id)
		if i < 0 || str(messages[i], "role") != "activity" {
			break
		}
		merge(messages[i])
		if patch, ok := e["patch"].([]any); ok {
			base := messages[i]["content"]
			if base == nil {
				base = map[string]any{}
			}
			content, err := applyPatch(base, patch)
			if err == nil {
				messages[i]["content"] = content
				messages[i]["activityType"] = str(e, "activityType")
			}
		}
	case "REASONING_ENCRYPTED_VALUE":
		if str(e, "subtype") == "tool-call" {
			_, call := findTool(str(e, "entityId"))
			if call != nil {
				call["encryptedValue"] = e["encryptedValue"]
			}
		} else if i := index(str(e, "entityId")); i >= 0 && str(messages[i], "role") != "activity" {
			messages[i]["encryptedValue"] = e["encryptedValue"]
		}
	}
	return messages
}

func mergeMetadata(a, b map[string]any) map[string]any {
	if a == nil {
		a = map[string]any{}
	}
	// @ag-ui/core mergeMetadata is a shallow object spread. A namespace is
	// one value: a later object replaces it wholesale, including nested keys.
	for k, v := range b {
		a[k] = v
	}
	return a
}

// Activity patches use RFC 6902. Work on a copy so a failed operation leaves
// the previous activity intact, matching the client's applyPatch(newDocument).
func applyPatch(value any, patch []any) (any, error) {
	raw, _ := json.Marshal(value)
	var doc any
	_ = json.Unmarshal(raw, &doc)
	for _, item := range patch {
		p, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid patch")
		}
		op := str(p, "op")
		path, err := pointer(str(p, "path"))
		if err != nil {
			return nil, err
		}
		v, hasValue := p["value"]
		if _, ok := p["path"].(string); !ok {
			return nil, fmt.Errorf("patch path required")
		}
		if (op == "add" || op == "replace" || op == "test") && !hasValue {
			return nil, fmt.Errorf("patch value required")
		}
		if op == "copy" || op == "move" {
			if _, ok := p["from"].(string); !ok {
				return nil, fmt.Errorf("patch from required")
			}
			from, e := pointer(str(p, "from"))
			if e != nil {
				return nil, e
			}
			v, e = patchGet(doc, from)
			if e != nil {
				return nil, e
			}
			if op == "move" {
				if len(path) > len(from) && reflect.DeepEqual(path[:len(from)], from) {
					return nil, fmt.Errorf("cannot move into descendant")
				}
				doc, e = patchSet(doc, from, nil, "remove")
				if e != nil {
					return nil, e
				}
			} else {
				// fast-json-patch copies JSON values, not references.
				raw, _ := json.Marshal(v)
				_ = json.Unmarshal(raw, &v)
			}
		}
		if op == "test" {
			old, e := patchGet(doc, path)
			if e != nil || !reflect.DeepEqual(old, v) {
				return nil, fmt.Errorf("patch test failed")
			}
			continue
		}
		if op == "copy" || op == "move" {
			op = "add"
		}
		doc, err = patchSet(doc, path, v, op)
		if err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func pointer(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "/") {
		return nil, fmt.Errorf("invalid pointer")
	}
	parts := strings.Split(s[1:], "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		if parts[i] == "__proto__" || i > 0 && parts[i-1] == "constructor" && parts[i] == "prototype" {
			return nil, fmt.Errorf("unsafe patch path")
		}
	}
	return parts, nil
}

func patchGet(doc any, path []string) (any, error) {
	if len(path) == 0 {
		return doc, nil
	}
	switch d := doc.(type) {
	case map[string]any:
		v, ok := d[path[0]]
		if ok {
			return patchGet(v, path[1:])
		}
	case []any:
		i, e := patchIndex(path[0])
		if e == nil && i >= 0 && i < len(d) {
			return patchGet(d[i], path[1:])
		}
	}
	return nil, fmt.Errorf("missing patch path")
}

func patchSet(doc any, path []string, v any, op string) (any, error) {
	if len(path) == 0 {
		switch op {
		case "add", "replace":
			return v, nil
		case "remove":
			return nil, nil
		default:
			return nil, fmt.Errorf("invalid root operation")
		}
	}
	key := path[0]
	switch d := doc.(type) {
	case map[string]any:
		old, exists := d[key]
		if len(path) > 1 {
			if !exists {
				break
			}
			child, err := patchSet(old, path[1:], v, op)
			if err != nil {
				return nil, err
			}
			d[key] = child
			return d, nil
		}
		switch op {
		case "add":
			d[key] = v
			return d, nil
		case "replace":
			if exists {
				d[key] = v
				return d, nil
			}
		case "remove":
			if exists {
				delete(d, key)
				return d, nil
			}
		}
	case []any:
		i, err := patchIndex(key)
		if key == "-" && op == "add" {
			i = len(d)
			err = nil
		}
		if err != nil || i < 0 || i > len(d) {
			break
		}
		if len(path) > 1 {
			if i == len(d) {
				break
			}
			child, err := patchSet(d[i], path[1:], v, op)
			if err != nil {
				return nil, err
			}
			d[i] = child
			return d, nil
		}
		switch op {
		case "add":
			d = append(d, nil)
			copy(d[i+1:], d[i:])
			d[i] = v
			return d, nil
		case "replace":
			if i < len(d) {
				d[i] = v
				return d, nil
			}
		case "remove":
			if i < len(d) {
				return append(d[:i], d[i+1:]...), nil
			}
		}
	}
	return nil, fmt.Errorf("invalid patch operation")
}

// fast-json-patch accepts decimal array indices (including leading zeros),
// but not signed integers or whitespace accepted by strconv.Atoi.
func patchIndex(key string) (int, error) {
	if key == "" || strings.IndexFunc(key, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, fmt.Errorf("invalid array index")
	}
	return strconv.Atoi(key)
}
