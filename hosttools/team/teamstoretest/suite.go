// Package teamstoretest provides deterministic conformance tests for durable
// team.TaskStore implementations. Factories must return a fresh empty store.
package teamstoretest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

// Run checks fencing, exclusive claims, idempotency, CAS, atomic task/wake
// commits, crash reclaim, detached round trips and fork lineage. Expiry uses
// nanosecond TTLs, never sleeps or scheduling assumptions. Store timestamps
// must have sufficient precision to honor the public time.Duration contract.
func Run(t *testing.T, newStore func(*testing.T) team.TaskStore) {
	t.Helper()
	ctx := context.Background()
	lease := func(t *testing.T, s team.TaskStore, key string) team.ScopeLease {
		t.Helper()
		l, e := s.AcquireScope(ctx, key, "owner", time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		return l
	}
	must := func(t *testing.T, e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	conflict := func(t *testing.T, e error) {
		t.Helper()
		if !errors.Is(e, team.ErrConflict) {
			t.Fatalf("error=%v, want ErrConflict", e)
		}
	}
	t.Run("ScopeFencing", func(t *testing.T) {
		s := newStore(t)
		l := lease(t, s, "scope")
		if _, e := s.AcquireScope(ctx, l.Scope, "other", time.Hour); !errors.Is(e, team.ErrThreadBusy) {
			t.Fatalf("acquire=%v", e)
		}
		bad := l
		bad.Owner = "other"
		_, e := s.RenewScope(ctx, bad, time.Hour)
		conflict(t, e)
		bad = l
		bad.Token = "stale"
		conflict(t, s.ReleaseScope(ctx, bad))
		renewed, e := s.RenewScope(ctx, l, time.Hour)
		must(t, e)
		if renewed.Token != l.Token {
			t.Fatal("renew changed fencing token")
		}
		must(t, s.ReleaseScope(ctx, l))
		next := lease(t, s, l.Scope)
		if next.Token == l.Token {
			t.Fatal("reused token")
		}
		conflict(t, s.PutMessage(ctx, l, team.InboxMessage{Scope: l.Scope, ID: "old"}))
	})
	t.Run("ConcurrentClaimsAndAcks", func(t *testing.T) {
		s := newStore(t)
		l := lease(t, s, "scope")
		m := team.InboxMessage{Scope: l.Scope, ID: "id", Text: "original", Wake: true, Host: true, From: adaptor.Author{Kind: adaptor.AuthorHuman, ID: "caller"}, Timing: adaptor.AfterTurn}
		must(t, s.PutMessage(ctx, l, m))
		must(t, s.PutMessage(ctx, l, m))
		different := m
		different.Text = "changed"
		conflict(t, s.PutMessage(ctx, l, different))
		begin := make(chan struct{})
		out := make(chan []team.InboxMessage, 8)
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-begin
				c, e := s.ClaimMessages(ctx, l, true, time.Hour)
				out <- c
				errs <- e
			}()
		}
		close(begin)
		wg.Wait()
		close(out)
		close(errs)
		var all []team.InboxMessage
		for c := range out {
			all = append(all, c...)
		}
		for e := range errs {
			must(t, e)
		}
		if len(all) != 1 || all[0].ClaimToken == "" {
			t.Fatalf("claims=%+v", all)
		}
		conflict(t, s.FinishMessage(ctx, l, m.ID, "wrong", team.Delivered, ""))
		must(t, s.FinishMessage(ctx, l, m.ID, all[0].ClaimToken, team.Delivered, ""))
		conflict(t, s.FinishMessage(ctx, l, m.ID, all[0].ClaimToken, team.DeliveryFailed, "late"))
		records, e := s.Messages(ctx, l.Scope)
		must(t, e)
		if len(records) != 1 || records[0].State != team.Delivered || records[0].From != m.From || records[0].Timing != m.Timing || !records[0].Host {
			t.Fatalf("messages=%+v", records)
		}
	})
	t.Run("CrashReclaim", func(t *testing.T) {
		s := newStore(t)
		l := lease(t, s, "scope")
		must(t, s.PutMessage(ctx, l, team.InboxMessage{Scope: l.Scope, ID: "id", Wake: true}))
		first, e := s.ClaimMessages(ctx, l, true, time.Nanosecond)
		must(t, e)
		if len(first) != 1 {
			t.Fatal(first)
		}
		next, e := s.ClaimMessages(ctx, l, true, time.Hour)
		must(t, e)
		if len(next) != 1 || next[0].ClaimToken == first[0].ClaimToken {
			t.Fatalf("reclaim=%+v", next)
		}
		conflict(t, s.FinishMessage(ctx, l, "id", first[0].ClaimToken, team.Delivered, ""))
		must(t, s.ReleaseScope(ctx, l))
		expired, e := s.AcquireScope(ctx, l.Scope, "crashed", time.Nanosecond)
		must(t, e)
		recovered := lease(t, s, l.Scope)
		conflict(t, s.ReleaseScope(ctx, expired))
		claims, e := s.ClaimMessages(ctx, recovered, true, time.Hour)
		must(t, e)
		if len(claims) != 1 || !claims[0].MaybeDuplicate {
			t.Fatalf("recovered=%+v", claims)
		}
	})
	t.Run("TaskRoundTripAndAtomicWake", func(t *testing.T) {
		s := newStore(t)
		l := lease(t, s, "scope")
		original := team.Task{Scope: l.Scope, ID: "task", Member: "member", Objective: "work", Input: map[string]any{"key": "value"}, Conversation: team.EncodeKey(l.Scope, "member", "member"), State: team.Queued, RemoteTask: "remote-task", RemoteContext: "remote-context", CreatedAt: time.Now().UTC()}
		original.RemoteSnapshot = &clienta2a.Task{ID: "remote-task", Artifacts: []clienta2a.Artifact{{ID: "artifact", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"key": "value"}}}}}}
		saved, e := s.SaveTask(ctx, l, original)
		must(t, e)
		if saved.Revision != 1 {
			t.Fatal(saved)
		}
		_, e = s.SaveTask(ctx, l, original)
		conflict(t, e)
		saved.State = team.Running
		saved, e = s.SaveTask(ctx, l, saved)
		must(t, e)
		notification := team.InboxMessage{Scope: l.Scope, ID: "wake", Text: "done", Wake: true}
		saved.State = team.Succeeded
		saved.Summary = "done"
		saved.Usage = &adaptor.Usage{InputTokens: 7}
		saved.Result = &adaptor.Result{Text: "answer", Usage: &adaptor.Usage{InputTokens: 7}, Metadata: map[string]string{"key": "value"}}
		terminal, e := s.SaveTask(ctx, l, saved, notification)
		must(t, e)
		// A conflicting notification must prevent both mutations.
		changed := terminal
		changed.Summary = "must not commit"
		notification.Text = "conflict"
		_, e = s.SaveTask(ctx, l, changed, notification)
		conflict(t, e)
		records, e := s.Tasks(ctx, l.Scope)
		must(t, e)
		if len(records) != 1 || !reflect.DeepEqual(records[0], terminal) {
			t.Fatalf("roundtrip=%+v want=%+v", records, terminal)
		}
		records[0].Input["key"] = "mutated"
		records[0].Result.Metadata["key"] = "mutated"
		records[0].Usage.InputTokens = 100
		records[0].RemoteSnapshot.Artifacts[0].Parts[0].Data.(map[string]any)["key"] = "changed"
		again, e := s.Tasks(ctx, l.Scope)
		must(t, e)
		if !reflect.DeepEqual(again[0], terminal) {
			t.Fatal("store leaked mutable task state")
		}
		messages, e := s.Messages(ctx, l.Scope)
		must(t, e)
		if len(messages) != 1 || messages[0].Text != "done" {
			t.Fatalf("messages=%+v", messages)
		}
		child := lease(t, s, "branch")
		must(t, s.ForkScope(ctx, l, child))
		parent, e := s.ForkParent(ctx, child.Scope)
		must(t, e)
		if parent != l.Scope {
			t.Fatal(parent)
		}
		copied, e := s.Tasks(ctx, child.Scope)
		must(t, e)
		if len(copied) != 1 || copied[0].Scope != child.Scope || copied[0].Conversation != team.EncodeKey(child.Scope, "member", "member") {
			t.Fatalf("copy=%+v", copied)
		}
		inbox, e := s.Messages(ctx, child.Scope)
		must(t, e)
		if len(inbox) != 0 {
			t.Fatal("fork copied inbox")
		}
		conflict(t, s.ForkScope(ctx, l, child))
	})
}
