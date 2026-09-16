//go:build !darwin && !linux && !windows

package cursor

import (
	"fmt"
	"os"
)

func openCursorRegularFile(string) (*os.File, error) {
	return nil, fmt.Errorf("cursor guarded configuration reads unavailable on this platform")
}
