package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/tool"
)

const inputErrorSchema = `{"type":"object","description":"SCHEMA_SECRET","required":["query"],"additionalProperties":false,"properties":{"query":{"type":"string","enum":["ALLOWED_SECRET"]},"nested":{"type":"object","additionalProperties":false,"required":["count"],"properties":{"count":{"type":"integer"}}}}}`

func TestInputFailuresAreSafeCorrectableRejections(t *testing.T) {
	cases := []struct {
		name, input, hint string
	}{
		{"malformed", `{"query":"VALUE_SECRET"`, "JSON syntax"},
		{"multiple values", `{} {}`, "JSON syntax"},
		{"trailing garbage", `{} VALUE_SECRET`, "JSON syntax"},
		{"required", `{}`, "required fields"},
		{"type", `{"query":17}`, "field types"},
		{"enum", `{"query":"VALUE_SECRET"}`, "allowed values"},
		{"extra", `{"query":"ALLOWED_SECRET","extra":"VALUE_SECRET"}`, `invalid field "extra"`},
		{"nested extra", `{"query":"ALLOWED_SECRET","nested":{"count":1,"extra":"VALUE_SECRET"}}`, `invalid field "extra"`},
		{"nested required", `{"query":"ALLOWED_SECRET","nested":{}}`, "required fields"},
		{"nested type", `{"query":"ALLOWED_SECRET","nested":{"count":"VALUE_SECRET"}}`, "field types"},
		{"root type", `[]`, "field types"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			definition := tool.Define("validate", "Validate input.", func(context.Context, map[string]any) (struct{}, error) {
				calls++
				return struct{}{}, nil
			}, tool.InputSchemaJSON([]byte(inputErrorSchema)))
			out, err := definition.Invoke(context.Background(), json.RawMessage(tc.input))
			if out != nil || calls != 0 {
				t.Fatalf("invalid input produced output or called handler: output=%s calls=%d", out, calls)
			}
			message := requireInvalidInputRejection(t, err)
			if !strings.Contains(message, tc.hint) {
				t.Errorf("message = %q, want hint %q", message, tc.hint)
			}
		})
	}
}

func requireInvalidInputRejection(t *testing.T, err error) string {
	t.Helper()
	if !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	code, message, ok := tool.AsRejection(err)
	if !ok || code != "invalid_input" {
		t.Fatalf("rejection = (%q, %q, %v), want invalid_input", code, message, ok)
	}
	for _, secret := range []string{"VALUE_SECRET", "SCHEMA_SECRET", "ALLOWED_SECRET", "DECODER_SECRET", "tool-schema.json", "json: cannot unmarshal", "validate JSON Schema"} {
		if strings.Contains(message, secret) || strings.Contains(err.Error(), secret) {
			t.Errorf("input error leaked %q", secret)
		}
	}
	return message
}

func TestAdditionalInputFieldNamesAreBoundedEscapedAndDeterministic(t *testing.T) {
	definition := tool.Define("validate", "Validate input.", func(context.Context, struct{}) (struct{}, error) {
		t.Fatal("handler called for extra fields")
		return struct{}{}, nil
	})
	cases := []struct {
		name, key, quoted string
	}{
		{"empty", "", `""`},
		{"unicode", "多余字段🙂", `"多余字段🙂"`},
		{"quotes and controls", "bad\"\\\n\r\t\x00\x1b\u202e", `"bad\"\\\n\r\t\x00\x1b\u202e"`},
		{"long ascii", strings.Repeat("a", 8192) + "PRIVATE_SUFFIX", `"` + strings.Repeat("a", 64) + `…"`},
		{"long unicode", strings.Repeat("界", 8192) + "PRIVATE_SUFFIX", `"` + strings.Repeat("界", 21) + `…"`},
		{"invalid utf8", "bad\xffkey", "\"bad�key\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{tc.key: "VALUE_SECRET"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = definition.Invoke(context.Background(), raw)
			message := requireInvalidInputRejection(t, err)
			if !strings.Contains(message, "invalid field "+tc.quoted+";") {
				t.Fatalf("field name was not safely quoted: %q", message)
			}
			if len(message) > 512 || !utf8.ValidString(message) || strings.Contains(message, "PRIVATE_SUFFIX") {
				t.Fatalf("unbounded or invalid UTF-8 message: length=%d", len(message))
			}
			for _, r := range message {
				if unicode.IsControl(r) || r == '\u202e' {
					t.Fatalf("unescaped control character %U", r)
				}
			}
		})
	}
	for i := 0; i < 50; i++ {
		_, err := definition.Invoke(context.Background(), json.RawMessage(`{"z":"VALUE_SECRET","a":"VALUE_SECRET","m":"VALUE_SECRET"}`))
		if message := requireInvalidInputRejection(t, err); !strings.Contains(message, `invalid field "a";`) {
			t.Fatalf("non-deterministic extra-field selection: %q", message)
		}
	}
}

type failingDecodedInput struct{}

func (*failingDecodedInput) UnmarshalJSON([]byte) error {
	return tool.Reject("forged_decoder", "DECODER_SECRET")
}

func TestGoInputDecodingHasDistinctSafeHint(t *testing.T) {
	definitions := []tool.Definition{
		tool.Define("mismatch", "Decode a typed input.", func(context.Context, searchInput) (struct{}, error) {
			t.Fatal("handler called after Go type mismatch")
			return struct{}{}, nil
		}, tool.InputSchemaJSON([]byte(`{"type":"object"}`))),
		tool.Define("custom", "Decode a custom input.", func(context.Context, failingDecodedInput) (struct{}, error) {
			t.Fatal("handler called after custom decode failure")
			return struct{}{}, nil
		}),
	}
	for i, definition := range definitions {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			raw := json.RawMessage(`{}`)
			if i == 0 {
				raw = json.RawMessage(`{"query":123}`)
			}
			_, err := definition.Invoke(context.Background(), raw)
			message := requireInvalidInputRejection(t, err)
			if !strings.Contains(message, "input type") || strings.Contains(message, "JSON syntax") {
				t.Fatalf("Go input decode was misclassified: %q", message)
			}
		})
	}
}

func TestAlternativeSchemaBranchesDoNotMislabelDeclaredFields(t *testing.T) {
	for _, keyword := range []string{"oneOf", "anyOf"} {
		t.Run(keyword, func(t *testing.T) {
			schema := `{"type":"object","` + keyword + `":[{"type":"object","additionalProperties":false,"required":["count"],"properties":{"count":{"type":"integer"}}},{"type":"object","additionalProperties":false,"required":["label"],"properties":{"label":{"type":"string"}}}]}`
			definition := tool.Define("alternative", "Choose valid arguments.", func(context.Context, map[string]any) (struct{}, error) {
				t.Fatal("handler ran for invalid alternative")
				return struct{}{}, nil
			}, tool.InputSchemaJSON([]byte(schema)))
			_, err := definition.Invoke(context.Background(), json.RawMessage(`{"count":"VALUE_SECRET"}`))
			message := requireInvalidInputRejection(t, err)
			if strings.Contains(message, "invalid field") || !strings.Contains(message, "field types") {
				t.Fatalf("declared count field was mislabeled by another branch: %q", message)
			}
		})
	}
}

func TestAdditionalFieldSelectionStableAcrossNestedErrors(t *testing.T) {
	definition := tool.Define("nested", "Validate nested objects.", func(context.Context, map[string]any) (struct{}, error) {
		t.Fatal("handler ran for nested extra fields")
		return struct{}{}, nil
	}, tool.InputSchemaJSON([]byte(`{"type":"object","properties":{"left":{"type":"object","additionalProperties":false},"right":{"type":"object","additionalProperties":false}}}`)))
	for i := 0; i < 50; i++ {
		_, err := definition.Invoke(context.Background(), json.RawMessage(`{"left":{"z":"VALUE_SECRET"},"right":{"a":"VALUE_SECRET"}}`))
		if message := requireInvalidInputRejection(t, err); !strings.Contains(message, `invalid field "a";`) {
			t.Fatalf("nested extra selection changed: %q", message)
		}
	}
}

type forgedInputRejection struct{}

func (forgedInputRejection) Error() string { return "DECODER_SECRET" }
func (forgedInputRejection) As(target any) bool {
	return errors.As(tool.Reject("invalid_input", "DECODER_SECRET"), target)
}

func TestAsRejectionRejectsExternalAsThroughWrappers(t *testing.T) {
	for _, err := range []error{forgedInputRejection{}, fmt.Errorf("wrapped: %w", forgedInputRejection{}), errors.Join(tool.ErrInvalidInput, forgedInputRejection{})} {
		if code, message, ok := tool.AsRejection(err); ok {
			t.Fatalf("external As forged rejection: (%q, %q)", code, message)
		}
	}
}

func TestCanceledInvalidInputDoesNotBecomeRejection(t *testing.T) {
	definition := tool.Define("cancel", "Preserve cancellation.", func(context.Context, struct{}) (struct{}, error) {
		t.Fatal("canceled handler ran")
		return struct{}{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := definition.Invoke(ctx, json.RawMessage(`{"broken":`))
	if !errors.Is(err, context.Canceled) || errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("cancellation lost priority: %v", err)
	}
	if _, _, ok := tool.AsRejection(err); ok {
		t.Fatal("cancellation was classified as model-correctable input")
	}
}
