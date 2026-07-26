// Package sessionrecorder forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder;
// this forwarding package will be removed in v1.0.0.
package sessionrecorder

import (
	newsessionrecorder "github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// Types.
type (
	Backend        = newsessionrecorder.Backend
	HostSeq        = newsessionrecorder.HostSeq
	JSONLOption    = newsessionrecorder.JSONLOption
	KeyValidator   = newsessionrecorder.KeyValidator
	Option         = newsessionrecorder.Option
	PendingTracker = newsessionrecorder.PendingTracker
	Record         = newsessionrecorder.Record
	Recorder       = newsessionrecorder.Recorder
	SessionInfo    = newsessionrecorder.SessionInfo
)

// Variables.
var (
	DefaultKeyPattern    = newsessionrecorder.DefaultKeyPattern
	DefaultKeyValidator  = newsessionrecorder.DefaultKeyValidator
	ErrInvalidSessionKey = newsessionrecorder.ErrInvalidSessionKey
)

// Functions.
var (
	PendingDecisions        = newsessionrecorder.PendingDecisions
	NewJSONLBackend         = newsessionrecorder.NewJSONLBackend
	NewMemoryBackend        = newsessionrecorder.NewMemoryBackend
	WithJSONLBadLineHandler = newsessionrecorder.WithJSONLBadLineHandler
	WithJSONLDirMode        = newsessionrecorder.WithJSONLDirMode
	WithJSONLFileMode       = newsessionrecorder.WithJSONLFileMode
	WithJSONLKeyValidator   = newsessionrecorder.WithJSONLKeyValidator
	WithClock               = newsessionrecorder.WithClock
	WithKeyValidator        = newsessionrecorder.WithKeyValidator
	NewPendingTracker       = newsessionrecorder.NewPendingTracker
	New                     = newsessionrecorder.New
)
