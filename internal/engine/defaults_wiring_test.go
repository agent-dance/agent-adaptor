package engine

import (
	"testing"
	"time"
)

func TestThreadLeaseDefaults(t *testing.T) {
	if got := LeaseTTL(); got != 5*time.Minute {
		t.Fatalf("LeaseTTL() = %v", got)
	}
	if got := LeaseRenewInterval(); got != 2*time.Minute {
		t.Fatalf("LeaseRenewInterval() = %v", got)
	}
}
