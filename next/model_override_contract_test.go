package adaptor_test

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestModelOverrideNormalization(t *testing.T) {
	fake := newFakeDriver()
	agent := adaptor.New(fake)

	if _, err := agent.Run(context.Background(), "explicit", adaptor.WithModel("  gpt-5.4  ")); err != nil {
		t.Fatalf("explicit model run: %v", err)
	}
	if got := fake.request(t, 0).ModelOverride; got != "gpt-5.4" {
		t.Fatalf("explicit ModelOverride = %q, want %q", got, "gpt-5.4")
	}

	if _, err := agent.Run(context.Background(), "blank", adaptor.WithModel("   \t\r\n")); err != nil {
		t.Fatalf("blank model run: %v", err)
	}
	if got := fake.request(t, 1).ModelOverride; got != "" {
		t.Fatalf("blank ModelOverride = %q, want empty", got)
	}

	if _, err := agent.Run(context.Background(), "unset"); err != nil {
		t.Fatalf("unset model run: %v", err)
	}
	if got := fake.request(t, 2).ModelOverride; got != "" {
		t.Fatalf("unset ModelOverride = %q, want empty", got)
	}
}
