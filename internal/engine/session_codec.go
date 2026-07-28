package engine

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Well-known session parameter keys used by the thread coordinator. Package
// driver owns the SPI identities; these aliases keep the engine algorithms on
// the canonical values.
const (
	// SessionParamCWD records the workspace directory captured in a session.
	SessionParamCWD = driver.SessionParamCWD
	// SessionParamWorkspaceID records the SDK workspace lease identifier.
	SessionParamWorkspaceID = driver.SessionParamWorkspaceID
	// SessionParamProfileFingerprint records the provider-visible effective
	// profile resource fingerprint captured by a resumable session.
	SessionParamProfileFingerprint = driver.SessionParamProfileFingerprint
)

type passthroughSessionCodec struct{}

func (passthroughSessionCodec) Name() string { return "passthrough" }

func (passthroughSessionCodec) ToParams(state *SessionState) SessionParams {
	if state == nil {
		return SessionParams{}
	}
	displayID := state.DisplayID
	if displayID == "" {
		displayID = state.ResumeID
	}
	return SessionParams{
		ResumeID:  state.ResumeID,
		DisplayID: displayID,
		Values:    cloneStringMap(state.Data),
	}
}

func (passthroughSessionCodec) FromParams(params SessionParams) *SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &SessionState{
		ResumeID:  params.ResumeID,
		DisplayID: displayID,
		Data:      cloneStringMap(params.Values),
	}
}

func (passthroughSessionCodec) GuardFingerprint(params SessionParams) string {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return ""
	}
	return stableHash(params.ResumeID, params.DisplayID, params.Values)
}

// SessionCodecFor returns the driver's explicit session codec when available,
// otherwise it falls back to a passthrough codec that simply round-trips
// SessionState fields. This permissive helper exists for stateless tooling and
// direct SPI inspection. Thread coordination must use resumeSessionCodecFor so
// an undeclared or incomplete resume capability can never become stateful by
// accident.
func SessionCodecFor(driver Driver) SessionCodec {
	if aware, ok := driver.(SessionCodecProvider); ok {
		if codec := aware.SessionCodec(); !nilSessionCodec(codec) {
			return codec
		}
	}
	return passthroughSessionCodec{}
}

// resumeSessionCodecFor validates the capability/interface half of the Thread
// contract before any store lease is acquired. It intentionally does not fall
// back: a Thread is meaningful only for a Driver that explicitly declares
// resume support and supplies a stable, non-nil codec.
func resumeSessionCodecFor(d Driver) (codec SessionCodec, err error) {
	if d == nil {
		return nil, &SessionIncompatibleError{Reason: "thread driver is nil"}
	}
	if !d.Descriptor().Sessions.SupportsResume {
		return nil, &SessionIncompatibleError{Reason: "driver does not declare session resume support"}
	}
	provider, ok := d.(SessionCodecProvider)
	if !ok {
		return nil, &SessionIncompatibleError{Reason: "resume-capable driver does not implement SessionCodecProvider"}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			codec = nil
			err = &SessionIncompatibleError{Reason: fmt.Sprintf("driver session codec panicked during validation (%T)", recovered)}
		}
	}()
	codec = provider.SessionCodec()
	if nilSessionCodec(codec) {
		return nil, &SessionIncompatibleError{Reason: "resume-capable driver returned a nil session codec"}
	}
	name := strings.TrimSpace(codec.Name())
	if name == "" {
		return nil, &SessionIncompatibleError{Reason: "driver session codec has an empty stable name"}
	}
	if again := strings.TrimSpace(codec.Name()); again != name {
		return nil, &SessionIncompatibleError{Reason: "driver session codec name is not stable"}
	}
	return codec, nil
}

func nilSessionCodec(codec SessionCodec) bool {
	if codec == nil {
		return true
	}
	value := reflect.ValueOf(codec)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func normalizeResumableSessionState(codec SessionCodec, state *SessionState) (normalized *SessionState, err error) {
	if state == nil {
		return nil, ErrSessionCheckpointMissing
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			normalized = nil
			err = &SessionIncompatibleError{Reason: fmt.Sprintf("driver session codec panicked while normalizing checkpoint (%T)", recovered)}
		}
	}()
	normalized = codec.FromParams(codec.ToParams(state))
	if normalized == nil || strings.TrimSpace(normalized.ResumeID) == "" {
		return nil, &SessionIncompatibleError{Reason: "session checkpoint has no resumable provider identifier"}
	}
	return normalized, nil
}

func validateResumableRecord(record *SessionRecord, codec SessionCodec) error {
	if record == nil {
		return ErrSessionNotFound
	}
	if record.SessionCodec == "" || record.SessionCodec != strings.TrimSpace(codec.Name()) {
		return &SessionIncompatibleError{Reason: "stored session codec does not match the configured driver"}
	}
	_, err := normalizeResumableSessionState(codec, record.DriverState)
	return err
}

func normalizeSessionState(driver Driver, state *SessionState) *SessionState {
	codec := SessionCodecFor(driver)
	return codec.FromParams(codec.ToParams(state))
}

func sessionDisplayID(driver Driver, state *SessionState) string {
	params := SessionCodecFor(driver).ToParams(state)
	if params.DisplayID != "" {
		return params.DisplayID
	}
	return params.ResumeID
}
