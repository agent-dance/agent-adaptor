package team

import (
	"encoding/json"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// taskUpdate adds observed presentation details without publishing the full
// Result, its raw provider payloads, or host runtime metadata.
func (run *execution) taskUpdate(task *taskRun, kind adaptor.SubagentEventKind) adaptor.SubagentUpdate {
	member := run.team.members[task.record.Member]
	name := member.Name
	if name == "" {
		name = member.Key
	}
	protocol := "local"
	if member.Remote != nil {
		protocol = "a2a"
	}
	data := map[string]any{
		"agent_name": name, "remote_protocol": protocol,
		"description": task.record.Objective, "reason": task.record.Reason,
		"time": task.record.UpdatedAt,
	}
	if kind == adaptor.SubagentFinished {
		if result := task.record.Result; result != nil {
			data["text"] = result.Text
			observed := map[string]any{
				"text": result.Text, "summary": result.Summary,
				"model": result.Model, "provider": result.Provider,
			}
			// Decode uses validated structured output when the invocation had a
			// schema. Otherwise its documented Text convenience decode applies.
			var output json.RawMessage
			if result.Decode(&output) == nil {
				observed["output"] = output
			}
			data["result"] = observed
		}
		if task.record.State != Succeeded {
			data["error"] = map[string]any{"code": task.record.Reason, "message": task.record.Summary}
		}
	}
	return adaptor.SubagentUpdate{
		Member: task.record.Member, Task: task.record.ID,
		State: string(task.record.State), Kind: kind,
		Summary: task.record.Summary, Data: data,
	}
}
