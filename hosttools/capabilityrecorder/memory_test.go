package capabilityrecorder_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/hosttools/capabilityrecorder"
)

func record(scope capabilityrecorder.Scope, seq uint64) capabilityrecorder.Record {
	zero := time.Duration(0)
	return capabilityrecorder.Record{Scope: scope, Sequence: seq, Time: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Invocation: capability.Invocation{
		InvocationID: "call", Ref: capability.Ref{Kind: capability.MCP, Key: " 知识__库 ", Operation: "_search"}, Phase: capability.Completed,
		Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Duration: &zero,
	}}
}

func TestMemoryExactScopesPaginationAndClones(t *testing.T) {
	ctx := context.Background()
	s := capabilityrecorder.NewMemoryStore()
	scopes := []capabilityrecorder.Scope{
		{IdentityID: "a:b", Tenant: "c", Profile: "p", RunID: "r"},
		{IdentityID: "a", Tenant: "b:c", Profile: "p", RunID: "r"},
		{IdentityID: "a:b", Tenant: "c", Profile: "other", RunID: "r"},
		{IdentityID: "a:b", Tenant: "c", Profile: "p", RunID: "r "},
		{RunID: "r"},
	}
	for _, scope := range scopes {
		for _, seq := range []uint64{9, 2, 5} {
			r := record(scope, seq)
			if err := s.Append(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err := s.Append(ctx, r); err != nil {
				t.Fatal("idempotent append", err)
			}
			*r.Invocation.Duration = 77
		}
	}
	for _, scope := range scopes {
		page, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope, Limit: 2})
		if err != nil || len(page.Records) != 2 || page.NextSequence != 5 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		if !reflect.DeepEqual(page.Records, []capabilityrecorder.Record{record(scope, 2), record(scope, 5)}) {
			t.Fatal("scope/order/deep clone", page)
		}
		*page.Records[0].Invocation.Duration = 42
		page.Records[1].Scope.RunID = "mutated"
		next, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope, AfterSequence: page.NextSequence, Limit: 2})
		if err != nil || len(next.Records) != 1 || next.Records[0].Sequence != 9 || next.NextSequence != 0 {
			t.Fatalf("tail=%+v err=%v", next, err)
		}
		all, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope})
		if err != nil || len(all.Records) != 3 || *all.Records[0].Invocation.Duration != 0 || all.Records[1].Scope != scope {
			t.Fatal("read clone", all, err)
		}
	}
	conflict := record(scopes[0], 2)
	conflict.Invocation.Ref.Operation = "conflict"
	if err := s.Append(ctx, conflict); err == nil {
		t.Fatal("conflict accepted")
	}
}

func TestMemoryQueryBoundsAndCancellation(t *testing.T) {
	s := capabilityrecorder.NewMemoryStore()
	ctx := context.Background()
	scope := capabilityrecorder.Scope{RunID: "r"}
	for i := uint64(1); i <= 1001; i++ {
		if err := s.Append(ctx, record(scope, i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		limit, count int
		next         uint64
	}{{0, 100, 100}, {1, 1, 1}, {1000, 1000, 1000}} {
		page, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope, Limit: tc.limit})
		if err != nil || len(page.Records) != tc.count || page.NextSequence != tc.next {
			t.Fatal(page, err)
		}
	}
	for _, q := range []capabilityrecorder.Query{{}, {Scope: scope, Limit: -1}, {Scope: scope, Limit: 1001}} {
		if _, err := s.Query(ctx, q); !errors.Is(err, capabilityrecorder.ErrInvalidQuery) {
			t.Fatalf("query=%+v err=%v", q, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Append(cancelled, record(scope, 1002)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Query(cancelled, capabilityrecorder.Query{Scope: scope}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	page, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope, AfterSequence: 1001})
	if err != nil || len(page.Records) != 0 || page.NextSequence != 0 {
		t.Fatal(page, err)
	}
}

func TestMemoryConcurrentAtomicAppendQuery(t *testing.T) {
	s := capabilityrecorder.NewMemoryStore()
	ctx := context.Background()
	scope := capabilityrecorder.Scope{RunID: "r"}
	var wg sync.WaitGroup
	for i := uint64(1); i <= 40; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			r := record(scope, seq)
			for j := 0; j < 4; j++ {
				if err := s.Append(ctx, r); err != nil {
					t.Error(err)
				}
				page, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope})
				if err != nil {
					t.Error(err)
				}
				for k := 1; k < len(page.Records); k++ {
					if page.Records[k-1].Sequence >= page.Records[k].Sequence {
						t.Error("unordered")
					}
				}
			}
		}(i)
	}
	wg.Wait()
	page, err := s.Query(ctx, capabilityrecorder.Query{Scope: scope})
	if err != nil || len(page.Records) != 40 {
		t.Fatal(page, err)
	}
}
