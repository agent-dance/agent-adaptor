// Package sse forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/bridges/sse; this
// forwarding package will be removed in v1.0.0.
package sse

import (
	newsse "github.com/agent-dance/agent-adaptor/bridges/sse"
)

// Types.
type (
	DecisionResolveRequest = newsse.DecisionResolveRequest
	Options                = newsse.Options
	Protocol               = newsse.Protocol
	RawRequest             = newsse.RawRequest
)

// Constants.
const (
	AGUI Protocol = newsse.AGUI
	Raw  Protocol = newsse.Raw
)

// Functions.
var (
	Handler                      = newsse.Handler
	WriteDecisionResolveError    = newsse.WriteDecisionResolveError
	DecodeDecisionResolveRequest = newsse.DecodeDecisionResolveRequest
)
