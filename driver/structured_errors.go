package driver

import "errors"

// Structured-output errors live with the structured-output vocabulary. This
// gives the engine, root package, and third-party drivers one concrete error
// identity without making any public API depend on internal packages.
var (
	// ErrStructuredOutputUnsupported is returned before driver launch when
	// the structured-output contract cannot be honored by the bound driver
	// or selected provider transport.
	ErrStructuredOutputUnsupported = errors.New("agentadaptor: structured output unsupported by driver")

	// ErrInvalidOutputSchema is returned before driver launch when a host
	// supplies malformed JSON, an unsupported output format, or a
	// JSON Schema document that cannot be compiled for local validation.
	ErrInvalidOutputSchema = errors.New("agentadaptor: invalid output schema")
)

// StructuredOutputUnsupportedError carries diagnostic detail while
// unwrapping to [ErrStructuredOutputUnsupported].
type StructuredOutputUnsupportedError struct {
	Driver string
	Reason string
}

func (e *StructuredOutputUnsupportedError) Error() string {
	if e == nil {
		return ErrStructuredOutputUnsupported.Error()
	}
	msg := ErrStructuredOutputUnsupported.Error()
	if e.Driver != "" {
		msg += ": driver=" + e.Driver
	}
	if e.Reason != "" {
		msg += " reason=" + e.Reason
	}
	return msg
}

// Unwrap exposes the stable error category for errors.Is.
func (e *StructuredOutputUnsupportedError) Unwrap() error {
	return ErrStructuredOutputUnsupported
}

// InvalidOutputSchemaError carries diagnostic detail while unwrapping to
// [ErrInvalidOutputSchema].
type InvalidOutputSchemaError struct {
	Reason string
	Cause  error
}

func (e *InvalidOutputSchemaError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrInvalidOutputSchema.Error()
	}
	return ErrInvalidOutputSchema.Error() + ": " + e.Reason
}

// Unwrap preserves both the public category and the lower-level cause.
func (e *InvalidOutputSchemaError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrInvalidOutputSchema
	}
	return errors.Join(ErrInvalidOutputSchema, e.Cause)
}
