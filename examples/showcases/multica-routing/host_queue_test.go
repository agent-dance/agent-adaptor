package main

import (
	"reflect"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func queuedComment(thread, id, target string) hostMessage {
	return hostMessage{
		Thread: thread, ID: id, Text: "comment " + id, Target: target,
		Author: adaptor.Author{Kind: adaptor.AuthorHuman, ID: "person"}, Kind: "comment",
	}
}

func admitComment(t *testing.T, q *hostQueue, msg hostMessage) {
	t.Helper()
	if accepted, err := q.Admit(msg); err != nil || !accepted {
		t.Fatalf("Admit(%+v) = %v, %v", msg, accepted, err)
	}
}

func TestHostQueuePriorityIsStableAndTargetsSurvive(t *testing.T) {
	q := newHostQueue([]string{"reviewer", "implementer"})
	for _, msg := range []hostMessage{
		queuedComment("thread", "ordinary-1", "reviewer"),
		queuedComment("thread", "ordinary-2", "implementer"),
		func() hostMessage { m := queuedComment("thread", "urgent-1", "leader"); m.Priority = true; return m }(),
		queuedComment("thread", "ordinary-3", "leader"),
		func() hostMessage { m := queuedComment("thread", "urgent-2", "reviewer"); m.Priority = true; return m }(),
	} {
		admitComment(t, q, msg)
	}
	var actual []string
	for {
		msg, ok := q.Pop("thread")
		if !ok {
			break
		}
		actual = append(actual, msg.ID+":"+msg.Target)
	}
	want := []string{"urgent-1:leader", "urgent-2:reviewer", "ordinary-1:reviewer", "ordinary-2:implementer", "ordinary-3:leader"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("routed order = %v, want %v", actual, want)
	}
}

func TestHostQueueWithdrawAndIDsAreThreadLocal(t *testing.T) {
	q := newHostQueue([]string{"worker"})
	a := queuedComment("a", "same", "worker")
	b := queuedComment("b", "same", "leader")
	b.Text = "a different discussion"
	b.Priority = true
	admitComment(t, q, a)
	admitComment(t, q, b)
	if q.Withdraw("missing", "same") || !q.Withdraw("a", "same") || q.Withdraw("a", "same") {
		t.Fatal("withdrawal crossed a thread or removed an ID twice")
	}
	if _, ok := q.Pop("a"); ok {
		t.Fatal("withdrawn comment remained queued")
	}
	if accepted, err := q.Admit(a); accepted || err != nil {
		t.Fatalf("withdrawn ID was resurrected: %v, %v", accepted, err)
	}
	got, ok := q.Pop("b")
	if !ok || got != b || q.Withdraw("b", "same") {
		t.Fatalf("other thread's comment was changed or recalled after dispatch: %+v, %v", got, ok)
	}
	if accepted, err := q.Admit(b); accepted || err != nil {
		t.Fatalf("dispatched ID was queued twice: %v, %v", accepted, err)
	}
}

func TestHostQueueDuplicateContentCannotChangeRoutingOrAuthority(t *testing.T) {
	q := newHostQueue([]string{"worker"})
	original := queuedComment("thread", "id", "worker")
	admitComment(t, q, original)
	if accepted, err := q.Admit(original); accepted || err != nil {
		t.Fatalf("identical duplicate = %v, %v", accepted, err)
	}
	for _, change := range []func(*hostMessage){
		func(m *hostMessage) { m.Text = "different instructions" },
		func(m *hostMessage) { m.Target = "leader" },
		func(m *hostMessage) { m.Author.ID = "different person" },
		func(m *hostMessage) { m.Author.Name = "different name" },
		func(m *hostMessage) { m.Priority = true },
	} {
		conflict := original
		change(&conflict)
		if accepted, err := q.Admit(conflict); accepted || err == nil {
			t.Fatalf("conflicting duplicate accepted: %+v; %v, %v", conflict, accepted, err)
		}
	}
	got, ok := q.Pop("thread")
	if !ok || got != original {
		t.Fatalf("conflict changed admitted comment: %+v, %v", got, ok)
	}
	if _, ok := q.Pop("thread"); ok {
		t.Fatal("duplicates added another queue entry")
	}
}

func TestHostQueueRejectsMalformedOrImpersonatedCommentsWithoutMutation(t *testing.T) {
	for name, change := range map[string]func(*hostMessage){
		"empty thread":   func(m *hostMessage) { m.Thread = "" },
		"blank ID":       func(m *hostMessage) { m.ID = " \t" },
		"blank text":     func(m *hostMessage) { m.Text = "\n\t " },
		"unknown target": func(m *hostMessage) { m.Target = "other-worker" },
		"padded target":  func(m *hostMessage) { m.Target = " worker" },
		"wrong case":     func(m *hostMessage) { m.Target = "Worker" },
		"wrong kind":     func(m *hostMessage) { m.Kind = "system" },
		"zero author":    func(m *hostMessage) { m.Author.Kind = 0 },
		"agent author":   func(m *hostMessage) { m.Author.Kind = adaptor.AuthorAgent },
		"system author":  func(m *hostMessage) { m.Author.Kind = adaptor.AuthorSystem },
	} {
		t.Run(name, func(t *testing.T) {
			q := newHostQueue([]string{"worker"})
			valid := queuedComment("thread", "id", "worker")
			invalid := valid
			change(&invalid)
			if accepted, err := q.Admit(invalid); accepted || err == nil {
				t.Fatalf("invalid comment accepted: %+v; %v, %v", invalid, accepted, err)
			}
			if _, ok := q.Pop("thread"); ok {
				t.Fatal("rejected comment was routed")
			}
			admitComment(t, q, valid)
			if got, ok := q.Pop("thread"); !ok || got != valid {
				t.Fatalf("rejection poisoned a valid admission: %+v, %v", got, ok)
			}
		})
	}
}

func TestHostQueuePreservesExactUnicodeAndOpaqueIDs(t *testing.T) {
	q := newHostQueue([]string{"审阅者"})
	original := hostMessage{
		Thread: " 讨论:甲/乙 ", ID: " id:零/一\x00 ", Text: "  请保留原文：便便 😀\n下一行  ",
		Target: "审阅者", Author: adaptor.Author{Kind: adaptor.AuthorHuman, ID: "用户:甲", Name: "姓名 😀"}, Kind: "comment",
	}
	admitComment(t, q, original)
	if _, ok := q.Pop("讨论:甲/乙"); ok {
		t.Fatal("opaque thread key was trimmed")
	}
	if q.Withdraw(original.Thread, "id:零/一\x00") {
		t.Fatal("opaque message ID was trimmed")
	}
	if got, ok := q.Pop(original.Thread); !ok || got != original {
		t.Fatalf("Unicode content or opaque identifiers changed: %+v, %v", got, ok)
	}
}

func TestHostQueueDefaultTargetIsLeader(t *testing.T) {
	q := newHostQueue(nil)
	msg := queuedComment("thread", "id", "")
	msg.Kind = ""
	admitComment(t, q, msg)
	msg.Target, msg.Kind = "leader", "comment"
	if accepted, err := q.Admit(msg); accepted || err != nil {
		t.Fatalf("equivalent default routing was not deduplicated: %v, %v", accepted, err)
	}
	if got, ok := q.Pop("thread"); !ok || got != msg {
		t.Fatalf("default comment routing = %+v, %v", got, ok)
	}
}

func TestEncodedHostThreadKeysCannotCollide(t *testing.T) {
	pairs := [][2]string{
		{"a:b", "c"}, {"a", "b:c"}, {"", ""}, {"workspace", "discussion"},
		{"😀", "甲:乙"}, {"😀:甲", "乙"}, {"ab", "c"}, {"a", "bc"},
		{"a\x00b", "c"}, {"a", "b\x00c"}, {"工作区", "讨论"}, {" 工作区", "讨论"},
	}
	keys := make(map[string][2]string)
	q := newHostQueue(nil)
	for _, pair := range pairs {
		key := encodeThreadKey(pair[0], pair[1])
		if previous, exists := keys[key]; exists {
			t.Fatalf("thread key collision between %q and %q", previous, pair)
		}
		keys[key] = pair
		if key != encodeThreadKey(pair[0], pair[1]) {
			t.Fatal("thread encoding is unstable")
		}
		admitComment(t, q, queuedComment(key, "same-id", "leader"))
	}
	for key := range keys {
		if got, ok := q.Pop(key); !ok || got.Thread != key {
			t.Fatalf("encoded discussion was misrouted: %+v, %v", got, ok)
		}
	}
}

func TestHostQueueConcurrentDuplicateAdmissionOccursOnce(t *testing.T) {
	q := newHostQueue([]string{"worker"})
	msg := queuedComment("thread", "id", "worker")
	var admissions sync.WaitGroup
	accepted := make(chan bool, 32)
	errors := make(chan error, 32)
	for range 32 {
		admissions.Go(func() {
			ok, err := q.Admit(msg)
			accepted <- ok
			errors <- err
		})
	}
	admissions.Wait()
	close(accepted)
	close(errors)
	count := 0
	for ok := range accepted {
		if ok {
			count++
		}
	}
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, ok := q.Pop("thread"); count != 1 || !ok || got != msg {
		t.Fatalf("concurrent duplicate admissions=%d, item=%+v, present=%v", count, got, ok)
	}
	if _, ok := q.Pop("thread"); ok {
		t.Fatal("concurrent duplicate created multiple deliveries")
	}
}
