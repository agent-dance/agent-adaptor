package toolruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type outputCase struct {
	name, schema   string
	valid, invalid []string
	object         bool
}

func outputCases() []outputCase {
	return []outputCase{
		{name: "object", schema: `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`, valid: []string{`{"value":"ok"}`}, invalid: []string{`"no"`}, object: true},
		{name: "string", schema: `{"type":"string","minLength":2}`, valid: []string{`"ok"`}, invalid: []string{`"x"`, `null`}},
		{name: "number", schema: `{"type":"number"}`, valid: []string{`9007199254740993`, `1.234567890123456789e42`}, invalid: []string{`"no"`}},
		{name: "precise_schema", schema: `{"enum":[9007199254740993],"default":9007199254740993,"examples":[1.234567890123456789e42]}`, valid: []string{`9007199254740993`}, invalid: []string{`9007199254740992`}},
		{name: "boolean", schema: `{"type":"boolean"}`, valid: []string{`false`, `true`}, invalid: []string{`0`}},
		{name: "null", schema: `{"type":"null"}`, valid: []string{`null`}, invalid: []string{`{}`}},
		{name: "array", schema: `{"type":"array","items":{"type":"integer"}}`, valid: []string{`[1,2]`, `[]`}, invalid: []string{`["no"]`}},
		{name: "true", schema: `true`, valid: []string{`null`, `false`, `[]`, `{"result":"nested"}`}},
		{name: "false", schema: `false`, invalid: []string{`null`, `{}`, `false`}},
		{name: "nullable", schema: `{"type":["object","null"]}`, valid: []string{`{}`, `null`}, invalid: []string{`[]`}},
		{name: "object_type_array", schema: `{"type":["object"]}`, valid: []string{`{}`}, invalid: []string{`null`}},
		{name: "union", schema: `{"oneOf":[{"type":"string"},{"type":"number"}]}`, valid: []string{`"ok"`, `42`}, invalid: []string{`false`}},
		{name: "defs", schema: `{"$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "pointer_escaped", schema: `{"$defs":{"a/b~c":{"type":"string"}},"$ref":"#/$defs/a~1b~0c"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "pointer_uri_escaped", schema: `{"$defs":{"a b":{"type":"string"}},"$ref":"#/$defs/a%20b"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "anchor", schema: `{"$defs":{"value":{"$anchor":"value","type":"string"}},"$ref":"#value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "recursive", schema: `{"anyOf":[{"type":"string"},{"type":"array","items":{"$ref":"#"}}]}`, valid: []string{`["ok",["ok"]]`}, invalid: []string{`[0]`}},
		{name: "dynamic", schema: `{"$dynamicAnchor":"node","anyOf":[{"type":"string"},{"type":"array","items":{"$dynamicRef":"#node"}}]}`, valid: []string{`["ok",["ok"]]`}, invalid: []string{`[0]`}},
		{name: "nested_id", schema: `{"$defs":{"value":{"$id":"nested.json","$anchor":"item","type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "scoped_anchors", schema: `{"$anchor":"item","type":"array","items":{"$id":"nested.json","$anchor":"item","anyOf":[{"type":"string"},{"type":"array","items":{"$ref":"#item"}}]}}`, valid: []string{`[["ok"]]`}, invalid: []string{`[0]`}},
		{name: "absolute_id", schema: `{"$id":"https://schema.invalid/original.json","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "authority_id", schema: `{"$id":"//schema.invalid/original.json","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "relative_id", schema: `{"$id":"relative/original.json","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "empty_id", schema: `{"$id":"","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "fragment_id", schema: `{"$id":"#","$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`, valid: []string{`"ok"`}, invalid: []string{`0`}},
		{name: "literal_data", schema: `{"const":{"$ref":"#/literal","$id":"relative.json"},"default":{"$ref":"#/default","$id":"default.json"},"examples":[{"$ref":"#/example","$id":"example.json"}]}`, valid: []string{`{"$ref":"#/literal","$id":"relative.json"}`}, invalid: []string{`{"$ref":"#/changed","$id":"relative.json"}`}},
	}
}

type outputIndex struct {
	Index int `json:"index"`
}

func TestHostedOutputSchemaAndValuesAreLegacyCompatible(t *testing.T) {
	cases := outputCases()
	definitions := make([]tool.Definition, 0, len(cases))
	for _, tc := range cases {
		values := append(append([]string(nil), tc.valid...), tc.invalid...)
		definitions = append(definitions, tool.Define(tc.name, "Return a controlled output.", func(_ context.Context, in outputIndex) (json.RawMessage, error) {
			return json.RawMessage(values[in.Index]), nil
		}, tool.OutputSchemaJSON([]byte(tc.schema))))
	}
	runtime, err := newRuntime(newGatewayManager(testGatewayConfig()), definitions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	endpoint, err := runtime.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	session := connectClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	defer session.Close()
	legacy := legacyOutputClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
	listed := legacy(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	t.Logf("legacy_list_response=%s", listed)
	var listing struct {
		Tools []struct {
			Name         string
			OutputSchema json.RawMessage
		}
	}
	if err := json.Unmarshal(listed, &listing); err != nil {
		t.Fatal(err)
	}
	schemas := make(map[string]json.RawMessage)
	for _, advertised := range listing.Tools {
		schemas[advertised.Name] = advertised.OutputSchema
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := definitions[i].Descriptor()
			if err != nil {
				t.Fatal(err)
			}
			wire := schemas[tc.name]
			var object map[string]json.RawMessage
			if json.Unmarshal(wire, &object) != nil || string(object["type"]) != `"object"` {
				t.Fatalf("legacy outputSchema must be an object schema, got %s", wire)
			}
			if tc.object && !bytes.Equal(wire, descriptor.OutputSchemaJSON) {
				t.Fatalf("explicit-object schema changed: %s", wire)
			}
			if !tc.object {
				var properties map[string]json.RawMessage
				if json.Unmarshal(object["properties"], &properties) != nil || !bytes.HasPrefix(properties["result"], []byte("{")) {
					t.Fatal("legacy properties.result schema must itself be an object")
				}
			}
			originalValidator := compileOutputSchema(t, descriptor.OutputSchemaJSON)
			wireValidator := compileOutputSchema(t, wire)
			for index, raw := range append(append([]string(nil), tc.valid...), tc.invalid...) {
				valid := index < len(tc.valid)
				value := outputJSONValue(t, []byte(raw))
				wireValue := value
				if !tc.object {
					wireValue = map[string]any{"result": value}
				}
				if got := originalValidator.Validate(value) == nil; got != valid {
					t.Fatalf("original fixture validation=%v, want %v", got, valid)
				}
				if got := wireValidator.Validate(wireValue) == nil; got != valid {
					t.Fatalf("projected fixture validation=%v, want %v", got, valid)
				}
				input, _ := json.Marshal(outputIndex{Index: index})
				local, localErr := definitions[i].Invoke(ctx, input)
				result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: json.RawMessage(input)})
				if err != nil {
					t.Fatal(err)
				}
				if !valid {
					if !errors.Is(localErr, tool.ErrInvalidOutput) {
						t.Fatalf("local error=%v", localErr)
					}
					requireSafeToolFailure(t, result, "internal_error")
					continue
				}
				if localErr != nil || result.IsError || len(result.Content) != 1 {
					t.Fatalf("success lost: local=%v result=%+v", localErr, result)
				}
				text, ok := result.Content[0].(*mcp.TextContent)
				if !ok || text.Text != string(local) {
					t.Fatalf("original output text changed: %+v, want %s", result.Content, local)
				}
				// The Go SDK client decodes structuredContent numbers into float64.
				// Exact numerical bytes are checked below through the raw handler.
				if tc.object {
					if _, ok := result.StructuredContent.(map[string]any); !ok {
						t.Fatal("object result lost")
					}
				} else {
					if envelope, ok := result.StructuredContent.(map[string]any); !ok || len(envelope) != 1 {
						t.Fatal("result envelope missing")
					}
				}
				request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: input}}
				unmarshaled, err := runtime.catalog.handler(tc.name)(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				structured, err := json.Marshal(unmarshaled.StructuredContent)
				want := string(local)
				if !tc.object {
					want = `{"result":` + want + `}`
				}
				if err != nil || string(structured) != want {
					t.Fatalf("structured bytes=%s, %v; want %s", structured, err, want)
				}
				requestBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{"index":%d}}}`, tc.name, index)
				called := legacy(requestBody)
				t.Logf("legacy_call_response=%s", called)
				var actual struct {
					StructuredContent json.RawMessage
					IsError           bool
					Content           []struct{ Type, Text string }
				}
				if err := json.Unmarshal(called, &actual); err != nil {
					t.Fatal(err)
				}
				if actual.IsError || string(actual.StructuredContent) != want || len(actual.Content) != 1 || actual.Content[0].Text != string(local) {
					t.Fatalf("legacy HTTP output does not preserve local bytes: %s", called)
				}
			}
			again, _ := definitions[i].Descriptor()
			if !bytes.Equal(again.OutputSchemaJSON, descriptor.OutputSchemaJSON) {
				t.Fatal("original descriptor mutated")
			}
		})
	}
}

func legacyOutputClient(t *testing.T, ctx context.Context, endpoint, token string) func(string) json.RawMessage {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	post := func(body string) json.RawMessage {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
		if err != nil {
			t.Fatal("cannot construct legacy loopback request")
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2025-06-18")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("legacy loopback request failed")
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("legacy loopback response failed")
		}
		var envelope struct {
			Result json.RawMessage
			Error  json.RawMessage
		}
		if json.Unmarshal(raw, &envelope) != nil || len(envelope.Error) != 0 || len(envelope.Result) == 0 {
			t.Fatal("legacy response is not a successful JSON-RPC response")
		}
		return envelope.Result
	}
	initialized := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"output-fixture","version":"1"}}}`)
	var result struct{ ProtocolVersion string }
	if json.Unmarshal(initialized, &result) != nil || result.ProtocolVersion != "2025-06-18" {
		t.Fatal("legacy protocol not negotiated")
	}
	return post
}

func TestOutputProjectionChangesOnlyRootResourceIdentity(t *testing.T) {
	for _, tc := range outputCases() {
		if tc.object || tc.schema == "true" || tc.schema == "false" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			original := json.RawMessage(tc.schema)
			projected, err := projectOutput(original)
			if err != nil {
				t.Fatal(err)
			}
			var wrapper struct {
				Properties map[string]map[string]json.RawMessage
			}
			if err := json.Unmarshal(projected.schema, &wrapper); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(original, &fields); err != nil {
				t.Fatal(err)
			}
			for key, raw := range fields {
				if key == "$id" {
					continue
				}
				if !bytes.Equal(wrapper.Properties["result"][key], raw) {
					t.Fatalf("schema/data field %q rewritten", key)
				}
			}
			if tc.name == "authority_id" && string(wrapper.Properties["result"]["$id"]) != `"https://schema.invalid/original.json"` {
				t.Fatal("explicit author authority lost")
			}
			if tc.name == "absolute_id" && !bytes.Equal(wrapper.Properties["result"]["$id"], fields["$id"]) {
				t.Fatal("absolute root identity changed")
			}
			repeated, err := projectOutput(original)
			if err != nil || !bytes.Equal(projected.schema, repeated.schema) || string(original) != tc.schema {
				t.Fatal("projection is unstable or mutates source")
			}
		})
	}
}

type rejectOutputSchemaLoader struct{ t *testing.T }

func (l rejectOutputSchemaLoader) Load(string) (any, error) {
	l.t.Error("schema projection attempted external resource loading")
	return nil, errors.New("external schema loading forbidden")
}

func compileOutputSchema(t *testing.T, raw []byte) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(rejectOutputSchemaLoader{t})
	if err := c.AddResource("output-schema.json", outputJSONValue(t, raw)); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("output-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func outputJSONValue(t *testing.T, raw []byte) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestHostedOutputFingerprintTracksWireSchema(t *testing.T) {
	for _, schema := range []string{`{"type":"object"}`, `{"type":"string"}`, `true`} {
		t.Run(schema, func(t *testing.T) {
			definition := tool.Define("result", "Return output.", func(context.Context, struct{}) (any, error) { return nil, nil }, tool.OutputSchemaJSON([]byte(schema)), tool.Revision("v1"))
			descriptor, err := definition.Descriptor()
			if err != nil {
				t.Fatal(err)
			}
			original, err := catalogFingerprint([]tool.Descriptor{descriptor})
			if err != nil {
				t.Fatal(err)
			}
			_, actual, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig())
			if err != nil {
				t.Fatal(err)
			}
			if (actual == original) != (schema == `{"type":"object"}`) {
				t.Fatalf("wire identity not reflected: original=%s actual=%s", original, actual)
			}
			_, repeat, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig())
			if err != nil || repeat != actual {
				t.Fatal("wire fingerprint unstable")
			}
		})
	}
}

func TestHostedOutputBoundAppliesBeforeFixedEnvelope(t *testing.T) {
	for _, raw := range []string{`null`, `"` + strings.Repeat("x", 1022) + `"`} {
		for _, limit := range []int{len(raw), len(raw) - 1} {
			t.Run(fmt.Sprintf("%d/%d", len(raw), limit), func(t *testing.T) {
				definition := tool.Define("bounded", "Return bounded output.", func(context.Context, struct{}) (json.RawMessage, error) { return json.RawMessage(raw), nil }, tool.OutputSchemaJSON([]byte(`true`)))
				cfg := testGatewayConfig()
				cfg.maxResponseBytes = limit
				catalog, _, err := newRuntimeCatalog([]tool.Definition{definition}, cfg)
				if err != nil {
					t.Fatal(err)
				}
				result, err := catalog.handler("bounded")(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
				if err != nil {
					t.Fatal(err)
				}
				if limit < len(raw) {
					requireSafeToolFailure(t, result, "internal_error")
					return
				}
				got, err := json.Marshal(result.StructuredContent)
				if err != nil || string(got) != `{"result":`+raw+`}` || len(got) != limit+11 {
					t.Fatalf("valid boundary output changed: %s, %v", got, err)
				}
			})
		}
	}
}

func TestHostedDirectBooleanProperties(t *testing.T) {
	for _, side := range []string{"input", "output"} {
		for _, constraint := range []string{"true", "false"} {
			t.Run(side+"/"+constraint, func(t *testing.T) {
				original := `{"type":"object","properties":{"x":` + constraint + `,"nested":{"type":"object","properties":{"y":false}}},"examples":[{"literal":{"properties":{"x":true}}}],"default":{"properties":{"x":false}}}`
				options := []tool.Option{tool.OutputSchemaJSON([]byte(`{"type":"object"}`))}
				if side == "input" {
					options = append(options, tool.InputSchemaJSON([]byte(original)))
				} else {
					options = append(options, tool.OutputSchemaJSON([]byte(original)))
				}
				definition := tool.Define("bool_property", "Return an object.", func(context.Context, map[string]any) (map[string]any, error) {
					return map[string]any{"literal": map[string]any{"properties": map[string]any{"x": true}}}, nil
				}, options...)
				before, err := definition.Descriptor()
				if err != nil {
					t.Fatal(err)
				}
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
				legacy := legacyOutputClient(t, ctx, endpoint.URL, runtimeToken(t, runtime))
				listed := legacy(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
				t.Logf("legacy_list_response=%s", listed)
				var listing struct {
					Tools []struct{ InputSchema, OutputSchema json.RawMessage }
				}
				if err := json.Unmarshal(listed, &listing); err != nil {
					t.Fatal(err)
				}
				wire, public := listing.Tools[0].OutputSchema, before.OutputSchemaJSON
				if side == "input" {
					wire, public = listing.Tools[0].InputSchema, before.InputSchemaJSON
				}
				var actual, expected map[string]json.RawMessage
				_ = json.Unmarshal(wire, &actual)
				_ = json.Unmarshal(public, &expected)
				var props map[string]json.RawMessage
				_ = json.Unmarshal(actual["properties"], &props)
				if string(props["x"]) != `{"allOf":[`+constraint+`]}` {
					t.Fatalf("direct boolean schema not adapted: %s", wire)
				}
				var oldProps map[string]json.RawMessage
				_ = json.Unmarshal(expected["properties"], &oldProps)
				if !bytes.Equal(props["nested"], oldProps["nested"]) {
					t.Fatal("deeper schema modified")
				}
				for key, value := range expected {
					if key != "properties" && !bytes.Equal(actual[key], value) {
						t.Fatalf("data/schema field %s changed", key)
					}
				}
				oldValidator, newValidator := compileOutputSchema(t, public), compileOutputSchema(t, wire)
				for _, raw := range []string{`{"literal":{"properties":{"x":true}}}`, `{"x":null}`, `{}`} {
					value := outputJSONValue(t, []byte(raw))
					if (oldValidator.Validate(value) == nil) != (newValidator.Validate(value) == nil) {
						t.Fatal("boolean constraint changed")
					}
				}
				oldFingerprint, _ := catalogFingerprint([]tool.Descriptor{before})
				_, newFingerprint, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig())
				if err != nil || oldFingerprint == newFingerprint {
					t.Fatal("changed wire schema missing from fingerprint")
				}
				after, _ := definition.Descriptor()
				if !bytes.Equal(before.InputSchemaJSON, after.InputSchemaJSON) || !bytes.Equal(before.OutputSchemaJSON, after.OutputSchemaJSON) {
					t.Fatal("public descriptor changed")
				}
				if runtime.catalog.outputs["bool_property"].wrapped {
					t.Fatal("object output unnecessarily enveloped")
				}
				called := legacy(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"bool_property","arguments":{"literal":{"properties":{"x":true}}}}}`)
				t.Logf("legacy_call_response=%s", called)
				var result struct {
					StructuredContent json.RawMessage
					Content           []struct{ Text string }
					IsError           bool
				}
				_ = json.Unmarshal(called, &result)
				if result.IsError || string(result.StructuredContent) != `{"literal":{"properties":{"x":true}}}` || len(result.Content) != 1 || result.Content[0].Text != string(result.StructuredContent) {
					t.Fatalf("object output/text changed: %s", called)
				}
			})
		}
	}
}

func TestProjectedRelativeIDsRetainDistinctResources(t *testing.T) {
	for _, id := range []string{"../shared.json", "/shared.json"} {
		t.Run(id, func(t *testing.T) {
			projections := make([]json.RawMessage, 0, 2)
			for _, kind := range []string{"string", "number"} {
				original := json.RawMessage(fmt.Sprintf(`{"$id":%q,"$defs":{"value":{"type":%q}},"$ref":"#/$defs/value"}`, id, kind))
				projected, err := projectOutput(original)
				if err != nil {
					t.Fatal(err)
				}
				projections = append(projections, projected.schema)
			}
			composite, _ := json.Marshal(map[string]any{"oneOf": projections})
			validator := compileOutputSchema(t, composite)
			for _, raw := range []string{`{"result":"ok"}`, `{"result":42}`} {
				if err := validator.Validate(outputJSONValue(t, []byte(raw))); err != nil {
					t.Fatalf("distinct resources collided: %v", err)
				}
			}
			if validator.Validate(outputJSONValue(t, []byte(`{"result":false}`))) == nil {
				t.Fatal("composite constraint lost")
			}
		})
	}
}

func TestWrappedOutputFailuresHaveNoStructuredSuccess(t *testing.T) {
	for _, kind := range []string{"handler_error", "panic", "rejection", "invalid_output", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			schema := `true`
			if kind == "invalid_output" {
				schema = `false`
			}
			definition := tool.Define("failure", "Fail without a structured value.", func(context.Context, struct{}) (string, error) {
				calls++
				switch kind {
				case "handler_error":
					return "", errors.New("HANDLER_SECRET")
				case "panic":
					panic("HANDLER_SECRET")
				case "rejection":
					return "", tool.Reject("retry", "Try again.")
				default:
					return "OUTPUT_SECRET", nil
				}
			}, tool.OutputSchemaJSON([]byte(schema)))
			catalog, _, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			code := "internal_error"
			if kind == "canceled" {
				cancel()
				code = "canceled"
			}
			if kind == "rejection" {
				code = "retry"
			}
			result, err := catalog.handler("failure")(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
			if err != nil {
				t.Fatal(err)
			}
			requireSafeToolFailure(t, result, code)
			want := 1
			if kind == "canceled" {
				want = 0
			}
			if calls != want {
				t.Fatalf("handler calls=%d, want %d", calls, want)
			}
		})
	}
}

func TestHostedOutputStillRejectsExternalReferences(t *testing.T) {
	for _, keyword := range []string{"$ref", "$dynamicRef", "$recursiveRef"} {
		t.Run(keyword, func(t *testing.T) {
			schema := fmt.Sprintf(`{"%s":"https://schema.invalid/private"}`, keyword)
			definition := tool.Define("external", "Reject external references.", func(context.Context, struct{}) (string, error) { t.Fatal("invalid definition invoked"); return "", nil }, tool.OutputSchemaJSON([]byte(schema)))
			if _, err := definition.Descriptor(); !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Fatalf("definition error=%v", err)
			}
			if _, _, err := newRuntimeCatalog([]tool.Definition{definition}, testGatewayConfig()); !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("catalog error=%v", err)
			}
		})
	}
}
