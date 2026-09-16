package codebuddy

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/driver"
)

// CodeBuddy 2.151.0 LLMView.toSerializableArray renders --output-format json as
// top-level history records followed by one ResultMessage. Only those top-level
// records are interpreted: embedded JSON, history text and nested arrays cannot
// supply a terminal. The legacy single result and newline-object output remain
// accepted. A terminal must be last in every representation.
func (p *parser) processNativeJSON(raw []byte) {
	records, ok := codeBuddyNativeRecords(raw)
	if !ok {
		p.protocolMalformed = true
		p.emit(driver.TranscriptItem{Kind: driver.TranscriptStdout, Text: string(raw)})
		return
	}
	for _, record := range records {
		var payload map[string]any
		if json.Unmarshal(record, &payload) != nil || payload == nil || p.terminalSeen {
			p.protocolMalformed = true
			// Retain the extra record for audit without allowing it to replace
			// the first terminal or make that terminal healthy again.
			p.emit(driver.TranscriptItem{Kind: driver.TranscriptStdout, Text: string(record)})
			continue
		}
		p.handlePayload(string(record), payload)
	}
}

func codeBuddyNativeRecords(raw []byte) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !utf8.Valid(trimmed) {
		return nil, false
	}
	if trimmed[0] == '[' {
		var records []json.RawMessage
		if json.Unmarshal(trimmed, &records) != nil || len(records) == 0 {
			return nil, false
		}
		return records, true
	}
	if trimmed[0] != '{' {
		return nil, false
	}
	if json.Valid(trimmed) {
		return []json.RawMessage{trimmed}, true
	}
	// Compatibility with existing newline-object JSON output is strict: every
	// nonempty line must be one complete object. Never salvage a terminal from
	// a truncated document, a trailing diagnostic or an array plus another value.
	var records []json.RawMessage
	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' || !json.Valid(line) {
			return nil, false
		}
		records = append(records, json.RawMessage(line))
	}
	return records, len(records) != 0
}
