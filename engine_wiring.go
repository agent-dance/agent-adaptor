package agentadaptor

import (
	"time"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// defaultLeaseTTL / defaultLeaseRenewInterval keep their historical
// declarations in the root package (internal tests mutate them to exercise
// lease-renewal timing). The engine reads them through the accessors injected
// below, so test mutations still flow into the session pipeline.
var (
	defaultLeaseTTL           = 5 * time.Minute
	defaultLeaseRenewInterval = 2 * time.Minute
)

func init() {
	engine.LeaseTTL = func() time.Duration { return defaultLeaseTTL }
	engine.LeaseRenewInterval = func() time.Duration { return defaultLeaseRenewInterval }
	// The built-in skill materializer is backed by the archive_*.go cluster,
	// which stays in the root package; engine.Build reaches it through this
	// factory.
	engine.DefaultSkillMaterializerFactory = newDefaultSkillMaterializer
}
