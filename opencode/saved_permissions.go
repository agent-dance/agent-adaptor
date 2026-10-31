package opencode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
)

// Native saved project grants are evaluated after agent/session rules and can
// therefore bypass Ask or auto-deny. The public API cannot replace these grants.
// Use the provider's read-only db command, never edit a user's permission table.
// Permission.list initializes the native per-instance cache between two empty
// observations; subsequent SDK replies are always "once" and cannot add grants.
func (p *serverProcess) verifySavedPermissions(ctx context.Context, c Config, env []driver.EnvBinding, sink driver.EventSink) (audit driver.RawStreams, err error) {
	if p.permissionsVerified {
		return audit, nil
	}
	raw, err := p.client.call(ctx, "GET", "/project/current", nil)
	if err != nil {
		return audit, err
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := decodeNativeObject(raw, &project); err != nil || project.ID == "" || strings.ContainsRune(project.ID, 0) {
		return audit, errors.New("opencode: permission probe requires the native project identity")
	}
	command := c.Command
	if command == "" {
		command = "opencode"
	}
	// Encode the opaque identity as a SQL blob cast to text, keeping it out of
	// SQL syntax and command-wrapper metacharacters on every platform.
	query := "SELECT data FROM permission WHERE project_id = CAST(X'" + hex.EncodeToString([]byte(project.ID)) + "' AS TEXT)"
	for probe := 0; probe < 2; probe++ {
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result, runErr := clihelper.Run(probeCtx, clihelper.CommandRequest{Command: command, Args: []string{"db", query, "--format", "json"}, CWD: p.client.cwd, Env: env}, sink)
		runErr = errors.Join(runErr, context.Cause(probeCtx))
		cancel()
		audit.Stdout += result.RawStreams.Stdout
		audit.Stderr += result.RawStreams.Stderr
		if runErr != nil {
			return audit, fmt.Errorf("opencode: native saved-permission probe: %w", runErr)
		}
		if result.ExitCode != 0 {
			return audit, errors.New("opencode: native saved-permission probe exited unsuccessfully")
		}
		var rows []struct {
			Data *string `json:"data"`
		}
		if err := decodeNativeObject([]byte(result.RawStreams.Stdout), &rows); err != nil || len(rows) > 1 {
			return audit, errors.New("opencode: malformed saved-permission query result")
		}
		if len(rows) == 1 {
			if rows[0].Data == nil {
				return audit, errors.New("opencode: saved-permission query omitted data")
			}
			rules, err := permissionRules(json.RawMessage(*rows[0].Data))
			if err != nil || strings.TrimSpace(*rows[0].Data) == "null" {
				return audit, errors.New("opencode: invalid saved native permission rules")
			}
			if len(rules) != 0 {
				return audit, fmt.Errorf("opencode: saved native project permissions cannot be reconciled with explicit Ask/auto-deny: %w", errors.ErrUnsupported)
			}
		}
		if probe == 0 {
			pending, err := p.client.call(ctx, "GET", "/permission", nil)
			if err != nil {
				return audit, err
			}
			var requests []json.RawMessage
			if err := decodeNativeObject(pending, &requests); err != nil || len(requests) != 0 {
				return audit, errors.New("opencode: permission cache was not initialized in an idle instance")
			}
		}
	}
	p.permissionsVerified = true
	return audit, nil
}
