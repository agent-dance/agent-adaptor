// offline demonstrates consumer APIs with a scripted in-memory Driver.
// It starts no CLI, contacts no service, and reads no provider configuration.
// Run it with: go run ./examples/offline
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/capabilityrecorder"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := demonstrate(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func demonstrate(ctx context.Context, out io.Writer) (err error) {
	store := capabilityrecorder.NewMemoryStore() // Explicitly nonpersistent.
	recorder, err := capabilityrecorder.New(capabilityrecorder.Config{Store: store})
	if err != nil {
		return err
	}
	identity := adaptor.Identity{ID: "offline-reviewer", Tenant: "demo", Profile: "demo"}
	agent := adaptor.New(demoDriver{},
		adaptor.WithIdentity(identity), recorder.Option(),
		adaptor.WithAppendSystemPrompt("Use repo terminology.\n"),
		adaptor.WithPolicy(adaptor.Policy{
			ActiveExecutionTimeout: 2 * time.Second,
			Approvals:              adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk, Timeout: 5 * time.Second},
		}),
	)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, agent.Close(shutdown))
	}()

	// Each call overrides a copy of the defaults. Empty clears this native
	// append channel; it does not rewrite the user's prompt or Instructions.
	for _, call := range []struct {
		name string
		opts []adaptor.CallOption
	}{
		{name: "default"},
		{name: "override", opts: []adaptor.CallOption{adaptor.WithAppendSystemPrompt("Answer in Chinese.\n")}},
		{name: "clear", opts: []adaptor.CallOption{adaptor.WithAppendSystemPrompt("")}},
		{name: "default again"},
	} {
		result, err := agent.Run(ctx, "show channels", call.opts...)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s\n", call.name, result.Text)
	}

	stream := agent.Stream(ctx, "review demo")
	// The same stream carries text, confirmed snapshots, facts and approval.
	for event := range stream.Events() {
		switch event := event.(type) {
		case adaptor.TextDelta:
			if event.Text != "" {
				fmt.Fprintf(out, "text: %s\n", event.Text)
			}
		case adaptor.CapabilityInvocation:
			fmt.Fprintf(out, "capability: %s %s\n", event.Invocation.Ref.Key, event.Invocation.Phase)
		case adaptor.TodoUpdated:
			// Replace the displayed scope, even when Items is empty. Revision
			// belongs to this run/scope; Event.Meta().Sequence orders events.
			fmt.Fprintf(out, "todo: revision=%d items=%d\n", event.Snapshot.Revision, len(event.Snapshot.Items))
		case *adaptor.ApprovalRequest:
			// A real UI lets a user choose. This offline demo chooses "docs".
			if err := event.Answer(ctx, "docs"); err != nil {
				stream.Cancel()
				for range stream.Events() {
				}
				_, runErr := stream.Result()
				return errors.Join(err, runErr)
			}
		}
	}
	result, err := stream.Result() // Read after draining the only event stream.
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "result: %s\n", result.Text)
	page, err := recorder.Query(ctx, capabilityrecorder.Query{
		Scope: capabilityrecorder.Scope{
			IdentityID: identity.ID, Tenant: identity.Tenant, Profile: identity.Profile,
			RunID: stream.RunID(),
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "recorded capability facts: %d\n", len(page.Records))

	// A complete Policy replaces the construction Policy, including approvals.
	// This script waits for cancellation, so it deterministically exhausts the
	// active budget. WithTimeout/parent deadline remain wall-clock upper bounds.
	_, err = agent.Run(ctx, "wait for budget", adaptor.WithPolicy(adaptor.Policy{
		ActiveExecutionTimeout: 25 * time.Millisecond,
	}))
	var failed *adaptor.RunError
	if !errors.As(err, &failed) || failed.Reason != adaptor.ReasonActiveExecutionTimeout {
		return fmt.Errorf("expected the demo's active budget failure, got %w", err)
	}
	// Reason selects the primary outcome. Cause can also match cancellation.
	fmt.Fprintf(out, "budget: %s partial=%q raw=%t\n", failed.Reason,
		failed.Result.Text, failed.Result.Raw().Stdout != "")
	return nil
}
