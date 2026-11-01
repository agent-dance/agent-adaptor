package adaptor_test

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// The automatic wrap-up has not been delivered or included in outcomes, so it
// must not silently consume a host's otherwise unrestricted opaque message ID.
func TestDeferredWrapUpLeavesHostMessageIDAvailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake, calls := controlledStandbyDriver()
	descriptor := fake.Descriptor()
	descriptor.Messages.MaxUnits = 256
	fake.descriptor = &descriptor
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxTurns: 2}}))
	stream := agent.Send(ctx, "original", adaptor.MessageID("original"))
	first := standbyReceive(t, calls)
	if err := stream.Send(ctx, "first supplement", adaptor.MessageID("first")); err != nil {
		t.Fatal(err)
	}
	first.reply <- standbyReply{response: messageCheckpoint("first-checkpoint")}
	// The second dispatch proves Claim has prepared (and deferred) the
	// automatic wrap-up, which exceeds this capability's 256-byte budget.
	second := standbyReceive(t, calls)
	id := stream.RunID() + "-limit"
	if err := stream.Send(ctx, "next", adaptor.MessageID(id)); err != nil {
		t.Fatal(err)
	}
	second.reply <- standbyReply{response: messageCheckpoint("second-checkpoint")}
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range result.Messages() {
		if outcome.ID == id {
			return
		}
	}
	t.Fatalf("Send returned nil but host input %q disappeared from final outcomes: %+v", id, result.Messages())
}
