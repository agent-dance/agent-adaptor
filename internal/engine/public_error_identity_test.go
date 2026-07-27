package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/skill"
)

type failingSkillMaterializer struct{ err error }

func (m failingSkillMaterializer) Materialize(context.Context, Skill) (string, error) {
	return "", m.err
}

func TestEngineErrorsUsePublicLeafConcreteTypes(t *testing.T) {
	merger := newSkillMerger()
	if err := merger.add(sourceLabelDefault, Skill{Key: "review", Source: SkillFromInline{SkillMD: "one"}}); err != nil {
		t.Fatal(err)
	}
	err := merger.add(sourceLabelRun, Skill{Key: "review", Source: SkillFromInline{SkillMD: "two"}})
	var conflict *skill.SkillKeyConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, skill.ErrSkillKeyConflict) {
		t.Fatalf("merge error = %T %v, want public skill conflict", err, err)
	}

	cause := errors.New("materializer failed")
	_, _, _, err = resolveSkillsWith(
		context.Background(),
		nil,
		failingSkillMaterializer{err: cause},
		AgentIdentity{},
		[]SkillRef{Skill{Key: "review", Source: SkillFromInline{SkillMD: "one"}}},
		nil,
		nil,
	)
	var materialization *skill.SkillMaterializationError
	if !errors.As(err, &materialization) || !errors.Is(err, skill.ErrSkillMaterializationFailed) || !errors.Is(err, cause) {
		t.Fatalf("resolution error = %T %v, want public skill materialization", err, err)
	}

	_, err = normalizeOutputSchema(&OutputSchema{Format: "unsupported", SchemaJSON: []byte(`{}`)})
	var invalid *driver.InvalidOutputSchemaError
	if !errors.As(err, &invalid) || !errors.Is(err, driver.ErrInvalidOutputSchema) {
		t.Fatalf("schema error = %T %v, want public driver invalid schema", err, err)
	}

	_, err = resolveStructuredOutputSource(
		DriverDescriptor{Type: "fake"},
		&OutputSchema{Mode: StructuredOutputNativeStrict},
		false,
		RunPolicy{},
	)
	var unsupported *driver.StructuredOutputUnsupportedError
	if !errors.As(err, &unsupported) || !errors.Is(err, driver.ErrStructuredOutputUnsupported) {
		t.Fatalf("capability error = %T %v, want public driver unsupported", err, err)
	}
}
