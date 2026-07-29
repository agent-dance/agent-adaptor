package driver

import "encoding/json"

// OutputFormat labels the final business-output contract requested by a host.
// It is distinct from driver protocol envelopes such as `stream-json`, which
// only make CLI events machine-readable.
type OutputFormat string

const (
	// OutputFormatJSONSchema requests a value governed by a JSON Schema.
	OutputFormatJSONSchema OutputFormat = "json_schema"
)

// StructuredOutputInvalidPolicy selects how prompt-validation failures are
// surfaced after the driver returns.
type StructuredOutputInvalidPolicy string

const (
	// StructuredOutputFailRun marks the run with FailurePolicyError when the
	// final JSON is absent or fails local validation.
	StructuredOutputFailRun StructuredOutputInvalidPolicy = "fail_run"
	// StructuredOutputReturnInvalid returns StructuredOutput.Valid=false but
	// does not turn the run into a failure.
	StructuredOutputReturnInvalid StructuredOutputInvalidPolicy = "return_invalid"
)

// OutputSchema is a per-run request for final structured JSON output.
// SchemaJSON is the raw JSON Schema document supplied by the host or generated
// by JSONSchemaFor. Public API intentionally exposes no third-party schema
// library types.
type OutputSchema struct {
	Format      OutputFormat
	SchemaJSON  json.RawMessage
	Name        string
	Description string
	OnInvalid   StructuredOutputInvalidPolicy
}

// StructuredOutputSource reports which mechanism produced the final JSON.
type StructuredOutputSource string

const (
	// StructuredOutputSourceNative means the provider enforced the schema.
	StructuredOutputSourceNative StructuredOutputSource = "native"
	// StructuredOutputSourcePromptValidate means core prompted for exact JSON
	// and validated the returned value locally.
	StructuredOutputSourcePromptValidate StructuredOutputSource = "prompt_validate"
)

// StructuredOutput is the portable final business value for structured-output
// runs. RawJSON is never raw stdout and never a provider terminal wrapper; it
// is the final assistant JSON value validated against the requested schema.
type StructuredOutput struct {
	Format OutputFormat
	Source StructuredOutputSource

	RawJSON json.RawMessage
	Value   any

	Valid            bool
	ValidationErrors []string
	SchemaHash       string
}
