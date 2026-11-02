package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// resultAudit explicitly includes the audit surfaces which Result keeps behind
// methods. A failed run can still contain the worker's complete partial result.
func resultAudit(r *adaptor.Result, err error) map[string]any {
	audit := map[string]any{"error": nil}
	var failure *adaptor.RunError
	if err != nil {
		audit["error"] = err.Error()
		if errors.As(err, &failure) && failure != nil {
			audit["failure"] = map[string]any{
				"reason": failure.Reason, "message": failure.Message, "details": failure.Details,
			}
			if r == nil {
				r = failure.Result
			}
		}
	}
	if r == nil {
		return audit
	}
	audit["text"] = r.Text
	audit["summary"] = r.Summary
	audit["runID"] = r.RunID
	audit["model"] = r.Model
	audit["provider"] = r.Provider
	audit["metadata"] = r.Metadata
	audit["turns"] = r.Turns
	audit["usage"] = r.Usage
	audit["raw"] = r.Raw()
	audit["transcript"] = r.Transcript()
	audit["services"] = r.Services()
	audit["messages"] = r.Messages()
	audit["subagents"] = r.Subagents()
	var decoded any
	if decodeErr := r.Decode(&decoded); decodeErr != nil {
		audit["decodeError"] = decodeErr.Error()
	} else {
		audit["decoded"] = decoded
	}
	return audit
}

// writeJSON never prints the supplied value or secrets. Redaction runs on JSON
// string values and keys so a secret containing quotes cannot damage syntax.
func writeJSON(path string, value any, secrets []string) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode audit JSON: %w", err)
	}
	data, err = redactJSONStrings(data, secrets)
	if err != nil {
		return err
	}
	if !json.Valid(data) {
		return errors.New("redacted audit JSON is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit JSON: %w", err)
	}
	defer file.Close()
	// OpenFile's mode applies only on creation. Restrict an existing report
	// before putting any newly observed content into it.
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict audit JSON permissions: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate audit JSON: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write audit JSON: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close audit JSON: %w", err)
	}
	return nil
}

func redactJSONStrings(data []byte, secrets []string) ([]byte, error) {
	var output strings.Builder
	for i := 0; i < len(data); {
		if data[i] != '"' {
			output.WriteByte(data[i])
			i++
			continue
		}
		start := i
		i++
		for i < len(data) {
			if data[i] == '\\' {
				i += 2
				continue
			}
			if data[i] == '"' {
				i++
				break
			}
			i++
		}
		var text string
		if err := json.Unmarshal(data[start:i], &text); err != nil {
			return nil, errors.New("decode audit JSON string for redaction")
		}
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			encoded, err := json.Marshal(secret)
			if err != nil {
				return nil, errors.New("encode audit redaction literal")
			}
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
		encoded, err := json.Marshal(text)
		if err != nil {
			return nil, errors.New("encode redacted audit JSON string")
		}
		output.Write(encoded)
	}
	return []byte(output.String()), nil
}
