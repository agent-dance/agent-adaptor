package todoobs

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/todo"
)

func TestTableClearOrderReplay(t *testing.T) {
	tr, e := NewTable(Scope{ID: "nested", ParentToolCallID: "parent"})
	if e != nil {
		t.Fatal(e)
	}
	if _, seen := tr.Snapshot(); seen {
		t.Fatal("unobserved table reported")
	}
	at := time.Now().UTC()
	out, e := tr.Replace(nil, todo.PlanUpdate, at)
	if e != nil || out.Items == nil || out.Revision != 1 {
		t.Fatal(out, e)
	}
	if out, e = tr.Replace([]todo.Item{}, todo.PlanUpdate, at); e != nil || out != nil {
		t.Fatal("duplicate clear")
	}
	items := []todo.Item{{ID: "b", Content: "中文\n任务", Status: todo.Pending}, {ID: "a", Content: "second", Status: todo.InProgress}}
	out, e = tr.Replace(items, todo.ToolResult, at)
	if e != nil || out.Revision != 2 || out.Items[0].ID != "b" {
		t.Fatal(e)
	}
	items[0].Content = "caller"
	out.Items[0].Content = "consumer"
	snapshot, _ := tr.Snapshot()
	if snapshot.Items[0].Content != "中文\n任务" || snapshot.ScopeID != "nested" || snapshot.ParentToolCallID != "parent" {
		t.Fatal(snapshot)
	}
	done := todo.Completed
	out, e = tr.Update("a", Patch{Status: &done}, todo.ToolResult, at)
	if e != nil || out.Items[1].Status != done || out.Revision != 3 {
		t.Fatal(out, e)
	}
}
func TestTableSyntheticUnknownAndAtomic(t *testing.T) {
	tr, _ := NewTable(Scope{})
	at := time.Now().UTC()
	item := todo.Item{ID: "synthetic:opaque", Content: "work", Status: todo.Pending, SyntheticID: true}
	if _, e := tr.Create(item, todo.ToolResult, at); e != nil {
		t.Fatal(e)
	}
	done := todo.Completed
	for _, id := range []string{"1", item.ID} {
		if _, e := tr.Update(id, Patch{Status: &done}, todo.ToolResult, at); !errors.Is(e, ErrUnknownID) {
			t.Fatal(e)
		}
	}
	for _, items := range [][]todo.Item{{{ID: "a", Content: "valid", Status: todo.Pending}, {ID: "b", Content: "bad", Status: "unknown"}}, {{ID: "a", Content: "bad\xff", Status: todo.Pending}}, {{ID: "a", Content: strings.Repeat("中", 1366), Status: todo.Pending}}, {{ID: "a", Content: "", Status: todo.Pending}}, {{ID: "a", Content: "x", Status: todo.Pending}, {ID: "a", Content: "y", Status: todo.Pending}}} {
		if _, e := tr.Replace(items, todo.ToolResult, at); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		s, _ := tr.Snapshot()
		if len(s.Items) != 1 || s.Items[0] != item || s.Revision != 1 {
			t.Fatal("partial mutation", s)
		}
	}
	if out, e := tr.Create(item, todo.ToolResult, at); e != nil || out != nil {
		t.Fatal("create replay")
	}
	item.Content = "conflict"
	if _, e := tr.Create(item, todo.ToolResult, at); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
func TestTableConcurrentScopes(t *testing.T) {
	a, _ := NewTable(Scope{ID: "a"})
	b, _ := NewTable(Scope{ID: "b"})
	var wg sync.WaitGroup
	for n := range 24 {
		wg.Go(func() {
			for _, tr := range []*Table{a, b} {
				if _, e := tr.Create(todo.Item{ID: fmt.Sprint(n), Content: "work", Status: todo.Pending}, todo.ToolResult, time.Now().UTC()); e != nil {
					t.Error(e)
				}
			}
		})
	}
	wg.Wait()
	for _, tr := range []*Table{a, b} {
		s, seen := tr.Snapshot()
		if !seen || len(s.Items) != 24 || s.Revision != 24 {
			t.Fatal(s)
		}
	}
}
