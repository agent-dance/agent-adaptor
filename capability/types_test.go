package capability_test

import (
	"github.com/agent-dance/agent-adaptor/capability"
	"testing"
	"time"
)

func TestInvocationDurationPresence(t *testing.T) {
	v := capability.Invocation{}
	if v.Duration != nil {
		t.Fatal("zero duration must be unobserved")
	}
	d := time.Duration(0)
	v.Duration = &d
	if v.Duration == nil || *v.Duration != 0 {
		t.Fatal("observed zero lost")
	}
	if capability.Started == capability.Completed || capability.HostLifecycle == capability.Relayed {
		t.Fatal("distinct closed states collapsed")
	}
}
