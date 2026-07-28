package adaptor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/skill"
)

// cleanupSkillDriver makes the pre-launch InjectSkills seam observable while
// retaining fakeDriver's request counter. Implementing the complete optional
// SkillSupport interface is important: the production type assertion must
// succeed before InjectSkills can fail the invocation.
type cleanupSkillDriver struct {
	*fakeDriver
	log       *callLog
	injectErr error
}

var _ driver.SkillSupport = (*cleanupSkillDriver)(nil)

func (d *cleanupSkillDriver) ListSkills(
	context.Context,
	any,
	driver.ResolvedSkills,
	[]string,
	[]driver.Skill,
	*driver.ProfileSelection,
) (driver.SkillSnapshot, error) {
	return driver.SkillSnapshot{}, nil
}

func (d *cleanupSkillDriver) InjectSkills(
	context.Context,
	any,
	driver.ResolvedSkills,
	*driver.ProfileSelection,
) error {
	d.log.add("inject")
	return d.injectErr
}

func (d *cleanupSkillDriver) SyncSkills(
	context.Context,
	any,
	driver.ResolvedSkills,
	[]string,
	[]driver.Skill,
	*driver.ProfileSelection,
) (driver.SkillSnapshot, error) {
	return driver.SkillSnapshot{}, nil
}

type cleanupMaterializerFunc func(context.Context, skill.Skill) (string, error)

func (f cleanupMaterializerFunc) Materialize(ctx context.Context, value skill.Skill) (string, error) {
	return f(ctx, value)
}

// TestSkillPreLaunchFailureCleansAcquiredRunResources preserves the v1 part
// of the deleted TestInjectSkillsErrorStopsRunAndCleansRuntime contract. Run
// resources are acquired before skill resolution, materialization, and driver
// injection, so a failure at any of those three pre-launch seams must unwind
// both kinds of acquired resource. The provider is detached first (reverse
// acquisition order), then manager-owned runtime services are released. The
// provider Driver.Run method must never begin.
func TestSkillPreLaunchFailureCleansAcquiredRunResources(t *testing.T) {
	resolveErr := adaptor.ErrSkillNotFound
	materializeErr := errors.New("test materializer rejected skill")
	injectErr := errors.New("test driver rejected skill injection")

	tests := []struct {
		name          string
		ref           skill.Ref
		materializer  adaptor.SkillMaterializer
		injectErr     error
		wantErr       error
		wantInjection bool
	}{
		{
			name:    "resolve",
			ref:     skill.Key("team/missing"),
			wantErr: resolveErr,
		},
		{
			name: "materialize",
			ref:  skill.Inline("team/materialize-failure", "# materialize failure\n"),
			materializer: cleanupMaterializerFunc(func(context.Context, skill.Skill) (string, error) {
				return "", materializeErr
			}),
			wantErr: materializeErr,
		},
		{
			name: "inject",
			ref:  skill.Inline("team/inject-failure", "# inject failure\n"),
			materializer: cleanupMaterializerFunc(func(context.Context, skill.Skill) (string, error) {
				return t.TempDir(), nil
			}),
			injectErr:     injectErr,
			wantErr:       injectErr,
			wantInjection: true,
		},
	}

	apis := []struct {
		name   string
		invoke func(context.Context, *adaptor.Agent, skill.Ref) error
	}{
		{
			name: "Run",
			invoke: func(ctx context.Context, agent *adaptor.Agent, ref skill.Ref) error {
				_, err := agent.Run(ctx, "go", adaptor.WithSkills(ref))
				return err
			},
		},
		{
			name: "Stream",
			invoke: func(ctx context.Context, agent *adaptor.Agent, ref skill.Ref) error {
				stream := agent.Stream(ctx, "go", adaptor.WithSkills(ref))
				for range stream.Events() {
				}
				_, err := stream.Result()
				return err
			},
		},
	}

	for _, test := range tests {
		for _, api := range apis {
			t.Run(test.name+"/"+api.name, func(t *testing.T) {
				log := &callLog{}
				manager := &fakeServiceManager{
					log: log,
					ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
						return []adaptor.ServiceRef{{ID: "runtime", Name: "runtime"}}, nil
					},
				}
				provider := &fakeProvider{name: "sidecar", log: log}
				fake := &cleanupSkillDriver{
					fakeDriver: newFakeDriver(),
					log:        log,
					injectErr:  test.injectErr,
				}

				options := []adaptor.Option{
					adaptor.WithServiceManager(manager),
					adaptor.WithServices(adaptor.ServiceSpec{ID: "runtime", Name: "runtime"}),
					adaptor.WithRunServices(provider),
				}
				if test.materializer != nil {
					options = append(options, adaptor.WithSkillMaterializer(test.materializer))
				}
				agent := adaptor.New(fake, options...)

				err := api.invoke(context.Background(), agent, test.ref)
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, test.wantErr)
				}
				if got := fake.runCount(); got != 0 {
					t.Fatalf("Driver.Run calls = %d, want 0 for a pre-launch skill failure", got)
				}

				wantPhases := []string{"ensure", "attach"}
				if test.wantInjection {
					wantPhases = append(wantPhases, "inject")
				}
				wantPhases = append(wantPhases, "detach", "release_run")
				if got := cleanupPhases(log.snapshot()); fmt.Sprint(got) != fmt.Sprint(wantPhases) {
					t.Fatalf("resource lifecycle = %v, want %v; raw log = %v", got, wantPhases, log.snapshot())
				}
			})
		}
	}
}

func cleanupPhases(entries []string) []string {
	phases := make([]string, 0, len(entries))
	for _, entry := range entries {
		switch {
		case entry == "inject":
			phases = append(phases, entry)
		case strings.HasPrefix(entry, "ensure:"):
			phases = append(phases, "ensure")
		case strings.HasPrefix(entry, "attach:"):
			phases = append(phases, "attach")
		case strings.HasPrefix(entry, "detach:"):
			phases = append(phases, "detach")
		case strings.HasPrefix(entry, "release_run:"):
			phases = append(phases, "release_run")
		}
	}
	return phases
}
