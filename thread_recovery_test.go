package adaptor_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

func TestThreadContinuityResumeRejectedAndConfigurationChanged(t *testing.T) {
	for _, cause := range []string{"resume rejected", "configuration changed"} {
		t.Run(cause, func(t *testing.T) {
			store := memory.NewStore()
			fake := messageFake(driver.MessageCapability{})
			calls := 0
			fake.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
				calls++
				if calls <= 4 {
					return messageCheckpoint([]string{"oldest discarded", "recent two", "recent three", "recent four"}[calls-1]), nil
				}
				if cause == "resume rejected" && req.Session.State != nil {
					return driver.Response{RawStreams: &driver.RawStreams{Stdout: "resume rejected\n"}}, &engine.ResumeRejectedError{Reason: "session expired"}
				}
				if req.Session.State != nil {
					t.Errorf("fallback still resumes: %+v", req.Session)
				}
				for _, text := range []string{"recent two", "recent three", "recent four", "new request"} {
					if !strings.Contains(req.Prompt, text) {
						t.Errorf("continuity preamble missing %q: %q", text, req.Prompt)
					}
				}
				if strings.Contains(req.Prompt, "oldest discarded") {
					t.Errorf("more than three recent turns retained: %q", req.Prompt)
				}
				return messageCheckpoint("fresh answer"), nil
			}
			agent := adaptor.New(fake, adaptor.WithThreadStore(store))
			thread := agent.Thread("continuity")
			for index := 0; index < 4; index++ {
				if _, err := thread.Send(context.Background(), "seed").Result(); err != nil {
					t.Fatal(err)
				}
			}
			var options []adaptor.CallOption
			if cause == "configuration changed" {
				options = append(options, adaptor.WithModel("different"))
			}
			stream := thread.Send(context.Background(), "new request", options...)
			result, err := stream.Result()
			if err != nil {
				t.Fatal(err)
			}
			if cause == "resume rejected" && !strings.Contains(result.Raw().Stdout, "resume rejected") {
				t.Fatal("fresh-session fallback discarded the rejected attempt's raw output")
			}
			notices := 0
			for _, event := range fullEvents(stream) {
				if notice, ok := event.(adaptor.Notice); ok && notice.Kind == "thread.continuity" {
					notices++
					if cause == "resume rejected" && !strings.Contains(notice.Text, "session expired") {
						t.Fatalf("notice lost cause: %+v", notice)
					}
				}
			}
			if notices != 1 {
				t.Fatalf("continuity notices=%d", notices)
			}
			if err := agent.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThreadReplaysProviderEphemeralGuidanceAcrossExecutions(t *testing.T) {
	for _, persists := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "persisted"}[persists], func(t *testing.T) {
			fake, calls := controlledStandbyDriver()
			fake.descriptor.Messages.PersistsDelivered = persists
			store := memory.NewStore()
			agent := adaptor.New(fake, adaptor.WithThreadStore(store))
			thread := agent.Thread("guidance")
			stream := thread.Send(context.Background(), "goal")
			first := standbyReceive(t, calls)
			if err := stream.Send(context.Background(), "keep the API stable", adaptor.MessageID("guidance"), adaptor.From(adaptor.Author{Kind: adaptor.AuthorAgent, Name: "reviewer"})); err != nil {
				t.Fatal(err)
			}
			claimed := first.request.Messages.Claim(driver.BoundaryTool)
			if len(claimed) != 1 {
				t.Fatal(claimed)
			}
			first.request.Messages.Ack(claimed[0].ID, driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
			first.reply <- standbyReply{response: messageCheckpoint("first answer")}
			if _, err := stream.Result(); err != nil {
				t.Fatal(err)
			}
			record := activeRecord(t, store, "guidance")
			if (len(record.EarlierGuidance) > 0) == persists {
				t.Fatalf("guidance=%v persists=%t", record.EarlierGuidance, persists)
			}
			nextStream := thread.Send(context.Background(), "continue")
			next := standbyReceive(t, calls)
			if strings.Contains(next.request.Prompt, "keep the API stable") == persists {
				t.Fatalf("replay=%q persists=%t", next.request.Prompt, persists)
			}
			if !persists && (!strings.Contains(next.request.Prompt, "Earlier guidance") || !strings.Contains(next.request.Prompt, "Peer agent input")) {
				t.Fatalf("guidance lost authority framing: %q", next.request.Prompt)
			}
			next.reply <- standbyReply{response: messageCheckpoint("second answer")}
			if _, err := nextStream.Result(); err != nil {
				t.Fatal(err)
			}
			_ = agent.Close(context.Background())
		})
	}
}

func TestThreadFailureRetainsDeliveredGuidanceWithoutAdvancingCheckpoint(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	store := memory.NewStore()
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	thread := agent.Thread("failed-guidance")
	seed := thread.Send(context.Background(), "seed")
	first := standbyReceive(t, calls)
	first.reply <- standbyReply{response: checkpointNamed("healthy")}
	if _, err := seed.Result(); err != nil {
		t.Fatal(err)
	}
	stream := thread.Send(context.Background(), "failing execution")
	call := standbyReceive(t, calls)
	if err := stream.Send(context.Background(), "guidance survives failure", adaptor.MessageID("guidance")); err != nil {
		t.Fatal(err)
	}
	items := call.request.Messages.Claim(driver.BoundaryTool)
	if len(items) != 1 {
		t.Fatal(items)
	}
	call.request.Messages.Ack(items[0].ID, driver.MessageAck{Delivered: true, Mechanism: "test.tool"})
	call.reply <- standbyReply{response: driver.Response{Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "failed"}}}
	if _, err := stream.Result(); err == nil {
		t.Fatal("business failure hidden")
	}
	record := activeRecord(t, store, "failed-guidance")
	if record.State.ResumeID != "healthy" || len(record.EarlierGuidance) != 1 {
		t.Fatalf("failure advanced checkpoint or lost guidance: %+v", record)
	}
	nextStream := thread.Send(context.Background(), "continue")
	next := standbyReceive(t, calls)
	if !strings.Contains(next.request.Prompt, "guidance survives failure") || next.request.Session.State.ResumeID != "healthy" {
		t.Fatalf("failed execution lost delivered guidance: %+v", next.request)
	}
	next.reply <- standbyReply{response: checkpointNamed("recovered")}
	if _, err := nextStream.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadContinuityHistoryIsBoundedAndKeepsNewest(t *testing.T) {
	store := memory.NewStore()
	fake := messageFake(driver.MessageCapability{})
	fake.response = messageCheckpoint(strings.Repeat("界", 5000) + "latest-tail")
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	thread := agent.Thread("bounded")
	for index := 0; index < 4; index++ {
		if _, err := thread.Send(context.Background(), "seed").Result(); err != nil {
			t.Fatal(err)
		}
	}
	record := activeRecord(t, store, "bounded")
	size := 0
	for _, text := range append(record.EarlierGuidance, record.RecentTurns...) {
		size += len(text)
		if !utf8.ValidString(text) {
			t.Fatal("truncation split a UTF-8 rune")
		}
	}
	if size > 8*1024 || len(record.RecentTurns) > 3 || !strings.HasSuffix(record.RecentTurns[len(record.RecentTurns)-1], "latest-tail") {
		t.Fatalf("size=%d recent=%v", size, record.RecentTurns)
	}
	_ = agent.Close(context.Background())
}

type flakyCheckpointStore struct {
	threadstore.Store
	mu      sync.Mutex
	failing bool
	saved   chan string
}

func (s *flakyCheckpointStore) Finalize(ctx context.Context, request threadstore.FinalizeRequest) error {
	s.mu.Lock()
	failing := s.failing
	s.mu.Unlock()
	if failing {
		return errors.New("checkpoint backend unavailable")
	}
	if err := s.Store.Finalize(ctx, request); err != nil {
		return err
	}
	s.saved <- request.Record.State.ResumeID
	return nil
}

func (s *flakyCheckpointStore) recover() {
	s.mu.Lock()
	s.failing = false
	s.mu.Unlock()
}

func checkpointNamed(name string) driver.Response {
	response := messageCheckpoint(name)
	response.Checkpoint.State.ResumeID = name
	return response
}

func TestThreadCheckpointPersistenceRetainsNewestAndFlushesBeforeRun(t *testing.T) {
	store := &flakyCheckpointStore{Store: memory.NewStore(), failing: true, saved: make(chan string, 32)}
	fake, calls := controlledStandbyDriver()
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	thread := agent.Thread("flaky")
	for _, name := range []string{"checkpoint-one", "checkpoint-two"} {
		stream := thread.Send(context.Background(), name)
		call := standbyReceive(t, calls)
		if name == "checkpoint-two" && (call.request.Session.State == nil || call.request.Session.State.ResumeID != "checkpoint-one") {
			t.Fatalf("in-process checkpoint not resumed: %+v", call.request.Session)
		}
		call.reply <- standbyReply{response: checkpointNamed(name)}
		if result, err := stream.Result(); err != nil || result == nil {
			t.Fatalf("storage failure poisoned successful run: result=%v error=%v", result, err)
		}
		noticed := false
		for _, event := range fullEvents(stream) {
			if notice, ok := event.(adaptor.Notice); ok && notice.Kind == "thread.persistence" {
				noticed = true
			}
		}
		if !noticed {
			t.Fatal("missing checkpoint persistence notice")
		}
		checkpoint, err := agent.Thread("flaky").Checkpoint(context.Background())
		if err != nil || checkpoint.State.ResumeID != name {
			t.Fatalf("authoritative Checkpoint=%+v error=%v", checkpoint, err)
		}
	}
	store.recover()
	stream := thread.Send(context.Background(), "third")
	call := standbyReceive(t, calls)
	if call.request.Session.State.ResumeID != "checkpoint-two" {
		t.Fatalf("resumed stale checkpoint: %+v", call.request.Session)
	}
	if saved := activeRecord(t, store, "flaky"); saved.State.ResumeID != "checkpoint-two" {
		t.Fatalf("Driver.Run preceded flush: %+v", saved)
	}
	call.reply <- standbyReply{response: checkpointNamed("checkpoint-three")}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadCheckpointPersistenceRetriesInBackground(t *testing.T) {
	store := &flakyCheckpointStore{Store: memory.NewStore(), failing: true, saved: make(chan string, 32)}
	fake := messageFake(driver.MessageCapability{})
	fake.response = checkpointNamed("background-checkpoint")
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	if _, err := agent.Thread("background").Send(context.Background(), "go").Result(); err != nil {
		t.Fatal(err)
	}
	store.recover()
	if saved := standbyReceive(t, store.saved); saved != "background-checkpoint" {
		t.Fatalf("background saved %q", saved)
	}
}

func TestThreadPendingFreshSessionsArchiveDurablePredecessor(t *testing.T) {
	store := &flakyCheckpointStore{Store: memory.NewStore(), saved: make(chan string, 32)}
	fake, calls := controlledStandbyDriver()
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	thread := agent.Thread("pending-rebind")
	seed := thread.Send(context.Background(), "seed")
	first := standbyReceive(t, calls)
	first.reply <- standbyReply{response: checkpointNamed("durable")}
	if _, err := seed.Result(); err != nil {
		t.Fatal(err)
	}
	predecessor := activeRecord(t, store, "pending-rebind")
	store.mu.Lock()
	store.failing = true
	store.mu.Unlock()
	for _, model := range []string{"model-a", "model-b"} {
		stream := thread.Send(context.Background(), model, adaptor.WithModel(model))
		call := standbyReceive(t, calls)
		if call.request.Session.State != nil {
			t.Fatalf("configuration drift reused a checkpoint: %+v", call.request.Session)
		}
		call.reply <- standbyReply{response: checkpointNamed(model)}
		if _, err := stream.Result(); err != nil {
			t.Fatal(err)
		}
	}
	store.recover()
	stream := thread.Send(context.Background(), "flush", adaptor.WithModel("model-b"))
	call := standbyReceive(t, calls)
	if record := activeRecord(t, store, "pending-rebind"); record.State.ResumeID != "model-b" {
		t.Fatalf("flushed an obsolete pending checkpoint: %+v", record)
	}
	old, err := store.Resolve(context.Background(), threadstore.Query{ID: predecessor.ID, IncludeArchived: true})
	if err != nil || old.Status != threadstore.StatusArchived {
		t.Fatalf("durable predecessor was not atomically archived: %+v / %v", old, err)
	}
	call.reply <- standbyReply{response: checkpointNamed("model-b")}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyReleasedCheckpointRecovery(t *testing.T) {
	for _, mode := range []string{"background", "local resume", "another writer"} {
		t.Run(mode, func(t *testing.T) {
			store := &flakyCheckpointStore{Store: memory.NewStore(), failing: true, saved: make(chan string, 32)}
			fake, calls := controlledStandbyDriver()
			agent := adaptor.New(fake, adaptor.WithThreadStore(store))
			standby, cancel := openTestStandby(t, agent, "released-recovery")
			events := standby.Events()
			if err := standby.Send(context.Background(), "original"); err != nil {
				t.Fatal(err)
			}
			first := standbyReceive(t, calls)
			first.reply <- standbyReply{response: checkpointNamed("local-checkpoint")}
			if _, done := standbyDone(t, events); done.Err != nil {
				t.Fatal(done.Err)
			}
			cancel()
			for range events {
			}
			if mode == "background" {
				store.recover()
				if saved := standbyReceive(t, store.saved); saved != "local-checkpoint" {
					t.Fatalf("released recovery saved %q", saved)
				}
				return
			}
			if mode == "local resume" {
				stream := agent.Thread("released-recovery").Send(context.Background(), "continue")
				call := standbyReceive(t, calls)
				if call.request.Session.State == nil || call.request.Session.State.ResumeID != "local-checkpoint" {
					t.Fatalf("released checkpoint not authoritative: %+v", call.request.Session)
				}
				call.reply <- standbyReply{response: checkpointNamed("new-local-checkpoint")}
				if _, err := stream.Result(); err != nil {
					t.Fatal(err)
				}
				return
			}
			otherFake, otherCalls := controlledStandbyDriver()
			other := adaptor.New(otherFake, adaptor.WithThreadStore(store))
			t.Cleanup(func() { _ = other.Close(context.Background()) })
			ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			for {
				stream := other.Thread("released-recovery").Send(ctx, "another writer")
				finished := make(chan error, 1)
				go func() { _, err := stream.Result(); finished <- err }()
				select {
				case call := <-otherCalls:
					store.recover()
					call.reply <- standbyReply{response: checkpointNamed("external-checkpoint")}
					if err := <-finished; err != nil {
						t.Fatal(err)
					}
				case err := <-finished:
					if !errors.Is(err, adaptor.ErrThreadBusy) {
						t.Fatal(err)
					}
					continue
				case <-ctx.Done():
					t.Fatal("Standby retained ownership after its context ended")
				}
				break
			}
			if _, err := agent.Thread("released-recovery").Send(ctx, "stale pending state").Result(); !errors.Is(err, adaptor.ErrThreadLeaseLost) {
				t.Fatalf("superseded pending checkpoint was not fenced: %v", err)
			}
			if record := activeRecord(t, store, "released-recovery"); record.State.ResumeID != "external-checkpoint" {
				t.Fatalf("background recovery overwrote a newer writer: %+v", record)
			}
		})
	}
}
