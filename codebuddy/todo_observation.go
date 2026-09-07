package codebuddy

import (
	"strconv"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/todoobs"
	"github.com/agent-dance/agent-adaptor/todo"
)

// CodeBuddy 2.137.1's official stream-json serializer places the tool's
// rawResponse in tool_result._meta.rawResponse. Task* results contain todos
// (the full list), with task for create/update. TodoWrite confirms newTodos.
func (p *parser) confirmTodo(id string, call *observedCall, result map[string]any) {
	meta := topObject(result, "_meta")
	raw := topObject(meta, "rawResponse")
	if value, exists := result["_meta"]; exists && value != nil && meta == nil {
		p.publishTodo(nil, todoobs.ErrInvalid)
		return
	}
	if value, exists := meta["rawResponse"]; exists && (value == nil || raw == nil) {
		p.publishTodo(nil, todoobs.ErrInvalid)
		return
	}
	now := time.Now().UTC()
	if values, exists := raw["todos"]; exists {
		items, err := p.todoItems(values)
		if err != nil {
			p.publishTodo(nil, err)
			return
		}
		p.publishTodo(p.observation.table.Replace(items, todo.ToolResult, now))
		return
	}
	switch call.name {
	case "TodoWrite":
		if resultText(result["content"]) != "Todo list updated successfully" {
			p.publishTodo(nil, todoobs.ErrInvalid)
			return
		}
		values, ok := call.input["newTodos"]
		if !ok {
			p.publishTodo(nil, todoobs.ErrInvalid)
			return
		}
		items, err := p.todoItems(values)
		if err != nil {
			p.publishTodo(nil, err)
			return
		}
		p.publishTodo(p.observation.table.Replace(items, todo.ToolResult, now))
	case "TaskCreate":
		subject := exactString(call.input, "subject")
		task := topObject(raw, "task")
		if _, exists := raw["task"]; exists && task == nil {
			p.publishTodo(nil, todoobs.ErrInvalid)
			return
		}
		taskID := exactString(task, "id")
		if value, exists := task["id"]; exists {
			if _, ok := value.(string); !ok || taskID == "" {
				p.publishTodo(nil, todoobs.ErrInvalid)
				return
			}
		}
		// Older captures without _meta still carry this exact official rendering.
		if taskID == "" {
			prefix, ok := strings.CutPrefix(resultText(result["content"]), "Task #")
			if ok {
				candidate, suffix, found := strings.Cut(prefix, " created successfully: ")
				if found && suffix == subject {
					taskID = candidate
				}
			}
		}
		if taskID == "" && task == nil {
			p.publishTodo(nil, todoobs.ErrInvalid)
			return
		}
		synthetic := taskID == ""
		if synthetic {
			taskID = syntheticTodoID(p.runID, id, "")
		}
		content := subject
		status := todo.Pending
		if task != nil {
			if v, exists := task["subject"]; exists {
				var ok bool
				content, ok = v.(string)
				if !ok {
					p.publishTodo(nil, todoobs.ErrInvalid)
					return
				}
			}
			if v, exists := task["status"]; exists {
				s, ok := v.(string)
				if !ok {
					p.publishTodo(nil, todoobs.ErrInvalid)
					return
				}
				status = todo.Status(s)
			}
		}
		p.publishTodo(p.observation.table.Create(todo.Item{ID: taskID, Content: content, Status: status, SyntheticID: synthetic}, todo.ToolResult, now))
	case "TaskUpdate":
		taskID := exactString(call.input, "taskId")
		confirmed := call.input
		if value, exists := raw["task"]; exists {
			task, ok := value.(map[string]any)
			if !ok || exactString(task, "id") != taskID {
				p.publishTodo(nil, todoobs.ErrInvalid)
				return
			}
			confirmed = task
		} else if !confirmedTaskUpdateText(call.input, result["content"]) {
			// Metadata presence is not proof. Without task/todos, only the
			// exact official success rendering can confirm an input patch.
			p.publishTodo(nil, todoobs.ErrInvalid)
			return
		}
		patch := todoobs.Patch{}
		if value, exists := confirmed["subject"]; exists {
			s, ok := value.(string)
			if !ok {
				p.publishTodo(nil, todoobs.ErrInvalid)
				return
			}
			patch.Content = &s
		}
		if value, exists := confirmed["status"]; exists {
			s, ok := value.(string)
			if !ok {
				p.publishTodo(nil, todoobs.ErrInvalid)
				return
			}
			v := todo.Status(s)
			patch.Status = &v
		}
		p.publishTodo(p.observation.table.Update(taskID, patch, todo.ToolResult, now))
	case "TaskList":
		p.publishTodo(nil, todoobs.ErrInvalid)
	}
}
func (p *parser) todoItems(raw any) ([]todo.Item, error) {
	values, ok := raw.([]any)
	if !ok {
		return nil, todoobs.ErrInvalid
	}
	items := make([]todo.Item, 0, len(values))
	for i, value := range values {
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, todoobs.ErrInvalid
		}
		content, ok := obj["content"].(string)
		if !ok {
			return nil, todoobs.ErrInvalid
		}
		status, ok := obj["status"].(string)
		if !ok {
			return nil, todoobs.ErrInvalid
		}
		id, hasID := obj["id"]
		item := todo.Item{Content: content, Status: todo.Status(status)}
		if hasID {
			item.ID, ok = id.(string)
			if !ok || item.ID == "" {
				return nil, todoobs.ErrInvalid
			}
		} else {
			item.ID = syntheticTodoID(p.runID, "snapshot", strconv.Itoa(i))
			item.SyntheticID = true
		}
		items = append(items, item)
	}
	return items, nil
}

// TaskUpdateTool.execute renders these provided fields in this fixed order.
// Unknown additive input fields have no effect on the official rendering.
func confirmedTaskUpdateText(input map[string]any, content any) bool {
	fields := make([]string, 0, 8)
	for _, field := range []string{"subject", "description", "activeForm", "owner", "metadata", "addBlocks", "addBlockedBy", "status"} {
		value, exists := input[field]
		if !exists {
			continue
		}
		name := field
		switch field {
		case "metadata":
			if _, ok := value.(map[string]any); !ok {
				return false
			}
		case "addBlocks", "addBlockedBy":
			if _, ok := value.([]any); !ok {
				return false
			}
			if field == "addBlocks" {
				name = "blocks"
			} else {
				name = "blockedBy"
			}
		default:
			text, ok := value.(string)
			if !ok {
				return false
			}
			if text == "" {
				continue
			}
		}
		fields = append(fields, name)
	}
	return len(fields) != 0 && resultText(content) == "Updated task #"+exactString(input, "taskId")+" "+strings.Join(fields, ", ")
}
