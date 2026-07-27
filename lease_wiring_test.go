package agentadaptor

import (
	"time"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// defaultLeaseTTL / defaultLeaseRenewInterval keep their historical
// declarations for the root-package internal tests that mutate them to
// exercise lease-renewal timing (runner_session_internal_test.go). The
// engine owns the production defaults; this file only re-points the engine
// accessors at the mutable test vars, and it is test-only so no production
// build carries an init()-time injection seam.
var (
	defaultLeaseTTL           = 5 * time.Minute
	defaultLeaseRenewInterval = 2 * time.Minute
)

func init() {
	engine.LeaseTTL = func() time.Duration { return defaultLeaseTTL }
	engine.LeaseRenewInterval = func() time.Duration { return defaultLeaseRenewInterval }
}
