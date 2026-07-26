// Package agui forwards to its new location.
//
// Deprecated: moved to github.com/agent-dance/agent-adaptor/bridges/agui; this
// forwarding package will be removed in v1.0.0.
package agui

import (
	newagui "github.com/agent-dance/agent-adaptor/bridges/agui"
)

// Types.
type (
	DecisionMode     = newagui.DecisionMode
	Message          = newagui.Message
	RunAgentInput    = newagui.RunAgentInput
	Translator       = newagui.Translator
	TranslatorOption = newagui.TranslatorOption
)

// Constants.
const (
	RoleAssistant = newagui.RoleAssistant
	RoleUser      = newagui.RoleUser
	RoleSystem    = newagui.RoleSystem
	RoleDeveloper = newagui.RoleDeveloper
	RoleTool      = newagui.RoleTool
	RoleActivity  = newagui.RoleActivity
	ReasoningRole = newagui.ReasoningRole

	DecisionAsToolCall DecisionMode = newagui.DecisionAsToolCall
	DecisionAsCustom   DecisionMode = newagui.DecisionAsCustom
)

// Functions.
var (
	ResolveDecision   = newagui.ResolveDecision
	ValidateStructure = newagui.ValidateStructure
	VerifySequence    = newagui.VerifySequence
	Wrap              = newagui.Wrap
	WrapWithContext   = newagui.WrapWithContext
	DecodeHTTPRequest = newagui.DecodeHTTPRequest
	NewTranslator     = newagui.NewTranslator
	WithDecisionMode  = newagui.WithDecisionMode
)
