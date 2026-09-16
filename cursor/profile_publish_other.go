//go:build !darwin && !linux && !windows

package cursor

import (
	"fmt"
	"os"
)

func publishCursorPrivateHome(*os.Root, string, string) error {
	return fmt.Errorf("cursor private HOME atomic publication is unavailable on this platform")
}
