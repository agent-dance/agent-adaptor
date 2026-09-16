package cursor

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
)

const cursorSessionConfigState = "cursor_config_state"

// cli-config.json mixes native settings with authentication and mutable
// caches. Only the formal authentication field is excluded from the guard. Unknown
// settings remain in the guard, so a newer CLI cannot silently change resume
// semantics. Clone seeds use the separately enumerated static schema fields.
func cursorConfigState(bindings []driver.EnvBinding) (string, error) {
	data, err := cursorConfiguration(bindings, true)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func cursorStaticConfig(bindings []driver.EnvBinding) ([]byte, error) {
	return cursorConfiguration(bindings, false)
}

func cursorConfiguration(bindings []driver.EnvBinding, guard bool) ([]byte, error) {
	file, err := openCursorConfigFile(filepath.Join(resolveCursorConfigDir(bindings), "cli-config.json"))
	values := map[string]json.RawMessage{}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 1<<20 {
			return nil, fmt.Errorf("cursor CLI configuration exceeds 1 MiB")
		}
		if err := json.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("cursor CLI configuration is malformed: %w", err)
		}
		if values == nil {
			return nil, fmt.Errorf("cursor CLI configuration must be an object")
		}
	}
	static := map[string]any{}
	for key, raw := range values {
		switch key {
		case "authInfo":
			continue
		}
		if !guard {
			switch key {
			case "version", "editor", "display", "notifications", "hints", "modelSlashCommands", "rewind", "statusLine", "channel", "model", "bedrock", "awsAuthRefresh", "hasChangedDefaultModel", "maxMode", "maxModeAutoEnabled", "modelParameters", "selectedModel", "network", "approvalMode", "permissions", "autoAcceptWebSearch", "sandbox", "showSandboxIntro", "runEverythingSettingsPromptStreak", "runEverythingSettingsPromptCooldownUntilMs", "attribution", "webFetchDomainAllowlist":
			default:
				continue // Unknown values are guarded, never copied into AuthNone.
			}
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		static[key] = value
	}
	data, err := json.Marshal(static)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// AuthLink is intentionally supported, but only when its fully resolved target
// is regular. Nonblocking/no-follow platform opens also close the FIFO swap
// window between inspection and opening; bounded reads alone cannot do that.
func openCursorConfigFile(path string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(resolved)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > 1<<20 {
		return nil, fmt.Errorf("cursor CLI configuration must be a bounded regular file")
	}
	f, err := openCursorRegularFile(resolved)
	if err != nil {
		return nil, err
	}
	opened, statErr := f.Stat()
	current, pathErr := os.Lstat(resolved)
	if statErr != nil || pathErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(before, current) {
		f.Close()
		return nil, fmt.Errorf("cursor CLI configuration identity changed")
	}
	return f, nil
}
