package profile

import "errors"

var (
	// ErrInUse means another Agent owns this hosted execution profile.
	ErrInUse = errors.New("profile: hosted profile is in use")
	// ErrUnsafe means directory identity, ownership, or control records cannot be verified.
	ErrUnsafe = errors.New("profile: unsafe hosted profile")
	// ErrRecoveryRequired means a prior generation did not complete clean shutdown.
	// Stop all writers and perform the documented offline recovery before reuse.
	ErrRecoveryRequired = errors.New("profile: hosted profile requires offline recovery")
	// ErrUnsupportedFilesystem means reliable local locking or private permissions cannot be established.
	ErrUnsupportedFilesystem = errors.New("profile: hosted profile filesystem is unsupported")
)
