package claude

import (
	"crypto/sha256"
	"fmt"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type sessionCodec struct{}

func (adapter) SessionCodec() agentadaptor.SessionCodec {
	return sessionCodec{}
}

func (sessionCodec) Name() string { return DriverType }

func (sessionCodec) ToParams(state *agentadaptor.DriverSessionState) agentadaptor.SessionParams {
	if state == nil {
		return agentadaptor.SessionParams{}
	}
	return agentadaptor.SessionParams{
		ResumeID:  state.ResumeID,
		DisplayID: displayID(state),
		Values:    cloneData(state.Data),
	}
}

func (sessionCodec) FromParams(params agentadaptor.SessionParams) *agentadaptor.DriverSessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &agentadaptor.DriverSessionState{
		ResumeID:  params.ResumeID,
		DisplayID: displayID,
		Data:      cloneData(params.Values),
	}
}

func (sessionCodec) GuardFingerprint(params agentadaptor.SessionParams) string {
	return guardHash(params.Values,
		agentadaptor.SessionParamCWD,
		agentadaptor.SessionParamWorkspaceID,
		agentadaptor.SessionParamPromptBundleKey,
	)
}

func displayID(state *agentadaptor.DriverSessionState) string {
	if state.DisplayID != "" {
		return state.DisplayID
	}
	return state.ResumeID
}

func cloneData(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func guardHash(values map[string]string, keys ...string) string {
	builder := strings.Builder{}
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(0)
		}
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(values[key])
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}
