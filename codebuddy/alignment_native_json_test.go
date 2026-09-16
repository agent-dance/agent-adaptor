package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestAlignmentCodeBuddyNativeJSONDocument(t *testing.T) {
	terminal := `{"type":"result","subtype":"success","is_error":false,"session_id":"native-session","result":"","structured_output":{"ok":true},"usage":{"input_tokens":7,"output_tokens":3}}`
	history := `{"type":"function_call","name":"StructuredOutput","callId":"native-call","arguments":"{\"ok\":true}"}`
	pretty := func(s string) string {
		t.Helper()
		var value any
		if err := json.Unmarshal([]byte(s), &value); err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(value, "", "  ")
		return string(b) + "\n"
	}
	for _, tc := range []struct {
		name, stdout    string
		exit            int
		valid, terminal bool
	}{
		{"official pretty history array", pretty("[" + history + "," + terminal + "]"), 0, true, true},
		{"compact array", "[" + history + "," + terminal + "]\n", 0, true, true},
		{"single result", terminal + "\n", 0, true, true},
		{"pretty result", pretty(terminal), 0, true, true},
		{"legacy newline objects", history + "\n" + terminal + "\n", 0, true, true},
		{"nonzero array", pretty("[" + terminal + "]"), 17, false, true},
		{"provider refusal", `[{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"native-session","errors":["fixture refusal"]}]`, 0, false, true},
		{"missing structured field", `[{"type":"result","subtype":"success","is_error":false,"session_id":"native-session","result":"{\"ok\":true}"}]`, 0, false, true},
		{"null structured field", strings.Replace("["+terminal+"]", `"structured_output":{"ok":true}`, `"structured_output":null`, 1), 0, false, true},
		{"wrong schema type", strings.Replace("["+terminal+"]", `"ok":true`, `"ok":"true"`, 1), 0, false, true},
		{"missing session", strings.Replace("["+terminal+"]", `"session_id":"native-session",`, "", 1), 0, false, true},
		{"duplicate result", "[" + terminal + "," + terminal + "]", 0, false, true},
		{"error after success", "[" + terminal + `,{"type":"error","message":"late failure"}]`, 0, false, true},
		{"unknown after success", "[" + terminal + `,{"type":"untrusted"}]`, 0, false, true},
		{"nested result is not terminal", `[{"type":"message","role":"user","content":` + terminal + `}]`, 0, false, false},
		{"nested array is not terminal", "[[" + terminal + "]]", 0, false, false},
		{"empty array", "[]", 0, false, false},
		{"null document", "null", 0, false, false},
		{"truncated array", "[" + terminal, 0, false, false},
		{"array then another document", "[" + terminal + "]\n" + terminal, 0, false, false},
		{"invalid UTF8", "[" + history + ",\"\xff\"," + terminal + "]", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "protocol.json"), []byte(tc.stdout), 0600); err != nil {
				t.Fatal(err)
			}
			command := testutil.WriteCommand(t, home, "native-json",
				fmt.Sprintf("#!/bin/sh\ncat protocol.json\nexit %d\n", tc.exit),
				fmt.Sprintf("@echo off\r\ntype protocol.json\r\nexit /b %d\r\n", tc.exit))
			recorder := &testutil.EventRecorder{}
			req := driver.Request{Prompt: "native fixture", Config: isolatedCodeBuddyConfig(command, home), Workspace: driver.WorkspaceLease{CWD: home},
				Policy: autoApprovePolicy(), StructuredOutputSource: driver.StructuredOutputSourceNative,
				OutputSchema: &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, OnInvalid: driver.StructuredOutputFailRun,
					SchemaJSON: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)}}
			response, err := (adapter{}).Run(context.Background(), req, recorder)
			if err != nil {
				t.Fatalf("Driver infrastructure error: %v", err)
			}
			if response.RawStreams == nil || response.RawStreams.Stdout != tc.stdout {
				t.Fatal("native Raw stdout changed")
			}
			if response.ExitCode != tc.exit {
				t.Fatalf("exit=%d want=%d", response.ExitCode, tc.exit)
			}
			if (response.RawStreams.Terminal != nil) != tc.terminal {
				t.Fatalf("terminal=%+v", response.RawStreams.Terminal)
			}
			if tc.valid {
				if response.Failure != nil || response.StructuredOutput == nil || !response.StructuredOutput.Valid || string(response.StructuredOutput.RawJSON) != `{"ok":true}` || response.Checkpoint == nil || !response.Checkpoint.Valid {
					t.Fatalf("native success missing: %+v", response)
				}
				if response.Output != "" || response.Usage == nil || response.Usage.InputTokens != 7 || response.Usage.OutputTokens != 3 {
					t.Fatalf("output/usage changed: %+v", response)
				}
				if len(response.Transcript) == 0 || response.Transcript[len(response.Transcript)-1].Kind != driver.TranscriptResult {
					t.Fatal("formal result transcript missing")
				}
			} else {
				if response.Checkpoint != nil || response.StructuredOutput != nil && response.StructuredOutput.Valid {
					t.Fatal("unhealthy/invalid document accepted")
				}
				if tc.exit == 0 && response.Failure == nil {
					t.Fatal("malformed/refused/missing native output lost failure")
				}
			}
		})
	}
}
