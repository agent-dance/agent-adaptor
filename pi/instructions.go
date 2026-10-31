package pi

import (
	"errors"
	"fmt"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profileinstructions"
)

// prepareInstructions captures file-backed contents before resource acquisition.
// The provider transport receives one immutable, invocation-local bundle.
func prepareInstructions(ref *driver.InstructionsBundleRef) (*driver.InstructionsBundleRef, error) {
	if ref == nil {
		return nil, nil
	}
	switch ref.Scope {
	case driver.InstructionScopeDefault, driver.InstructionScopeUser, driver.InstructionScopeProject, driver.InstructionScopeLocal, driver.InstructionScopeRun:
	default:
		return nil, fmt.Errorf("pi: unsupported instruction scope %q: %w", ref.Scope, errors.ErrUnsupported)
	}
	switch ref.Mode {
	case driver.InstructionModeAdditive, driver.InstructionModeReplace:
	default:
		return nil, fmt.Errorf("pi: unsupported instruction mode %q: %w", ref.Mode, errors.ErrUnsupported)
	}
	if len(ref.Native) != 0 {
		return nil, fmt.Errorf("pi: native instruction extensions are unsupported: %w", errors.ErrUnsupported)
	}
	plan, err := profileinstructions.PlanNativeFile(ref, "")
	if err != nil {
		return nil, fmt.Errorf("pi instructions: %w", err)
	}
	copy := *ref
	copy.Path, copy.Content = "", plan.Content
	return &copy, nil
}
