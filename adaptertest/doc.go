// Package adaptertest provides reusable contract tests for DriverAdapter
// implementations.
//
// The suite is intentionally host-neutral: it validates descriptor shape,
// control-plane surfaces, explicit session-codec behavior, and skill/admin
// truthfulness without assuming a live provider account.
//
// Typical usage inside one adapter package test:
//
//	adaptertest.Run(t, adaptertest.Subject{
//		Name:    "codex",
//		Adapter: codex.NewAdapter(),
//		Config: agentadaptor.CodexConfig{Model: "gpt-5.4"},
//		SessionState: &agentadaptor.DriverSessionState{
//			ResumeID: "codex-session",
//			Data: map[string]string{
//				agentadaptor.SessionParamCWD:         "C:/workspace",
//				agentadaptor.SessionParamWorkspaceID: "workspace-1",
//			},
//		},
//		RequiredSessionKeys: []string{
//			agentadaptor.SessionParamCWD,
//			agentadaptor.SessionParamWorkspaceID,
//		},
//		RequiredConfigFields: []string{"command", "cwd", "model"},
//	})
package adaptertest
