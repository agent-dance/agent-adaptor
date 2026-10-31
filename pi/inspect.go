package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

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
		command = "pi"
	}
	checks := append(driverutil.CommandEnvironmentChecks(command), driverutil.CWDEnvironmentChecks(c.CWD)...)
	return driverutil.SummarizeEnvironment(DriverType, checks), nil
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
func (d configuredDriver) DetectModel(ctx context.Context, value any, selection *driver.ProfileSelection) (*driver.DetectedModel, error) {
	c, err := d.config(value)
	if err != nil {
		return nil, err
	}
	if c.Model != "" {
		return &driver.DetectedModel{Model: c.Model, Provider: c.Provider, Source: "config"}, nil
	}
	p, err := resolveProfile(ctx, c, selection, false)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(p.Profile.Dir, "settings.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var settings struct {
		DefaultModel    string `json:"defaultModel"`
		DefaultProvider string `json:"defaultProvider"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	if settings.DefaultModel == "" {
		return nil, nil
	}
	return &driver.DetectedModel{Model: settings.DefaultModel, Provider: settings.DefaultProvider, Source: "settings.json"}, nil
}

// ListModels reports configured custom models, not an invented static catalogue.
func (d configuredDriver) ListModels(ctx context.Context, value any) ([]driver.ModelInfo, error) {
	c, err := d.config(value)
	if err != nil {
		return nil, err
	}
	p, err := resolveProfile(ctx, c, nil, false)
	if err != nil {
		return nil, err
	}
	models := map[string]bool{}
	if c.Model != "" {
		models[c.Model] = true
	}
	raw, err := os.ReadFile(filepath.Join(p.Profile.Dir, "models.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		var catalog struct {
			Providers map[string]struct {
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return nil, err
		}
		for provider, entry := range catalog.Providers {
			for _, model := range entry.Models {
				if model.ID != "" {
					models[provider+"/"+model.ID] = true
				}
			}
		}
	}
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]driver.ModelInfo, 0, len(keys))
	for _, key := range keys {
		out = append(out, driver.ModelInfo{ID: key, Label: key})
	}
	return out, nil
}
func (configuredDriver) GetQuota(context.Context, any, *driver.ProfileSelection) (driver.QuotaReport, error) {
	return driver.QuotaReport{DriverType: DriverType, Available: false, Error: "Pi does not expose a quota probe"}, nil
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
