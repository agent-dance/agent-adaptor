// Package subagentstream forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/bridges/subagentstream;
// this forwarding package will be removed in v1.0.0.
package subagentstream

import (
	newsubagentstream "github.com/agent-dance/agent-adaptor/bridges/subagentstream"
)

// Types.
type (
	Event      = newsubagentstream.Event
	EventBus   = newsubagentstream.EventBus
	MuxOptions = newsubagentstream.MuxOptions
)

// Functions.
var (
	AGUICustomEvent = newsubagentstream.AGUICustomEvent
	StreamPayload   = newsubagentstream.StreamPayload
	Wrap            = newsubagentstream.Wrap
	WrapAGUI        = newsubagentstream.WrapAGUI
)
