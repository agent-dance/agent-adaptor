package profile_test

import (
	"errors"
	"fmt"
	"github.com/agent-dance/agent-adaptor/profile"
	"testing"
)

func TestHostedProfileErrorsAreDistinctAndWrappable(t *testing.T) {
	values := []error{profile.ErrInUse, profile.ErrUnsafe, profile.ErrRecoveryRequired, profile.ErrUnsupportedFilesystem}
	for i, e := range values {
		wrapped := fmt.Errorf("prepare hosted profile: %w", e)
		for j, other := range values {
			if errors.Is(wrapped, other) != (i == j) {
				t.Fatalf("error identity %d/%d", i, j)
			}
		}
	}
}
