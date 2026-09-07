package todo_test

import (
	"github.com/agent-dance/agent-adaptor/todo"
	"testing"
)

func TestSnapshotClearAndSyntheticIdentity(t *testing.T) {
	s := todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 1}
	if s.Items == nil || len(s.Items) != 0 {
		t.Fatal("clear snapshot must be explicit")
	}
	item := todo.Item{ID: "synthetic:real-is-distinct", SyntheticID: true, Status: todo.Pending}
	if !item.SyntheticID || todo.Pending == todo.Completed {
		t.Fatal("identity/state lost")
	}
}
