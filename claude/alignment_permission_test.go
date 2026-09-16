package claude_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// A successful Bash result is not evidence of an approval: Claude may allow
// read-only commands before consulting its stdio permission handler.
func validateClaudePermissionProtocol(stdout, approvedToolID string) error {
	if approvedToolID == "" {
		return fmt.Errorf("missing approved tool ID")
	}
	var calls, requests, results, terminals int
	var callAt, requestAt, resultAt, terminalAt int
	for index, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var frame struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			IsReplay  bool   `json:"isReplay"`
			IsError   *bool  `json:"is_error"`
			Request   struct {
				Subtype   string `json:"subtype"`
				ToolName  string `json:"tool_name"`
				ToolUseID string `json:"tool_use_id"`
				Input     struct {
					Command string `json:"command"`
				} `json:"input"`
			} `json:"request"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return fmt.Errorf("invalid formal stdout frame %d: %w", index, err)
		}
		if frame.IsReplay {
			continue
		}
		switch frame.Type {
		case "control_request":
			requests++
			requestAt = index + 1
			if frame.RequestID == "" || frame.Request.Subtype != "can_use_tool" || frame.Request.ToolName != "Bash" || frame.Request.ToolUseID != approvedToolID || frame.Request.Input.Command != "echo permission-ok" {
				return fmt.Errorf("permission request does not match the approved Bash call")
			}
		case "assistant", "user":
			var content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				ToolUseID string `json:"tool_use_id"`
				IsError   *bool  `json:"is_error"`
				Content   string `json:"content"`
				Input     struct {
					Command string `json:"command"`
				} `json:"input"`
			}
			if err := json.Unmarshal(frame.Message.Content, &content); err != nil {
				return fmt.Errorf("invalid tool message: %w", err)
			}
			for _, block := range content {
				if frame.Type == "assistant" && block.Type == "tool_use" {
					calls++
					callAt = index + 1
					if block.Name != "Bash" || block.ID != approvedToolID || block.Input.Command != "echo permission-ok" {
						return fmt.Errorf("unexpected tool call in permission probe")
					}
				}
				if frame.Type == "user" && block.Type == "tool_result" {
					results++
					resultAt = index + 1
					if block.ToolUseID != approvedToolID || block.IsError == nil || *block.IsError || strings.TrimSpace(block.Content) != "permission-ok" {
						return fmt.Errorf("missing successful result for the approved Bash call")
					}
				}
			}
		case "result":
			terminals++
			terminalAt = index + 1
			if frame.Subtype != "success" || frame.IsError == nil || *frame.IsError {
				return fmt.Errorf("permission probe did not finish successfully")
			}
		}
	}
	if calls != 1 || requests != 1 || results != 1 || terminals != 1 || callAt >= resultAt || requestAt >= resultAt || resultAt >= terminalAt {
		return fmt.Errorf("permission protocol incomplete or duplicated: calls=%d requests=%d results=%d terminals=%d", calls, requests, results, terminals)
	}
	return nil
}

func assertLivePermissionProtocol(t *testing.T, result *adaptor.Result, approvedToolID string) {
	t.Helper()
	if result == nil || result.Raw().Terminal == nil {
		t.Fatal("permission probe lost its formal terminal")
	}
	if err := validateClaudePermissionProtocol(result.Raw().Stdout, approvedToolID); err != nil {
		t.Fatal(err)
	}
	calls, results := 0, 0
	for _, item := range result.Transcript() {
		if item.ToolUseID != approvedToolID {
			continue
		}
		switch string(item.Kind) {
		case "tool_call":
			input, _ := item.Input.(map[string]any)
			if item.ToolName == "Bash" && input["command"] == "echo permission-ok" {
				calls++
			}
		case "tool_result":
			if !item.IsError && strings.TrimSpace(item.Text) == "permission-ok" {
				results++
			}
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("permission Transcript correlation: calls=%d results=%d", calls, results)
	}
}

func TestAlignmentClaudePermissionEvidenceRequiresActualRequest(t *testing.T) {
	// This is the shape observed in the original 2.1.159 failure: Bash really
	// ran and returned the expected result without requesting permission.
	call := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"bash-1","name":"Bash","input":{"command":"echo permission-ok"}}]}}` + "\n"
	request := `{"type":"control_request","request_id":"permission-1","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"bash-1","input":{"command":"echo permission-ok"}}}` + "\n"
	result := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bash-1","content":"permission-ok","is_error":false}]}}` + "\n"
	terminal := `{"type":"result","subtype":"success","is_error":false,"result":"PERMISSION_OK"}` + "\n"
	for _, tc := range []struct {
		name, raw, approvedID string
		valid                 bool
	}{
		{"approved", call + request + result + terminal, "bash-1", true},
		{"auto_allowed_original", call + result + terminal, "bash-1", false},
		{"no_callback", call + request + result + terminal, "", false},
		{"different_tool", call + request + result + terminal, "other", false},
		{"duplicate_request", call + request + request + result + terminal, "bash-1", false},
		{"failed_result", call + request + strings.Replace(result, `"is_error":false`, `"is_error":true`, 1) + terminal, "bash-1", false},
		{"result_before_approval", call + result + request + terminal, "bash-1", false},
		{"missing_terminal", call + request + result, "bash-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateClaudePermissionProtocol(tc.raw, tc.approvedID); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}
