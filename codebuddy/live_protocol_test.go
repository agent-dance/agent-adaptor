//go:build codebuddy_live

package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

// Only used with the fixed, non-secret live Question fixture. Do not dump Raw:
// system messages and unrelated tools may contain private profile information.
func logLiveQuestionProtocol(t *testing.T, result *adaptor.Result) {
	t.Helper()
	if result == nil {
		return
	}
	searches := map[string]bool{}
	for _, line := range strings.Split(result.Raw().Stdout, "\n") {
		var frame map[string]any
		if json.Unmarshal([]byte(line), &frame) != nil {
			continue
		}
		switch frame["type"] {
		case "system":
			if frame["subtype"] == "init" {
				tools, _ := json.Marshal(frame["tools"])
				t.Logf("[question protocol] declared tools=%s", tools)
			}
		case "control_request":
			request, _ := frame["request"].(map[string]any)
			t.Logf("[question protocol] control subtype=%v tool=%v", request["subtype"], request["tool_name"])
		case "assistant", "user":
			message, _ := frame["message"].(map[string]any)
			content, _ := message["content"].([]any)
			for _, item := range content {
				block, _ := item.(map[string]any)
				if block["type"] == "tool_use" && block["name"] == "ToolSearch" {
					id, _ := block["id"].(string)
					searches[id] = true
				}
				id, _ := block["tool_use_id"].(string)
				if block["type"] == "tool_result" && searches[id] {
					content, _ := json.Marshal(block["content"])
					t.Logf("[question protocol] ToolSearch result=%s", truncateForLog(string(content)))
				}
			}
		case "result":
			t.Logf("[question protocol] terminal subtype=%v is_error=%v", frame["subtype"], frame["is_error"])
		}
	}
}
