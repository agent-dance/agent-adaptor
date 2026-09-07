package tool

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	santhoshjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	santhoshjsonschemakind "github.com/santhosh-tekuri/jsonschema/v6/kind"
)

func invalidInputRejection(message string) error {
	return errors.Join(ErrInvalidInput, Reject("invalid_input", message))
}

// Only errors from the JSON parser and schema validator enter this function.
// In particular, custom Go decoders cannot supply a validation error or use As
// to inject model-visible text. Never format the error, schema, or input values.
func invalidInputMessage(err error) string {
	var validationErr *santhoshjsonschema.ValidationError
	if errors.As(err, &validationErr) {
		if key, ok := invalidAdditionalProperty(validationErr); ok {
			return "invalid field " + quoteInputField(key) + "; use a declared field name and retry"
		}
		return "tool arguments do not match the input schema; check required fields, field types, and allowed values, then retry"
	}
	return "invalid JSON arguments; correct the JSON syntax and retry"
}

// The validator may traverse object properties in map order. Choose the
// lexicographically first extra name across all causes, including nested ones,
// so diagnostics do not depend on that order. Alternative branches cannot prove
// a field is undeclared: it may be valid in another branch that failed for a
// different reason. A separate bool preserves an empty extra name.
func invalidAdditionalProperty(err *santhoshjsonschema.ValidationError) (string, bool) {
	if err == nil {
		return "", false
	}
	switch err.ErrorKind.(type) {
	case *santhoshjsonschemakind.AnyOf, *santhoshjsonschemakind.OneOf:
		return "", false
	}
	var first string
	found := false
	if invalid, ok := err.ErrorKind.(*santhoshjsonschemakind.AdditionalProperties); ok {
		for _, key := range invalid.Properties {
			if !found || key < first {
				first, found = key, true
			}
		}
	}
	for _, cause := range err.Causes {
		if key, ok := invalidAdditionalProperty(cause); ok && (!found || key < first) {
			first, found = key, true
		}
	}
	return first, found
}

func quoteInputField(key string) string {
	const maximumFieldBytes = 64
	var bounded strings.Builder
	for _, r := range key {
		if bounded.Len()+utf8.RuneLen(r) > maximumFieldBytes {
			bounded.WriteRune('…')
			break
		}
		bounded.WriteRune(r)
	}
	// Quote escapes quotes, backslashes, controls, and non-printing Unicode.
	// Bounding before quoting keeps UTF-8 intact and the entire hint < 512 bytes.
	return strconv.Quote(bounded.String())
}
