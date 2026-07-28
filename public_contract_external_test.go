package adaptor_test

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/skill"
)

type externalServiceManager struct{}

func (externalServiceManager) Ensure(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
	return nil, nil
}
func (externalServiceManager) ReleaseByRun(context.Context, string) error { return nil }
func (externalServiceManager) ReleaseByLabels(context.Context, map[string]string) error {
	return nil
}

type externalWorkspaceManager struct{}

func (externalWorkspaceManager) Resolve(context.Context, adaptor.WorkspaceRequest) (adaptor.WorkspaceLease, error) {
	return adaptor.WorkspaceLease{}, nil
}
func (externalWorkspaceManager) Release(context.Context, adaptor.WorkspaceLease, adaptor.WorkspaceReleaseMode) error {
	return nil
}

var (
	_ adaptor.ServiceManager   = externalServiceManager{}
	_ adaptor.WorkspaceManager = externalWorkspaceManager{}

	_ *adaptor.SkillKeyConflictError            = (*skill.SkillKeyConflictError)(nil)
	_ *adaptor.SkillMaterializationError        = (*skill.SkillMaterializationError)(nil)
	_ *adaptor.InvalidOutputSchemaError         = (*driver.InvalidOutputSchemaError)(nil)
	_ *adaptor.StructuredOutputUnsupportedError = (*driver.StructuredOutputUnsupportedError)(nil)
	_ adaptor.SharedOption                      = adaptor.WithMCP(mcp.Stdio("tools", "tool"))
)

func TestPublicErrorSentinelsShareLeafIdentity(t *testing.T) {
	cases := []struct {
		root error
		leaf error
	}{
		{adaptor.ErrSkillKeyConflict, skill.ErrSkillKeyConflict},
		{adaptor.ErrSkillMaterializationFailed, skill.ErrSkillMaterializationFailed},
		{adaptor.ErrSkillSourceMissing, skill.ErrSkillSourceMissing},
		{adaptor.ErrSkillKeyMissing, skill.ErrSkillKeyMissing},
		{adaptor.ErrSkillNotFound, skill.ErrSkillNotFound},
		{adaptor.ErrInvalidMCPConfig, mcp.ErrInvalidConfig},
		{adaptor.ErrMCPUnsupported, mcp.ErrUnsupported},
		{adaptor.ErrMCPTransportUnsupported, mcp.ErrTransportUnsupported},
		{adaptor.ErrInvalidOutputSchema, driver.ErrInvalidOutputSchema},
		{adaptor.ErrStructuredOutputUnsupported, driver.ErrStructuredOutputUnsupported},
	}
	for _, tc := range cases {
		if tc.root != tc.leaf || !errors.Is(tc.root, tc.leaf) {
			t.Fatalf("root sentinel %q does not share leaf identity %q", tc.root, tc.leaf)
		}
	}
}
