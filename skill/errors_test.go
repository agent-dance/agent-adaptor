package skill_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/skill"
)

func TestSkillErrorIdentityAndCause(t *testing.T) {
	sources := []string{"run", "binding"}
	conflict := &skill.SkillKeyConflictError{Key: "review", Sources: sources}
	if !errors.Is(conflict, skill.ErrSkillKeyConflict) {
		t.Fatal("conflict must match ErrSkillKeyConflict")
	}
	var typedConflict *skill.SkillKeyConflictError
	if !errors.As(conflict, &typedConflict) || typedConflict != conflict {
		t.Fatal("conflict must preserve its concrete skill error identity")
	}
	_ = conflict.Error()
	if !reflect.DeepEqual(sources, []string{"run", "binding"}) {
		t.Fatalf("Error sorted the caller-owned Sources in place: %v", sources)
	}

	cause := errors.New("disk full")
	materialization := &skill.SkillMaterializationError{Key: "review", Cause: cause}
	if !errors.Is(materialization, skill.ErrSkillMaterializationFailed) {
		t.Fatal("materialization must match ErrSkillMaterializationFailed")
	}
	if !errors.Is(materialization, cause) {
		t.Fatal("materialization must preserve the lower-level cause")
	}
	var typedMaterialization *skill.SkillMaterializationError
	if !errors.As(materialization, &typedMaterialization) || typedMaterialization != materialization {
		t.Fatal("materialization must preserve its concrete skill error identity")
	}
}
