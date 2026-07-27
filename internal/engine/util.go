package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
)

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneEnvBindings(values []EnvBinding) []EnvBinding {
	if len(values) == 0 {
		return nil
	}
	out := make([]EnvBinding, 0, len(values))
	for _, value := range values {
		out = append(out, EnvBinding{Name: value.Name, Value: value.Value})
	}
	return out
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneWorkspaceRuntimeConfig(cfg *WorkspaceRuntimeConfig) *WorkspaceRuntimeConfig {
	if cfg == nil {
		return nil
	}
	return &WorkspaceRuntimeConfig{Services: cloneRuntimeServiceSpecs(cfg.Services)}
}

func cloneRuntimeServiceSpecs(values []RuntimeServiceSpec) []RuntimeServiceSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]RuntimeServiceSpec, 0, len(values))
	for _, value := range values {
		out = append(out, RuntimeServiceSpec{
			ID:          value.ID,
			Name:        value.Name,
			URL:         value.URL,
			Description: value.Description,
			Lifecycle:   value.Lifecycle,
			ReuseKey:    value.ReuseKey,
			Command:     value.Command,
			CWD:         value.CWD,
			Port:        value.Port,
			Metadata:    cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneRuntimeServiceRefs(values []RuntimeServiceRef) []RuntimeServiceRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]RuntimeServiceRef, 0, len(values))
	for _, value := range values {
		out = append(out, RuntimeServiceRef{
			ID:           value.ID,
			Name:         value.Name,
			URL:          value.URL,
			Status:       value.Status,
			Lifecycle:    value.Lifecycle,
			ReuseKey:     value.ReuseKey,
			Command:      value.Command,
			CWD:          value.CWD,
			Port:         value.Port,
			OwnerAgentID: value.OwnerAgentID,
			Health:       value.Health,
			MCP:          cloneMCPServerSpecPtr(value.MCP),
			Metadata:     cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneRuntimePayload(payload RuntimePayload) RuntimePayload {
	return RuntimePayload{
		Requested:   cloneRuntimeServiceSpecs(payload.Requested),
		Ensured:     cloneRuntimeServiceRefs(payload.Ensured),
		SecretEnv:   cloneEnvBindings(payload.SecretEnv),
		Fingerprint: payload.Fingerprint,
	}
}

func cloneRuntimeServiceReports(values []RuntimeServiceReport) []RuntimeServiceReport {
	if len(values) == 0 {
		return nil
	}
	out := make([]RuntimeServiceReport, 0, len(values))
	for _, value := range values {
		out = append(out, RuntimeServiceReport{
			ID:           value.ID,
			Name:         value.Name,
			URL:          value.URL,
			Status:       value.Status,
			Lifecycle:    value.Lifecycle,
			ReuseKey:     value.ReuseKey,
			Command:      value.Command,
			CWD:          value.CWD,
			Port:         value.Port,
			OwnerAgentID: value.OwnerAgentID,
			Health:       value.Health,
			Metadata:     cloneStringMap(value.Metadata),
		})
	}
	return out
}

func cloneTranscriptItems(values []TranscriptItem) []TranscriptItem {
	if len(values) == 0 {
		return nil
	}
	out := make([]TranscriptItem, 0, len(values))
	for _, value := range values {
		out = append(out, cloneTranscriptItem(value))
	}
	return out
}

func cloneTranscriptItem(value TranscriptItem) TranscriptItem {
	return TranscriptItem{
		Kind:      value.Kind,
		Text:      value.Text,
		Delta:     value.Delta,
		ToolUseID: value.ToolUseID,
		ToolName:  value.ToolName,
		Input:     value.Input,
		IsError:   value.IsError,
		Model:     value.Model,
		SessionID: value.SessionID,
		Usage:     cloneUsagePointer(value.Usage),
		CostUSD:   cloneFloat64Pointer(value.CostUSD),
		Subtype:   value.Subtype,
		Errors:    cloneStrings(value.Errors),
		Metadata:  cloneStringMap(value.Metadata),
		Data:      cloneAnyMap(value.Data),
	}
}

func cloneUsagePointer(value *Usage) *Usage {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneRawStreams(value *RawStreams) *RawStreams {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Terminal != nil {
		terminal := *value.Terminal
		terminal.JSON = append([]byte(nil), value.Terminal.JSON...)
		copy.Terminal = &terminal
	}
	return &copy
}

func cloneRunQuestion(question *RunQuestion) *RunQuestion {
	if question == nil {
		return nil
	}
	out := &RunQuestion{
		Prompt:  question.Prompt,
		Choices: make([]RunChoice, 0, len(question.Choices)),
	}
	for _, choice := range question.Choices {
		out.Choices = append(out.Choices, choice)
	}
	return out
}

func cloneRunFailure(failure *RunFailure) *RunFailure {
	if failure == nil {
		return nil
	}
	return &RunFailure{
		Message:       failure.Message,
		Code:          failure.Code,
		Metadata:      cloneAnyMap(failure.Metadata),
		HumanDecision: cloneHumanDecisionFailure(failure.HumanDecision),
	}
}

func cloneHumanDecisionFailure(f *HumanDecisionFailure) *HumanDecisionFailure {
	if f == nil {
		return nil
	}
	return &HumanDecisionFailure{
		Kind:     f.Kind,
		Source:   f.Source,
		Decision: f.Decision,
		Request:  cloneDecisionRequest(f.Request),
		Attempts: f.Attempts,
	}
}

func cloneDecisionRequest(req *DecisionRequest) *DecisionRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.Payload = cloneAnyMap(req.Payload)
	if len(req.Choices) > 0 {
		out.Choices = append([]DecisionChoice(nil), req.Choices...)
	}
	return &out
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneConfigSchema(schema *ConfigSchema) *ConfigSchema {
	if schema == nil {
		return nil
	}
	return &ConfigSchema{
		Fields: cloneConfigFields(schema.Fields),
	}
}

func cloneConfigFields(fields []ConfigField) []ConfigField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]ConfigField, 0, len(fields))
	for _, field := range fields {
		out = append(out, ConfigField{
			Name:        field.Name,
			Label:       field.Label,
			Type:        field.Type,
			Required:    field.Required,
			Description: field.Description,
			Hint:        field.Hint,
			Default:     field.Default,
			Options:     cloneConfigOptions(field.Options),
			Group:       field.Group,
			Meta:        cloneStringMap(field.Meta),
		})
	}
	return out
}

func cloneConfigOptions(options []ConfigOption) []ConfigOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]ConfigOption, len(options))
	copy(out, options)
	return out
}

func mergeStringMaps(parts ...map[string]string) map[string]string {
	size := 0
	for _, part := range parts {
		size += len(part)
	}
	if size == 0 {
		return nil
	}
	out := make(map[string]string, size)
	for _, part := range parts {
		for key, value := range part {
			out[key] = value
		}
	}
	return out
}

func ensureBaseCWD(cwd string) string {
	if strings.TrimSpace(cwd) != "" {
		return cwd
	}
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func canonicalize(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch value := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if value {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(value)
		buf.Write(encoded)
	case float64:
		encoded, _ := json.Marshal(value)
		buf.Write(encoded)
	case json.Number:
		buf.WriteString(value.String())
	case []any:
		buf.WriteByte('[')
		for index, item := range value {
			if index > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		buf.WriteByte('{')
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for index, key := range keys {
			if index > 0 {
				buf.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			buf.Write(encoded)
			buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, value[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical type %T", v)
	}
	return nil
}

func stableHash(parts ...any) string {
	canonicalParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			canonicalParts = append(canonicalParts, "null")
			continue
		}
		rv := reflect.ValueOf(part)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			canonicalParts = append(canonicalParts, "null")
			continue
		}
		decoded, err := canonicalize(part)
		if err != nil {
			canonicalParts = append(canonicalParts, fmt.Sprintf("%T:%v", part, part))
			continue
		}
		var buf bytes.Buffer
		if err := writeCanonicalJSON(&buf, decoded); err != nil {
			canonicalParts = append(canonicalParts, fmt.Sprintf("%T:%v", part, part))
			continue
		}
		canonicalParts = append(canonicalParts, buf.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(canonicalParts, "\x00")))
	return fmt.Sprintf("%x", sum[:])
}
