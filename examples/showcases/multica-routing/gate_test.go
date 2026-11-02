package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/tool"
)

type gateInvocation struct {
	result json.RawMessage
	err    error
}

func invokeGate(ctx context.Context, definition tool.Definition, input string) <-chan gateInvocation {
	done := make(chan gateInvocation, 1)
	go func() {
		result, err := definition.Invoke(ctx, json.RawMessage(input))
		done <- gateInvocation{result: result, err: err}
	}()
	return done
}

func gateTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func gateOutcome(t *testing.T, ctx context.Context, done <-chan gateInvocation) gateInvocation {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatalf("gate did not settle: %v", ctx.Err())
		return gateInvocation{}
	}
}

func requireReleasedGate(t *testing.T, outcome gateInvocation, role, name string) {
	t.Helper()
	var result gateResult
	if outcome.err != nil || json.Unmarshal(outcome.result, &result) != nil || result != (gateResult{Role: role, Gate: name, Released: true}) {
		t.Fatalf("gate outcome=%s err=%v", outcome.result, outcome.err)
	}
}

func TestGateRecordsActualInvocationThenWaitsForHostRelease(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	defer g.releaseAll()
	definition := g.toolFor("reviewer")
	descriptor, err := definition.Descriptor()
	if err != nil || descriptor.Name != "host_gate" || descriptor.Revision == "" || descriptor.Annotations.ReadOnly == nil || !*descriptor.Annotations.ReadOnly || descriptor.Annotations.OpenWorld == nil || *descriptor.Annotations.OpenWorld {
		t.Fatalf("invalid gate declaration: %+v, %v", descriptor, err)
	}
	done := invokeGate(ctx, definition, `{"gate":"review-start"}`)
	arrival, err := g.wait(ctx, "reviewer", "review-start")
	if err != nil || arrival.Role != "reviewer" || arrival.Gate != "review-start" || arrival.At.IsZero() {
		t.Fatalf("arrival=%+v err=%v", arrival, err)
	}
	select {
	case outcome := <-done:
		t.Fatalf("gate finished before release: %+v", outcome)
	default:
	}
	g.release("reviewer", "review-start")
	g.release("reviewer", "review-start")
	requireReleasedGate(t, gateOutcome(t, ctx, done), "reviewer", "review-start")
	again, err := g.wait(ctx, "reviewer", "review-start")
	if err != nil || again != arrival {
		t.Fatalf("first actual arrival changed: %+v, %v", again, err)
	}
}

func TestGateReleaseBeforeInvocationDoesNotFabricateArrival(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	g.release("implementer", "ready")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if arrival, err := g.wait(cancelled, "implementer", "ready"); !errors.Is(err, context.Canceled) || !arrival.At.IsZero() {
		t.Fatalf("release fabricated an arrival: %+v, %v", arrival, err)
	}
	done := invokeGate(ctx, g.toolFor("implementer"), `{"gate":"ready"}`)
	requireReleasedGate(t, gateOutcome(t, ctx, done), "implementer", "ready")
	if arrival, err := g.wait(ctx, "implementer", "ready"); err != nil || arrival.At.IsZero() {
		t.Fatalf("real invocation was not recorded: %+v, %v", arrival, err)
	}
}

func TestGateIdentityCannotBeSuppliedByToolArguments(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	defer g.releaseAll()
	reviewer := g.toolFor("reviewer")
	if _, err := reviewer.Invoke(ctx, json.RawMessage(`{"gate":"check","role":"implementer"}`)); err == nil {
		t.Fatal("caller-supplied role bypassed the closed input schema")
	}
	done := invokeGate(ctx, reviewer, `{"gate":"check"}`)
	if arrival, err := g.wait(ctx, "reviewer", "check"); err != nil || arrival.Role != "reviewer" {
		t.Fatalf("bound role lost: %+v, %v", arrival, err)
	}
	g.release("implementer", "check")
	select {
	case result := <-done:
		t.Fatalf("another role released the reviewer: %+v", result)
	default:
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if arrival, err := g.wait(cancelled, "implementer", "check"); !errors.Is(err, context.Canceled) || !arrival.At.IsZero() {
		t.Fatalf("reviewer fabricated implementer arrival: %+v, %v", arrival, err)
	}
	g.release("reviewer", "check")
	requireReleasedGate(t, gateOutcome(t, ctx, done), "reviewer", "check")
}

func TestGateCancellationSettlesBlockedInvocation(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	defer g.releaseAll()
	callCtx, cancel := context.WithCancel(ctx)
	done := invokeGate(callCtx, g.toolFor("worker"), `{"gate":"pending"}`)
	if _, err := g.wait(ctx, "worker", "pending"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if result := gateOutcome(t, ctx, done); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled invocation=%s err=%v", result.result, result.err)
	}
	g.release("worker", "already-cancelled")
	if _, err := g.toolFor("worker").Invoke(callCtx, json.RawMessage(`{"gate":"already-cancelled"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("released gate ignored cancellation: %v", err)
	}
	if arrival, err := g.wait(callCtx, "worker", "already-cancelled"); !errors.Is(err, context.Canceled) || !arrival.At.IsZero() {
		t.Fatalf("pre-cancelled call fabricated arrival: %+v, %v", arrival, err)
	}
}

func TestGateReleaseAllCoversExistingAndLateInvocations(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	done := invokeGate(ctx, g.toolFor("worker"), `{"gate":"existing"}`)
	if _, err := g.wait(ctx, "worker", "existing"); err != nil {
		t.Fatal(err)
	}
	g.releaseAll()
	g.releaseAll()
	requireReleasedGate(t, gateOutcome(t, ctx, done), "worker", "existing")
	requireReleasedGate(t, gateOutcome(t, ctx, invokeGate(ctx, g.toolFor("leader"), `{"gate":"late"}`)), "leader", "late")
	if arrival, err := g.wait(ctx, "leader", "late"); err != nil || arrival.Role != "leader" {
		t.Fatalf("late actual invocation missing: %+v, %v", arrival, err)
	}
}

func TestGateCompoundIdentityHasNoDelimiterCollision(t *testing.T) {
	ctx := gateTestContext(t)
	g := newGates()
	defer g.releaseAll()
	done := invokeGate(ctx, g.toolFor("a:b"), `{"gate":"c"}`)
	if _, err := g.wait(ctx, "a:b", "c"); err != nil {
		t.Fatal(err)
	}
	g.release("a", "b:c")
	select {
	case result := <-done:
		t.Fatalf("delimiter collision released another role's checkpoint: %+v", result)
	default:
	}
	g.release("a:b", "c")
	requireReleasedGate(t, gateOutcome(t, ctx, done), "a:b", "c")
}
