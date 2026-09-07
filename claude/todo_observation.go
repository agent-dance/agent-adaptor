package claude

import (
	"errors"
	"strconv"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/todoobs"
	"github.com/agent-dance/agent-adaptor/todo"
)

// Structured tool outputs are read only from the matching SDK user wrapper.
// Text results are never recursively decoded or searched for task IDs.
func (p *claudeParser) observeTodo(call *observedTool, structured any) {
	switch call.name {
	case "TaskCreate", "TaskUpdate", "TaskList", "TodoWrite":
	default:
		return
	}
	o := p.observations()
	table := o.tables[call.scope.id]
	if table == nil {
		var err error
		table, err = todoobs.NewTable(todoobs.Scope{ID: call.scope.id, ParentScopeID: call.scope.parentScope, ParentToolCallID: call.scope.parentID})
		if err != nil {
			p.observationNotice("todo_invalid")
			return
		}
		o.tables[call.scope.id] = table
	}
	output, outputOK := structured.(map[string]any)
	if structured != nil && !outputOK {
		p.observationNotice("todo_invalid")
		return
	}
	if success, exists := output["success"]; exists && success != true {
		p.observationNotice("todo_invalid")
		return
	}
	now := time.Now().UTC()
	var snapshot *todo.Snapshot
	var err error
	if tasks, exists := output["tasks"]; exists {
		var items []todo.Item
		items, err = claudeTaskList(tasks)
		if err == nil {
			snapshot, err = table.Replace(items, todo.ToolResult, now)
		}
	} else {
		switch call.name {
		case "TaskCreate":
			task, taskOK := output["task"].(map[string]any)
			if _, exists := output["task"]; exists && !taskOK {
				p.observationNotice("todo_invalid")
				return
			}
			if value, exists := task["id"]; exists {
				if _, ok := value.(string); !ok {
					p.observationNotice("todo_invalid")
					return
				}
			}
			id := claudeExactString(task, "id")
			synthetic := id == ""
			if synthetic {
				id = "synthetic:" + claudeTuple(p.stream.runID, call.scope.id, call.key.id)
			}
			// Creation is confirmed by the successful result, not by local ordinal.
			content := claudeExactString(call.input, "subject")
			if subject, ok := task["subject"].(string); ok {
				content = subject
			}
			snapshot, err = table.Create(todo.Item{ID: id, Content: content, Status: todo.Pending, SyntheticID: synthetic}, todo.ToolResult, now)
		case "TaskUpdate":
			id := claudeExactString(call.input, "taskId")
			patch := todoobs.Patch{}
			if raw, exists := call.input["subject"]; exists {
				value, ok := raw.(string)
				if !ok {
					err = todoobs.ErrInvalid
				} else {
					patch.Content = &value
				}
			}
			if raw, exists := call.input["status"]; exists {
				value, ok := raw.(string)
				if !ok {
					err = todoobs.ErrInvalid
				} else {
					if value == "deleted" {
						snapshot, err = deleteClaudeTask(table, id, now)
						break
					}
					status := todo.Status(value)
					patch.Status = &status
				}
			}
			if success, exists := output["success"]; exists && success != true {
				err = todoobs.ErrInvalid
			}
			if err == nil {
				snapshot, err = table.Update(id, patch, todo.ToolResult, now)
			}
		case "TodoWrite":
			var items []todo.Item
			items, err = claudeTodoWrite(call.input["todos"], p.stream.runID, call.scope.id)
			if err == nil {
				snapshot, err = table.Replace(items, todo.ToolResult, now)
			}
		case "TaskList":
			err = todoobs.ErrInvalid
		}
	}
	if err != nil {
		code := "todo_invalid"
		if errors.Is(err, todoobs.ErrUnknownID) {
			code = "todo_unknown_id"
		}
		p.observationNotice(code)
		return
	}
	if snapshot != nil {
		p.stream.emitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: snapshot})
	}
}

func claudeTaskList(raw any) ([]todo.Item, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, todoobs.ErrInvalid
	}
	items := make([]todo.Item, 0, len(list))
	for _, entry := range list {
		task, ok := entry.(map[string]any)
		if !ok {
			return nil, todoobs.ErrInvalid
		}
		items = append(items, todo.Item{ID: claudeExactString(task, "id"), Content: claudeExactString(task, "subject"), Status: todo.Status(claudeExactString(task, "status"))})
	}
	return items, nil
}

func claudeTodoWrite(raw any, runID, scopeID string) ([]todo.Item, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, todoobs.ErrInvalid
	}
	items := make([]todo.Item, 0, len(list))
	for i, entry := range list {
		task, ok := entry.(map[string]any)
		if !ok {
			return nil, todoobs.ErrInvalid
		}
		id := claudeExactString(task, "id")
		synthetic := id == ""
		if synthetic {
			id = "synthetic:" + claudeTuple(runID, scopeID, "TodoWrite", strconv.Itoa(i))
		}
		items = append(items, todo.Item{ID: id, Content: claudeExactString(task, "content"), Status: todo.Status(claudeExactString(task, "status")), SyntheticID: synthetic})
	}
	return items, nil
}

func deleteClaudeTask(table *todoobs.Table, id string, at time.Time) (*todo.Snapshot, error) {
	snapshot, ok := table.Snapshot()
	if !ok {
		return nil, todoobs.ErrUnknownID
	}
	for i, item := range snapshot.Items {
		if item.ID == id && !item.SyntheticID {
			return table.Replace(append(snapshot.Items[:i], snapshot.Items[i+1:]...), todo.ToolResult, at)
		}
	}
	return nil, todoobs.ErrUnknownID
}
