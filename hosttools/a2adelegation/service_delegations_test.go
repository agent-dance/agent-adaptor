package a2adelegation_test

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestServiceDelegationsPreservesStartedOrderAndRepeatedAgents(t *testing.T) {
	var nextID atomic.Uint64
	svc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("echo", adaptor.New(&scriptedRoleDriver{kind: "ordered", final: "done"}), a2adelegation.Policy{}),
		},
		NewID: func() string { return fmt.Sprintf("delegation-%02d", nextID.Add(1)) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	const runID = "run-ordered"
	for i := 0; i < 2; i++ {
		if _, err := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{
			RunID: runID, Agent: "echo", Objective: fmt.Sprintf("attempt %d", i+1),
		}); err != nil {
			t.Fatalf("Delegate(%d): %v", i+1, err)
		}
	}

	got := svc.Delegations(runID)
	if len(got) != 2 {
		t.Fatalf("Delegations = %d entries, want 2: %+v", len(got), got)
	}
	for i, wantID := range []string{"delegation-01", "delegation-02"} {
		if got[i].DelegationID != wantID || got[i].Agent != "echo" || got[i].Status != "completed" {
			t.Errorf("Delegations[%d] = %+v, want id=%q agent=echo status=completed", i, got[i], wantID)
		}
	}
	if latest, ok := svc.Result(runID, "echo"); !ok || latest.DelegationID != "delegation-02" {
		t.Fatalf("Result latest projection = (%+v, %v), want delegation-02", latest, ok)
	}
	if len(svc.Results(runID)) != 1 {
		t.Fatalf("Results map should retain one latest projection for the repeated agent")
	}

	// Both the slice and nested mutable fields are caller-owned copies.
	if got[0].RawTask == nil {
		t.Fatalf("recorded delegation is missing RawTask: %+v", got[0])
	}
	got[0].Status = "tampered"
	got[0].RawTask["provider"] = "tampered"
	again := svc.Delegations(runID)
	if again[0].Status != "completed" || again[0].RawTask["provider"] == "tampered" {
		t.Fatalf("caller mutation escaped defensive copy: %+v", again[0])
	}

	if unknown := svc.Delegations("unknown"); len(unknown) != 0 {
		t.Fatalf("Delegations(unknown) = %+v, want empty", unknown)
	}
	if err := svc.ReleaseRun(runID); err != nil {
		t.Fatalf("ReleaseRun: %v", err)
	}
	if len(svc.Delegations(runID)) != 2 {
		t.Fatal("ordered delegation history was lost by ReleaseRun")
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(svc.Delegations(runID)) != 2 {
		t.Fatal("ordered delegation history was lost by Close")
	}
}

func TestServiceDelegationsConcurrentPublishAndQuery(t *testing.T) {
	svc := newUnitService(t)
	const (
		runID = "run-concurrent"
		count = 8
	)

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := svc.Bus().SubscribeRun(subCtx, runID)
	started := make(chan []string, 1)
	go func() {
		ids := make([]string, 0, count)
		for event := range events {
			if event.Kind == a2adelegation.DelegationStarted {
				ids = append(ids, event.DelegationID)
				if len(ids) == count {
					started <- ids
					return
				}
			}
		}
	}()

	var delegates sync.WaitGroup
	delegates.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer delegates.Done()
			if _, err := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{
				RunID: runID, Agent: "echo", Objective: fmt.Sprintf("concurrent %d", i),
			}); err != nil {
				t.Errorf("Delegate(%d): %v", i, err)
			}
		}(i)
	}

	queryDone := make(chan struct{})
	go func() {
		defer close(queryDone)
		for {
			entries := svc.Delegations(runID)
			for i := range entries {
				_ = entries[i].Status
			}
			if len(entries) == count {
				return
			}
		}
	}()
	delegates.Wait()
	<-queryDone

	var accepted []string
	select {
	case accepted = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("did not observe every DelegationStarted event")
	}
	recorded := svc.Delegations(runID)
	gotIDs := make([]string, len(recorded))
	for i := range recorded {
		gotIDs[i] = recorded[i].DelegationID
		if recorded[i].Status != "completed" {
			t.Errorf("Delegations[%d].Status = %q, want completed", i, recorded[i].Status)
		}
	}
	if !reflect.DeepEqual(gotIDs, accepted) {
		t.Fatalf("Delegations order = %v, EventBus Started order = %v", gotIDs, accepted)
	}
}

func TestServiceDelegationsLocalRemoteParity(t *testing.T) {
	script := parityScript{deltas: []string{"hello ", "world"}, final: "hello world\nPARITY_DONE"}
	localSvc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("echo", adaptor.New(&scriptedRoleDriver{
				kind: "ordered-parity", deltas: script.deltas, final: script.final,
			}), a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
	})
	if err != nil {
		t.Fatalf("NewService(local): %v", err)
	}
	defer localSvc.Close()

	server := remoteParityServer(t, script)
	defer server.Close()
	remoteSvc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Remote("echo", server.URL, a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
	})
	if err != nil {
		t.Fatalf("NewService(remote): %v", err)
	}
	defer remoteSvc.Close()

	for label, svc := range map[string]*a2adelegation.Service{"local": localSvc, "remote": remoteSvc} {
		if _, err := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{
			RunID: "run-parity-accessor", Agent: "echo", Objective: label,
		}); err != nil {
			t.Fatalf("Delegate(%s): %v", label, err)
		}
	}
	local := localSvc.Delegations("run-parity-accessor")
	remote := remoteSvc.Delegations("run-parity-accessor")
	if len(local) != 1 || len(remote) != 1 {
		t.Fatalf("Delegations lengths local=%d remote=%d, want 1/1", len(local), len(remote))
	}
	if local[0].Agent != remote[0].Agent || local[0].Status != remote[0].Status || local[0].Summary != remote[0].Summary {
		t.Fatalf("Local/Remote accessor semantics diverge:\nlocal:  %+v\nremote: %+v", local[0], remote[0])
	}
}
