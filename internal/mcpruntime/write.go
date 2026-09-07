package mcpruntime

import (
	"fmt"
	"os"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

// writeProfileConfig keeps the permissions of an existing regular provider
// configuration. Reconciliation must not turn a content update into permission
// drift; a missing file retains the materializer's established default mode.
func writeProfileConfig(path string, raw []byte) error {
	mode := os.FileMode(0644)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: MCP configuration is not a regular file", profile.ErrUnsafe)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return profilestate.AtomicWriteFile(path, raw, mode)
}
