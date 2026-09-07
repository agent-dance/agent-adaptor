package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"testing"
)

func TestAlignmentNativeAppendArgs(t *testing.T) {
	text := " 甲\n\"乙\" "
	for _, control := range []bool{false, true} {
		args := buildExecArgs(Config{}, driver.Request{AppendSystemPrompt: text}, PermissionUnset, control)
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--append-system-prompt" && args[i+1] == text {
				found = true
			}
		}
		if !found {
			t.Errorf("native append missing for control=%v", control)
		}
	}
	d := Driver(Config{}).Descriptor()
	if !d.SystemPrompt.Append || !d.Observation.Streaming.Skills || !d.Observation.Streaming.MCP || !d.Observation.Streaming.Subagents || !d.Observation.Streaming.Todos {
		t.Error("native append/observation support undeclared")
	}
}
