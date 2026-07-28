package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	santhoshjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Engine-side structured-output helpers: request normalization, capability
// resolution, and local validation. The host-facing constructors and the
// generic JSONSchemaFor/DecodeStructuredOutput API stay in the root package
// (structured_output.go).

func normalizeOutputSchema(schema *OutputSchema) (*OutputSchema, error) {
	if schema == nil {
		return nil, nil
	}
	out := cloneOutputSchema(schema)
	if out.Format == "" {
		out.Format = OutputFormatJSONSchema
	}
	if out.Mode == "" {
		out.Mode = StructuredOutputNativeStrict
	}
	if out.OnInvalid == "" {
		out.OnInvalid = StructuredOutputFailRun
	}
	if out.Format != OutputFormatJSONSchema {
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported output format %q", out.Format)}
	}
	switch out.Mode {
	case StructuredOutputNativeStrict, StructuredOutputPreferNative, StructuredOutputPromptValidate:
	default:
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported structured output mode %q", out.Mode)}
	}
	switch out.OnInvalid {
	case StructuredOutputFailRun, StructuredOutputReturnInvalid:
	default:
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported invalid policy %q", out.OnInvalid)}
	}
	if len(bytes.TrimSpace(out.SchemaJSON)) == 0 {
		return nil, &InvalidOutputSchemaError{Reason: "schema JSON is required"}
	}
	normalized, err := normalizeJSON(out.SchemaJSON)
	if err != nil {
		return nil, err
	}
	if _, err := compileJSONSchema(normalized); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "compile JSON Schema", Cause: err}
	}
	out.SchemaJSON = normalized
	return out, nil
}

func resolveStructuredOutputSource(desc Descriptor, schema *OutputSchema, providerStreaming bool, policy RunPolicy) (StructuredOutputSource, error) {
	if schema == nil {
		return "", nil
	}
	caps := desc.StructuredOutput
	hitlAsk := policy.HumanDecision.Permission == HumanDecisionAsk ||
		policy.HumanDecision.PlanReview == HumanDecisionAsk ||
		policy.HumanDecision.Question == QuestionAsk

	nativeOK := caps.JSONSchemaNative && caps.WorksWithRun
	if nativeOK && providerStreaming && !caps.WorksWithStreaming {
		nativeOK = false
	}
	if nativeOK && hitlAsk && !caps.WorksWithHITL {
		nativeOK = false
	}

	promptOK := caps.JSONSchemaPromptValidate && caps.WorksWithRun
	if promptOK && providerStreaming && !caps.WorksWithStreaming {
		promptOK = false
	}
	if promptOK && hitlAsk && !caps.WorksWithHITL {
		promptOK = false
	}

	switch schema.Mode {
	case StructuredOutputNativeStrict:
		if nativeOK {
			return StructuredOutputSourceNative, nil
		}
		return "", &StructuredOutputUnsupportedError{Driver: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, providerStreaming, hitlAsk, true)}
	case StructuredOutputPromptValidate:
		if promptOK {
			return StructuredOutputSourcePromptValidate, nil
		}
		return "", &StructuredOutputUnsupportedError{Driver: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, providerStreaming, hitlAsk, false)}
	case StructuredOutputPreferNative:
		if nativeOK {
			return StructuredOutputSourceNative, nil
		}
		if promptOK {
			return StructuredOutputSourcePromptValidate, nil
		}
		return "", &StructuredOutputUnsupportedError{Driver: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, providerStreaming, hitlAsk, false)}
	default:
		return "", &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported structured output mode %q", schema.Mode)}
	}
}

func structuredCapabilityReason(caps StructuredOutputCapability, providerStreaming, hitlAsk, native bool) string {
	switch {
	case native && !caps.JSONSchemaNative:
		return "native JSON Schema output is not supported"
	case !native && !caps.JSONSchemaPromptValidate:
		return "prompt-validation JSON Schema output is not supported"
	case !caps.WorksWithRun:
		return "structured output is not supported by the execution pipeline"
	case providerStreaming && !caps.WorksWithStreaming:
		return "structured output is not supported by the selected provider streaming transport"
	case hitlAsk && !caps.WorksWithHITL:
		return "structured output is not supported with HITL Ask modes"
	case caps.Notes != "":
		return caps.Notes
	default:
		return "unsupported structured output capability combination"
	}
}

func validateStructuredOutput(schema *OutputSchema, source StructuredOutputSource, raw []byte) *StructuredOutput {
	out := &StructuredOutput{
		Format:     schema.Format,
		Mode:       schema.Mode,
		Source:     source,
		SchemaHash: schemaHash(schema),
	}
	normalized, value, errs := validateJSONValue(schema.SchemaJSON, raw)
	out.RawJSON = normalized
	out.Value = value
	out.Valid = len(errs) == 0
	out.ValidationErrors = errs
	return out
}

func cloneOutputSchema(schema *OutputSchema) *OutputSchema {
	if schema == nil {
		return nil
	}
	out := *schema
	out.SchemaJSON = append(json.RawMessage(nil), schema.SchemaJSON...)
	return &out
}

func cloneStructuredOutput(value *StructuredOutput) *StructuredOutput {
	if value == nil {
		return nil
	}
	out := *value
	out.RawJSON = append(json.RawMessage(nil), value.RawJSON...)
	out.ValidationErrors = cloneStrings(value.ValidationErrors)
	return &out
}

func schemaHash(schema *OutputSchema) string {
	if schema == nil {
		return ""
	}
	return stableHash("output_schema", schema.Format, schema.SchemaJSON)
}

func normalizeJSON(raw []byte) (json.RawMessage, error) {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "parse JSON", Cause: err}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, &InvalidOutputSchemaError{Reason: "multiple JSON values"}
		}
		return nil, &InvalidOutputSchemaError{Reason: "parse JSON", Cause: err}
	}
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, decoded); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "canonicalize JSON", Cause: err}
	}
	return json.RawMessage(buf.Bytes()), nil
}

func compileJSONSchema(schemaJSON []byte) (*santhoshjsonschema.Schema, error) {
	doc, err := decodeJSONValue(schemaJSON)
	if err != nil {
		return nil, err
	}
	compiler := santhoshjsonschema.NewCompiler()
	compiler.DefaultDraft(santhoshjsonschema.Draft2020)
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("schema.json")
}

func validateJSONValue(schemaJSON []byte, raw []byte) (json.RawMessage, any, []string) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	value, err := decodeJSONValue(normalized)
	if err != nil {
		return normalized, nil, []string{err.Error()}
	}
	schema, err := compileJSONSchema(schemaJSON)
	if err != nil {
		return normalized, value, []string{err.Error()}
	}
	if err := schema.Validate(value); err != nil {
		return normalized, value, flattenValidationErrors(err)
	}
	return normalized, value, nil
}

func decodeJSONValue(raw []byte) (any, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, &InvalidOutputSchemaError{Reason: "multiple JSON values"}
		}
		return nil, err
	}
	return value, nil
}

func flattenValidationErrors(err error) []string {
	if err == nil {
		return nil
	}
	var validationErr *santhoshjsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return []string{err.Error()}
	}
	var out []string
	var walk func(*santhoshjsonschema.ValidationError)
	walk = func(e *santhoshjsonschema.ValidationError) {
		if e == nil {
			return
		}
		if len(e.Causes) == 0 {
			out = append(out, e.Error())
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(validationErr)
	if len(out) == 0 {
		out = append(out, err.Error())
	}
	return out
}

func structuredOutputPromptInstruction(schema *OutputSchema) string {
	if schema == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Return only a single JSON value as the final assistant response. ")
	b.WriteString("Do not include Markdown fences, prose, comments, or any text outside the JSON value. ")
	b.WriteString("The JSON value must validate against this JSON Schema")
	if schema.Name != "" {
		b.WriteString(" named ")
		b.WriteString(schema.Name)
	}
	if schema.Description != "" {
		b.WriteString(". Description: ")
		b.WriteString(schema.Description)
	}
	b.WriteString(". Schema: ")
	b.WriteString(string(schema.SchemaJSON))
	return b.String()
}

// --- operations used by the root package ----------------------------------

// NormalizeOutputSchema exposes normalizeOutputSchema for the root package.
func NormalizeOutputSchema(schema *OutputSchema) (*OutputSchema, error) {
	return normalizeOutputSchema(schema)
}

// ResolveStructuredOutputSource exposes resolveStructuredOutputSource for the
// root package.
func ResolveStructuredOutputSource(desc Descriptor, schema *OutputSchema, streaming bool, policy RunPolicy) (StructuredOutputSource, error) {
	return resolveStructuredOutputSource(desc, schema, streaming, policy)
}

// CloneOutputSchema exposes cloneOutputSchema for the root package.
func CloneOutputSchema(schema *OutputSchema) *OutputSchema { return cloneOutputSchema(schema) }

// NormalizeJSON exposes normalizeJSON for the root package.
func NormalizeJSON(raw []byte) (json.RawMessage, error) { return normalizeJSON(raw) }

// StructuredOutputPromptInstruction exposes structuredOutputPromptInstruction
// for the root package.
func StructuredOutputPromptInstruction(schema *OutputSchema) string {
	return structuredOutputPromptInstruction(schema)
}
