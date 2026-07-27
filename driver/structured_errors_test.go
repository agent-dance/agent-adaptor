package driver_test

import (
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestStructuredErrorIdentityAndCause(t *testing.T) {
	unsupported := &driver.StructuredOutputUnsupportedError{
		Adapter: "fake",
		Mode:    driver.StructuredOutputNativeStrict,
	}
	if !errors.Is(unsupported, driver.ErrStructuredOutputUnsupported) {
		t.Fatal("unsupported error must match its driver sentinel")
	}
	var typedUnsupported *driver.StructuredOutputUnsupportedError
	if !errors.As(unsupported, &typedUnsupported) || typedUnsupported != unsupported {
		t.Fatal("unsupported error must preserve concrete identity")
	}

	cause := errors.New("schema compiler")
	invalid := &driver.InvalidOutputSchemaError{Reason: "compile", Cause: cause}
	if !errors.Is(invalid, driver.ErrInvalidOutputSchema) || !errors.Is(invalid, cause) {
		t.Fatal("invalid schema error must preserve category and cause")
	}
	var typedInvalid *driver.InvalidOutputSchemaError
	if !errors.As(invalid, &typedInvalid) || typedInvalid != invalid {
		t.Fatal("invalid schema error must preserve concrete identity")
	}
}
