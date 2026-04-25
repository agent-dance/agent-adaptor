// Package runtimeservice provides drop-in mixins and helpers for
// implementing the agentadaptor.RuntimeServiceManager interface.
//
// The package exists so hosts that don't yet need a particular
// capability (e.g. label-based release introduced in v0.5) can
// satisfy the interface with a single embedding line, without
// scattering noop methods across their managers.
package runtimeservice

import (
	"context"
)

// NoopReleaseByLabels is a drop-in mixin that satisfies the v0.5
// agentadaptor.RuntimeServiceManager.ReleaseByLabels method with a
// noop. Hosts that don't yet need label-based release can embed it
// to keep their existing Ensure / ReleaseByRun implementation
// compiling against the v0.5 interface:
//
//	type myMgr struct {
//	    runtimeservice.NoopReleaseByLabels
//	    // ... existing fields ...
//	}
//
// Embedding promotes ReleaseByLabels onto the wrapper while leaving
// every other method to the host's own implementation. When the host
// is ready to support label-based release, simply remove the
// embedding and provide the real method.
//
// Note: this mixin is opt-in. Hosts implementing a fresh
// RuntimeServiceManager from scratch should write a real
// ReleaseByLabels (even if the body is `return nil`) so the choice
// is explicit at code-review time rather than hidden behind an
// embedded type.
type NoopReleaseByLabels struct{}

// ReleaseByLabels implements the v0.5
// agentadaptor.RuntimeServiceManager.ReleaseByLabels method as a
// noop. See the type doc for migration guidance.
func (NoopReleaseByLabels) ReleaseByLabels(_ context.Context, _ map[string]string) error {
	return nil
}
