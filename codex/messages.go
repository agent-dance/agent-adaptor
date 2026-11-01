package codex

import (
	"context"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

var _ driver.MessageCapabilityResolver = configuredDriver{}

func codexMessageCapability() driver.MessageCapability {
	return driver.MessageCapability{
		Boundaries: []driver.Boundary{driver.BoundaryImmediate},
		Interrupt:  true, Continue: driver.ContinueInRun,
		PersistsDelivered: true, MinVersion: "0.100.0",
	}
}

// ResolveMessageCapability projects message delivery onto the same transport
// selection used by Run. Batch exec continuations are resumed by core.
func (adapter) ResolveMessageCapability(req driver.Request) driver.MessageCapability {
	if usesCodexAppServer(req) {
		return codexVersion(context.Background(), readConfig(req.Config)).Capability(codexMessageCapability())
	}
	return driver.MessageCapability{Continue: driver.ContinueResume, PersistsDelivered: true}
}

// ResolveMessageCapability uses the captured executable configuration for the
// version gate as well as the invocation's selected transport.
func (d configuredDriver) ResolveMessageCapability(req driver.Request) driver.MessageCapability {
	return d.adapter.ResolveMessageCapability(d.requestWithConfig(req))
}
