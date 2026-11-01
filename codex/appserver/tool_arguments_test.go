package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestOfficialToolNotificationsPreserveArgumentSnapshots(t *testing.T) {
	for _, kind := range []ThreadItemKind{ThreadItemMcpToolCall, ThreadItemDynamicToolCall} {
		for _, tc := range []struct {
			name string
			raw  string
			want map[string]any
		}{
			{name: "object", raw: ` { "member" : "worker", "nested": {"values":[1,true,null,{"text":"value"}]}, "literal":"{\"member\":\"decoy\"}", "arguments":"user field" } `, want: map[string]any{
				"member": "worker", "nested": map[string]any{"values": []any{float64(1), true, nil, map[string]any{"text": "value"}}}, "literal": `{"member":"decoy"}`, "arguments": "user field",
			}},
			{name: "empty object", raw: `{}`, want: map[string]any{}},
			{name: "omitted"},
			{name: "null", raw: `null`, want: map[string]any{"arguments": nil}},
			{name: "empty string", raw: `""`, want: map[string]any{"arguments": ""}},
			{name: "JSON-looking string", raw: `"{\"member\":\"decoy\"}"`, want: map[string]any{"arguments": `{"member":"decoy"}`}},
			{name: "number", raw: `42`, want: map[string]any{"arguments": float64(42)}},
			{name: "boolean", raw: `true`, want: map[string]any{"arguments": true}},
			{name: "array", raw: `[1,{"member":"worker"}]`, want: map[string]any{"arguments": []any{float64(1), map[string]any{"member": "worker"}}}},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				arguments := ""
				if tc.raw != "" {
					arguments = `,"arguments":` + tc.raw
				}
				itemJSON := json.RawMessage(fmt.Sprintf(`{"id":"tool-1","type":%q,"server":"team","tool":"delegate","status":"inProgress"%s}`, kind, arguments))
				params := json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":` + string(itemJSON) + `}`)
				original := append(json.RawMessage(nil), params...)
				sink := &recordingSink{}
				translator := NewTranslator(sink, "run-1")
				translator.Dispatch(NotifyTurnStarted, json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}`))
				translator.Dispatch(NotifyItemStarted, params)
				translator.Dispatch(NotifyItemCompleted, params)
				translator.Dispatch(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
				var start *driver.StreamPayload
				for i := range sink.streams {
					if sink.streams[i].Kind == driver.StreamToolCallStart {
						if start != nil {
							t.Fatal("multiple tool openings")
						}
						start = &sink.streams[i]
					}
				}
				if start == nil || !reflect.DeepEqual(start.Args, tc.want) {
					t.Fatalf("initial arguments=%+v, want %+v", start, tc.want)
				}
				wantName := "delegate"
				if kind == ThreadItemMcpToolCall {
					wantName = "team/delegate"
				}
				if start.ToolCallID != "tool-1" || start.Name != wantName || start.RunID != "run-1" || start.ThreadID != "thread-1" || start.TurnID != "turn-1" {
					t.Fatalf("tool identity=%+v", start)
				}
				// Exercise the public Event conversion used by Team consumers.
				stream := adaptor.New(payloadDriver{payloads: sink.streams}).Send(context.Background(), "tool notification")
				publicStarts := 0
				for event := range stream.Events() {
					if call, ok := event.(adaptor.ToolCall); ok && call.Phase == adaptor.PhaseStart {
						publicStarts++
						if call.Name != wantName || !reflect.DeepEqual(call.Args, tc.want) {
							t.Fatalf("public ToolCall=%+v", call)
						}
					}
				}
				if _, err := stream.Result(); err != nil || publicStarts != 1 {
					t.Fatalf("public starts=%d err=%v", publicStarts, err)
				}
				// Normalization owns decoded values, never the provider's raw JSON.
				if !bytes.Equal(params, original) {
					t.Fatalf("notification changed: %s", params)
				}
				decoded, err := DecodeThreadItem(itemJSON)
				if err != nil || !bytes.Equal(decoded.Raw, itemJSON) {
					t.Fatalf("raw item changed: %+v err=%v", decoded, err)
				}
				if tc.name == "object" {
					start.Args["member"] = "mutated"
					start.Args["nested"].(map[string]any)["values"] = nil
					if !bytes.Equal(params, original) || !bytes.Equal(decoded.Raw, itemJSON) {
						t.Fatal("argument mutation changed provider raw data")
					}
					var body ItemStartedNotificationBody
					if err := json.Unmarshal(params, &body); err != nil {
						t.Fatal(err)
					}
					again, err := DecodeThreadItem(body.Item)
					if err != nil {
						t.Fatal(err)
					}
					var raw json.RawMessage
					if kind == ThreadItemMcpToolCall {
						raw = again.McpToolCall.Arguments
					} else {
						raw = again.DynamicToolCall.Arguments
					}
					if !reflect.DeepEqual(toolArguments(raw), tc.want) {
						t.Fatal("provider argument snapshot changed")
					}
				}
			})
		}
	}
}
