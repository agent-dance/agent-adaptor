package driverutil

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// Lines only frames LF-delimited bytes. The owning Driver interprets records;
// neither framing nor process capture guesses provider semantics.
type Lines struct{ pending bytes.Buffer }

func (l *Lines) Write(chunk []byte, consume func([]byte)) {
	l.pending.Write(chunk)
	for {
		i := bytes.IndexByte(l.pending.Bytes(), '\n')
		if i < 0 {
			return
		}
		line := bytes.Clone(l.pending.Next(i + 1))
		consume(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'}))
	}
}

func (l *Lines) Flush(consume func([]byte)) {
	if l.pending.Len() > 0 {
		consume(bytes.Clone(l.pending.Bytes()))
		l.pending.Reset()
	}
}

// ProtocolSink preserves items supplied by a Driver parser and propagates sink
// errors. It does not construct transcript items or interpret provider bytes.
type ProtocolSink struct {
	Sink       driver.EventSink
	Streaming  bool
	Transcript []driver.TranscriptItem
	Err        error
}

func (s *ProtocolSink) Record(item driver.TranscriptItem) {
	s.Transcript = append(s.Transcript, item)
	if s.Err == nil {
		s.Err = s.Sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &item})
	}
}

func (s *ProtocolSink) Stream(payload driver.StreamPayload) {
	if s.Streaming && s.Err == nil {
		s.Err = s.Sink.EmitStream(payload)
	}
}

// NativeSessionCodec losslessly maps an opaque provider identifier and guards.
// JSON canonicalization avoids delimiter collisions in arbitrary guard values.
type NativeSessionCodec string

func (c NativeSessionCodec) Name() string { return string(c) }
func (NativeSessionCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	displayID := s.DisplayID
	if displayID == "" {
		displayID = s.ResumeID
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: displayID, Values: maps.Clone(s.Data)}
}
func (NativeSessionCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" && p.DisplayID == "" && len(p.Values) == 0 {
		return nil
	}
	displayID := p.DisplayID
	if displayID == "" {
		displayID = p.ResumeID
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: displayID, Data: maps.Clone(p.Values)}
}
func (c NativeSessionCodec) GuardFingerprint(p driver.SessionParams) string {
	value, _ := driver.CanonicalSessionConfigFingerprint(string(c), p.Values)
	return value
}

func SessionGuards(req driver.Request, cwd, profileDir string) map[string]string {
	return map[string]string{
		driver.SessionParamCWD:                cwd,
		driver.SessionParamWorkspaceID:        req.Workspace.ID,
		driver.SessionParamProfileFingerprint: req.ProfilePayload.SessionFingerprint(),
		"profile_dir":                         profileDir,
	}
}

func CheckSessionGuards(req driver.Request, guards map[string]string) error {
	if req.Session == nil || req.Session.State == nil {
		return nil
	}
	for key, expected := range guards {
		if previous, ok := req.Session.State.Data[key]; ok && previous != expected {
			return &engine.ResumeRejectedError{Reason: "session " + key + " changed"}
		}
	}
	return nil
}

func EffectiveCWD(configured string, workspace driver.WorkspaceLease) (string, error) {
	if workspace.CWD != "" {
		configured = workspace.CWD
	}
	if configured == "" {
		configured = "."
	}
	return filepath.Abs(configured)
}

// NativeFilesFingerprint includes the bytes of explicitly identified native
// configuration files. Missing files have an explicit nil value; inspection
// never initializes a profile. No contents or credentials are returned.
func NativeFilesFingerprint(domain string, files ...string) (string, error) {
	values := map[string][]byte{}
	for _, path := range files {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(absolute)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		values[absolute] = raw
	}
	return driver.CanonicalSessionConfigFingerprint(domain, values)
}

// UnknownRecord keeps future protocol extensions observable without treating
// their nested fields as text, usage, sessions, or completion evidence.
func UnknownRecord(raw []byte, eventType string) driver.TranscriptItem {
	return driver.TranscriptItem{Kind: driver.TranscriptSystem, Text: "unmapped provider event: " + eventType, Data: map[string]any{"raw": json.RawMessage(bytes.Clone(raw))}}
}

// ObserveStdout leaves stderr in RawStreams; it is not a semantic transcript.
func ObserveStdout(lines *Lines, consume func([]byte)) func(string, []byte, time.Time) error {
	return func(stream string, chunk []byte, _ time.Time) error {
		if stream == "stdout" {
			lines.Write(chunk, consume)
		}
		return nil
	}
}
