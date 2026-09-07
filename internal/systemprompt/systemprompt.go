// Package systemprompt implements native text transport mechanics. Providers
// choose their own argv/RPC fields and own each materialized file's lifetime.
package systemprompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/pelletier/go-toml/v2"
)

// MaxInlineBytes limits the unencoded text of argv-based transports.
const MaxInlineBytes = 32 << 10

func unsupported(name, reason string) error {
	return &driver.SystemPromptUnsupportedError{Driver: name, Reason: reason}
}

// Validate rejects invalid UTF-8 before NUL, preserving every legal byte.
func Validate(driverType, text string) error {
	if !utf8.ValidString(text) {
		return unsupported(driverType, "invalid_utf8")
	}
	if strings.IndexByte(text, 0) >= 0 {
		return unsupported(driverType, "nul_byte")
	}
	return nil
}

// Fingerprint is SHA-256 of the original bytes, or empty for empty text.
func Fingerprint(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ValidateInline checks the exact UTF-8 bytes before transport encoding.
func ValidateInline(driverType, text string) error {
	if err := Validate(driverType, text); err != nil {
		return err
	}
	if len(text) > MaxInlineBytes {
		return unsupported(driverType, "inline_limit")
	}
	return nil
}

// TOMLString uses the existing TOML encoder and verifies a lossless round trip.
func TOMLString(text string) (string, error) {
	if err := Validate("", text); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(map[string]string{"value": text}); err != nil {
		return "", err
	}
	encoded := buf.String()
	var decoded map[string]string
	if err := toml.Unmarshal([]byte(encoded), &decoded); err != nil {
		return "", err
	}
	if decoded["value"] != text {
		return "", unsupported("", "invalid_utf8")
	}
	_, value, _ := strings.Cut(encoded, "=")
	return strings.TrimSpace(value), nil
}

// ValidateCommandLine checks the final executable/argv after shim preparation.
// On Windows the executable, separators, quoting, escapes and terminal NUL
// count against 32767 UTF-16 units; cmd shims also have an 8191-character bound.
// cmd's second parsing pass cannot losslessly carry shell metacharacters, so
// arguments after its "call" wrapper are conservatively rejected.
func ValidateCommandLine(driverType, command string, args []string, goos string) error {
	if err := Validate(driverType, command); err != nil {
		return err
	}
	for _, arg := range args {
		if err := Validate(driverType, arg); err != nil {
			return err
		}
	}
	if goos != "windows" {
		return nil
	}
	base := strings.ToLower(command)
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	cmdShim := base == "cmd" || base == "cmd.exe" || strings.HasSuffix(base, ".cmd") || strings.HasSuffix(base, ".bat")
	if cmdShim {
		start := 0
		for i, arg := range args {
			if strings.EqualFold(arg, "call") {
				start = i + 1
				break
			}
		}
		for _, arg := range args[start:] {
			if strings.ContainsAny(arg, "\r\n\"%!^&|<>()") {
				return unsupported(driverType, "unsafe_shell_argument")
			}
		}
	}
	units := len(utf16.Encode([]rune(windowsQuote(command)))) + 1
	for _, arg := range args {
		units += 1 + len(utf16.Encode([]rune(windowsQuote(arg))))
	}
	if units > 32767 || cmdShim && units > 8191 {
		return unsupported(driverType, "command_line_limit")
	}
	return nil
}

// windowsQuote mirrors the CommandLineToArgvW/Go exec quoting rules. cmd
// re-interpretation is checked separately and is never claimed to be covered.
func windowsQuote(text string) string {
	if text == "" {
		return `""`
	}
	if !strings.ContainsAny(text, " \t\"") {
		return text
	}
	var b strings.Builder
	hasSpace := strings.ContainsAny(text, " \t")
	if hasSpace {
		b.WriteByte('"')
	}
	slashes := 0
	for _, r := range text {
		if r == '\\' {
			slashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat("\\", 2*slashes+1))
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
		}
		slashes = 0
		b.WriteRune(r)
	}
	if hasSpace {
		b.WriteString(strings.Repeat("\\", 2*slashes))
		b.WriteByte('"')
	} else {
		b.WriteString(strings.Repeat("\\", slashes))
	}
	return b.String()
}
