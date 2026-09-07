package toolruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGatewayDeliversSafeInputCorrectionsAndKeepsFailuresPrivate(t *testing.T) {
	var calls atomic.Int64
	definitions := []tool.Definition{
		tool.Define("validate", "Validate input.", func(context.Context, testInput) (testOutput, error) {
			calls.Add(1)
			return testOutput{}, nil
		}),
		tool.Define("output", "Validate output.", func(context.Context, testInput) (testOutput, error) {
			return testOutput{Value: "OUTPUT_SECRET"}, nil
		}, tool.OutputSchemaJSON([]byte(`{"type":"object","properties":{"value":{"const":"SCHEMA_SECRET"}}}`))),
		tool.Define("business", "Fail with private details.", func(context.Context, testInput) (testOutput, error) {
			return testOutput{Value: "OUTPUT_SECRET"}, errors.New("HANDLER_SECRET")
		}),
		tool.Define("enum", "Validate allowed values.", func(context.Context, testInput) (testOutput, error) {
			calls.Add(1)
			return testOutput{}, nil
		}, tool.InputSchemaJSON([]byte(`{"type":"object","description":"SCHEMA_SECRET","properties":{"value":{"enum":["SCHEMA_SECRET"]}}}`))),
		tool.Define("decode", "Decode Go input.", func(context.Context, testInput) (testOutput, error) {
			calls.Add(1)
			return testOutput{}, nil
		}, tool.InputSchemaJSON([]byte(`{"type":"object"}`))),
	}
	runtime, err := newRuntime(newGatewayManager(testGatewayConfig()), definitions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	session := connectClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()
	for _, tc := range []struct {
		name, toolName, raw, code, hint string
	}{
		{"extra", "validate", `{"value":"VALUE_SECRET","extra":"VALUE_SECRET"}`, "invalid_input", `invalid field "extra"`},
		{"required", "validate", `{}`, "invalid_input", "required fields"},
		{"type", "validate", `{"value":123}`, "invalid_input", "field types"},
		{"null", "validate", `null`, "invalid_input", "field types"},
		{"enum", "enum", `{"value":"VALUE_SECRET"}`, "invalid_input", "allowed values"},
		{"decode", "decode", `{"value":123}`, "invalid_input", "input type"},
		{"output", "output", `{"value":"x"}`, "internal_error", "Tool execution failed."},
		{"business", "business", `{"value":"x"}`, "internal_error", "Tool execution failed."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.toolName, Arguments: json.RawMessage(tc.raw)})
			if err != nil {
				t.Fatal(err)
			}
			message := requireSafeToolFailure(t, result, tc.code)
			if !strings.Contains(message, tc.hint) {
				t.Fatalf("message = %q, want hint %q", message, tc.hint)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid-input handler ran %d times", calls.Load())
	}
}

func TestGatewaySuccessMatchesDefinitionAndRejectsUnauthenticatedCalls(t *testing.T) {
	var calls atomic.Int64
	definition := tool.Define("echo", "Echo JSON without transport mutations.", func(_ context.Context, in map[string]any) (map[string]any, error) {
		calls.Add(1)
		return in, nil
	}, tool.InputSchemaJSON([]byte(`{"type":"object","properties":{"value":{"type":"string","default":"input default"}}}`)),
		tool.OutputSchemaJSON([]byte(`{"type":"object","properties":{"value":{"type":"string","default":"output default"}}}`)))
	runtime, err := newRuntime(newGatewayManager(testGatewayConfig()), []tool.Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	for _, token := range []string{"", "wrong-token"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized || calls.Load() != 0 {
			t.Fatalf("unauthenticated call: status=%d handler calls=%d", response.StatusCode, calls.Load())
		}
	}
	session := connectClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("ListTools = %+v, %v", listed, err)
	}
	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		advertised any
		declared   json.RawMessage
	}{
		{listed.Tools[0].InputSchema, descriptor.InputSchemaJSON},
		{listed.Tools[0].OutputSchema, descriptor.OutputSchemaJSON},
	} {
		var expected any
		if err := json.Unmarshal(pair.declared, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pair.advertised, expected) {
			t.Fatal("MCP changed the advertised schema")
		}
	}
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"omitted", nil}, {"empty object", json.RawMessage(`{}`)}, {"populated", json.RawMessage(`{"value":"ok"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, err := definition.Invoke(ctx, tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			var arguments any
			if tc.raw != nil {
				arguments = tc.raw
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: arguments})
			if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
				t.Fatalf("CallTool = %+v, %v", result, err)
			}
			if calls.Load() != before+1 {
				t.Fatalf("one MCP call ran handler %d times", calls.Load()-before)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != string(expected) {
				t.Fatalf("content = %+v, want JSON text %s", result.Content, expected)
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil || string(encoded) != string(expected) {
				t.Fatalf("structured content = %s, %v; want %s", encoded, err, expected)
			}
		})
	}
}

func TestGatewayPreservesAllJSONOutputShapes(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"object", map[string]any{"value": "ok"}},
		{"array", []string{"one", "two"}},
		{"string", "ok"},
		{"number", 42},
		{"boolean", false},
		{"null", nil},
	}
	var definitions []tool.Definition
	for _, tc := range cases {
		definitions = append(definitions, tool.Define(tc.name, "Return a JSON value.", func(context.Context, struct{}) (any, error) {
			return tc.value, nil
		}, tool.OutputSchemaJSON([]byte(`true`))))
	}
	runtime, err := newRuntime(newGatewayManager(testGatewayConfig()), definitions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	session := connectClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name})
			if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
				t.Fatalf("result = %+v, %v", result, err)
			}
			want, _ := json.Marshal(tc.value)
			got, err := json.Marshal(result.StructuredContent)
			if err != nil || string(got) != string(want) {
				t.Fatalf("structured content = %s, %v; want %s", got, err, want)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != string(want) {
				t.Fatalf("JSON text fallback = %+v, want %s", result.Content, want)
			}
		})
	}
}

func TestRuntimeHandlerClassifiesMalformedAndCanceledInputs(t *testing.T) {
	definition := tool.Define("validate", "Validate input.", func(context.Context, testInput) (testOutput, error) {
		t.Fatal("handler ran for invalid input")
		return testOutput{}, nil
	})
	catalog, _, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Malformed arguments cannot be put in a valid JSON-RPC envelope, so test
	// the same runtime handler directly for this pre-transport failure.
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"value":`)}}
	result, err := catalog.handler("validate")(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if message := requireSafeToolFailure(t, result, "invalid_input"); !strings.Contains(message, "JSON syntax") {
		t.Fatalf("malformed input hint = %q", message)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = catalog.handler("validate")(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	requireSafeToolFailure(t, result, "canceled")
}

func requireSafeToolFailure(t *testing.T, result *mcp.CallToolResult, code string) string {
	t.Helper()
	if result == nil || !result.IsError || len(result.Content) != 1 || result.StructuredContent != nil {
		t.Fatalf("result = %+v, want one tool error without output", result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("error content type = %T", result.Content[0])
	}
	var payload struct{ Code, Message string }
	if err := json.Unmarshal([]byte(content.Text), &payload); err != nil || payload.Code != code {
		t.Fatalf("error payload = %q (%v), want code %q", content.Text, err, code)
	}
	for _, secret := range []string{"VALUE_SECRET", "OUTPUT_SECRET", "SCHEMA_SECRET", "HANDLER_SECRET", "tool-schema.json", "validate JSON Schema"} {
		if strings.Contains(content.Text, secret) {
			t.Errorf("runtime error leaked %q", secret)
		}
	}
	return payload.Message
}
