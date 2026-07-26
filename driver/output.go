package driver

import "encoding/json"

// OutputFormat labels the final business-output contract requested by a host.
// It is distinct from driver protocol envelopes such as `stream-json`, which
// only make CLI events machine-readable.
type OutputFormat string

const (
	OutputFormatJSONSchema OutputFormat = "json_schema"
)

// StructuredOutputMode states the enforcement level the host is requesting.
type StructuredOutputMode string

const (
	// StructuredOutputNativeStrict requires provider/CLI-native schema
	// enforcement. The SDK rejects unsupported drivers before launch.
	StructuredOutputNativeStrict StructuredOutputMode = "native_strict"
	// StructuredOutputPreferNative uses native enforcement when available,
	// otherwise the explicit prompt+validation fallback when supported.
	StructuredOutputPreferNative StructuredOutputMode = "prefer_native"
	// StructuredOutputPromptValidate injects exact-JSON instructions and
	// validates the driver's final Output locally.
	StructuredOutputPromptValidate StructuredOutputMode = "prompt_validate"
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
	Mode        StructuredOutputMode
	SchemaJSON  json.RawMessage
	Name        string
	Description string
	OnInvalid   StructuredOutputInvalidPolicy
}

// StructuredOutputSource reports which mechanism produced the final JSON.
type StructuredOutputSource string

const (
	StructuredOutputSourceNative         StructuredOutputSource = "native"
	StructuredOutputSourcePromptValidate StructuredOutputSource = "prompt_validate"
)

// StructuredOutput is the portable final business value for structured-output
// runs. RawJSON is never raw stdout and never a provider terminal wrapper; it
// is the final assistant JSON value validated against the requested schema.
type StructuredOutput struct {
	Format OutputFormat
	Mode   StructuredOutputMode
	Source StructuredOutputSource

	RawJSON json.RawMessage
	Value   any

	Valid            bool
	ValidationErrors []string
	SchemaHash       string
}
