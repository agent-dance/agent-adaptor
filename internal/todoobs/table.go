// Package todoobs maintains confirmed normalized snapshots, never provider
// JSON, tool names, argument acceptance, or inferred tool success.
package todoobs

import (
	"errors"
	"reflect"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/todo"
)

type Scope struct {
	ID               string
	ParentScopeID    string
	ParentToolCallID string
}
type Patch struct {
	Content *string
	Status  *todo.Status
}
type Table struct {
	mu       sync.Mutex
	scope    Scope
	observed bool
	snapshot todo.Snapshot
}

var ErrInvalid = errors.New("todoobs: invalid value")
var ErrUnknownID = errors.New("todoobs: unknown id")
var ErrConflict = errors.New("todoobs: conflicting id")

func validText(s string, n int, required, content bool) bool {
	if len(s) > n || (required && s == "") || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(content && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}
func validScope(s Scope) bool {
	return validText(s.ID, 2048, false, false) && validText(s.ParentScopeID, 2048, false, false) && validText(s.ParentToolCallID, 2048, false, false) && (s.ParentToolCallID != "" || s.ParentScopeID == "")
}
func validSource(s todo.Source, at time.Time) bool {
	_, off := at.Zone()
	return (s == todo.ToolResult || s == todo.PlanUpdate) && !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 && off == 0
}
func validStatus(s todo.Status) bool {
	return s == todo.Pending || s == todo.InProgress || s == todo.Completed || s == todo.Cancelled
}
func validateItems(items []todo.Item) bool {
	if len(items) > 128 {
		return false
	}
	ids := map[string]bool{}
	for _, i := range items {
		if !validText(i.ID, 2048, true, false) || !validText(i.Content, 4096, true, true) || !validStatus(i.Status) || ids[i.ID] {
			return false
		}
		ids[i.ID] = true
	}
	return true
}
func Validate(s todo.Snapshot) error {
	if !validScope(Scope{s.ScopeID, s.ParentScopeID, s.ParentToolCallID}) || !validSource(s.Source, s.OccurredAt) || !validateItems(s.Items) || s.Revision == 0 {
		return ErrInvalid
	}
	return nil
}
func Clone(s todo.Snapshot) todo.Snapshot { s.Items = append([]todo.Item{}, s.Items...); return s }
func NewTable(s Scope) (*Table, error) {
	if !validScope(s) {
		return nil, ErrInvalid
	}
	return &Table{scope: s}, nil
}
func (t *Table) Create(item todo.Item, source todo.Source, at time.Time) (*todo.Snapshot, error) {
	if !validateItems([]todo.Item{item}) || !validSource(source, at) {
		return nil, ErrInvalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, old := range t.snapshot.Items {
		if old.ID == item.ID {
			if old == item {
				return nil, nil
			}
			return nil, ErrConflict
		}
	}
	return t.replace(append(append([]todo.Item{}, t.snapshot.Items...), item), source, at)
}
func (t *Table) Update(id string, p Patch, source todo.Source, at time.Time) (*todo.Snapshot, error) {
	if p.Content == nil && p.Status == nil || !validSource(source, at) {
		return nil, ErrInvalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	items := append([]todo.Item{}, t.snapshot.Items...)
	for n, old := range items {
		if old.ID == id && !old.SyntheticID {
			if p.Content != nil {
				items[n].Content = *p.Content
			}
			if p.Status != nil {
				items[n].Status = *p.Status
			}
			return t.replace(items, source, at)
		}
	}
	return nil, ErrUnknownID
}
func (t *Table) Replace(items []todo.Item, source todo.Source, at time.Time) (*todo.Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.replace(items, source, at)
}
func (t *Table) replace(items []todo.Item, source todo.Source, at time.Time) (*todo.Snapshot, error) {
	if !validSource(source, at) || !validateItems(items) {
		return nil, ErrInvalid
	}
	items = append([]todo.Item{}, items...)
	if t.observed && t.snapshot.Source == source && reflect.DeepEqual(t.snapshot.Items, items) {
		return nil, nil
	}
	if t.snapshot.Revision == ^uint64(0) {
		return nil, ErrInvalid
	}
	t.snapshot = todo.Snapshot{Items: items, Source: source, ScopeID: t.scope.ID, ParentScopeID: t.scope.ParentScopeID, ParentToolCallID: t.scope.ParentToolCallID, Revision: t.snapshot.Revision + 1, OccurredAt: at}
	t.observed = true
	s := Clone(t.snapshot)
	return &s, nil
}
func (t *Table) Snapshot() (todo.Snapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Clone(t.snapshot), t.observed
}
