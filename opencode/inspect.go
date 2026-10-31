package opencode

import (
	"context"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func (d configuredDriver) CheckEnvironment(_ context.Context, value any) (driver.EnvironmentReport, error) {
	c, err := d.config(value)
	if err != nil {
		return driver.EnvironmentReport{}, err
	}
	if err := d.ValidateConfig(value); err != nil {
		return driver.EnvironmentReport{}, err
	}
	command := c.Command
	if command == "" {
		command = "opencode"
	}
	return driverutil.SummarizeEnvironment(DriverType, append(driverutil.CommandEnvironmentChecks(command), driverutil.CWDEnvironmentChecks(c.CWD)...)), nil
}
func (d configuredDriver) GetProfile(ctx context.Context, value any, _ driver.AgentIdentity, selection *driver.ProfileSelection) (driver.AgentProfile, error) {
	c, err := d.config(value)
	if err != nil {
		return driver.AgentProfile{}, err
	}
	p, err := resolveProfile(ctx, c, selection, false)
	return p.Profile, err
}
func (d configuredDriver) ConfigSchema(_ context.Context, value any) (*driver.ConfigSchema, error) {
	if err := d.ValidateConfig(value); err != nil {
		return nil, err
	}
	return d.Descriptor().ConfigSchema, nil
}
func (d configuredDriver) DetectModel(_ context.Context, value any, _ *driver.ProfileSelection) (*driver.DetectedModel, error) {
	c, err := d.config(value)
	if err != nil {
		return nil, err
	}
	if c.Model == "" {
		return nil, nil
	}
	provider, _, _ := strings.Cut(c.Model, "/")
	return &driver.DetectedModel{Model: c.Model, Provider: provider, Source: "config"}, nil
}
func (configuredDriver) GetQuota(context.Context, any, *driver.ProfileSelection) (driver.QuotaReport, error) {
	return driver.QuotaReport{DriverType: DriverType, Available: false, Error: "OpenCode run does not expose provider quota"}, nil
}
func (d configuredDriver) ListSkills(ctx context.Context, value any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, selection *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	c, err := d.config(value)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	p, err := resolveProfile(ctx, c, selection, false)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return skillruntime.ListProfileSkills(DriverType, p.Profile.Dir, payload, selected, resolved)
}
func (configuredDriver) InjectSkills(context.Context, any, driver.ResolvedSkills, *driver.ProfileSelection) error {
	// Run delivers explicit skills through invocation-local native settings.
	return nil
}
func (d configuredDriver) SyncSkills(ctx context.Context, value any, payload driver.ResolvedSkills, selected []string, resolved []driver.Skill, selection *driver.ProfileSelection) (driver.SkillSnapshot, error) {
	c, err := d.config(value)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	p, err := resolveProfile(ctx, c, selection, true)
	if err != nil {
		return driver.SkillSnapshot{}, err
	}
	return skillruntime.SyncProfileSkills(ctx, DriverType, p.Profile.Dir, profileKind(c, p.Profile), payload, selected, resolved)
}
