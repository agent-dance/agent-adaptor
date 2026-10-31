package codex

import "github.com/agent-dance/agent-adaptor/v2/driver"

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
		return codexMessageCapability()
	}
	return driver.MessageCapability{Continue: driver.ContinueResume, PersistsDelivered: true}
}
