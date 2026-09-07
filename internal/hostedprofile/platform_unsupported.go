//go:build !linux && !darwin && !windows

package hostedprofile

import (
	"github.com/agent-dance/agent-adaptor/profile"
	"os"
)

func canonicalDirectory(string) (string, string, error) {
	return "", "", profile.ErrUnsupportedFilesystem
}
func validateObject(*os.File, bool) error { return profile.ErrUnsupportedFilesystem }
func secureObject(*os.File, bool) error   { return profile.ErrUnsupportedFilesystem }
func noFollowFlags() int                  { return 0 }
func lockFile(*os.File) error             { return profile.ErrUnsupportedFilesystem }
func unlockFile(*os.File) error           { return profile.ErrUnsupportedFilesystem }
func syncDirectory(*os.File) error        { return profile.ErrUnsupportedFilesystem }

func renameWithin(*os.Root, string, string) error { return profile.ErrUnsupportedFilesystem }

func publishWithin(*os.Root, string, string) error { return profile.ErrUnsupportedFilesystem }

func openOwnershipLock(*os.Root) (*os.File, error) { return nil, profile.ErrUnsupportedFilesystem }
