package main

import (
	"context"
	"fmt"
	"log"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/contractdriver"
	"github.com/agent-dance/agent-adaptor/runtimeservice"
)

type manager struct {
	runtimeservice.NoopReleaseByLabels
	releasedRunID string
}

func (m *manager) Ensure(_ context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	return []agentadaptor.RuntimeServiceRef{{
		ID: "docs", Name: "docs", URL: "http://127.0.0.1:18080",
		Status: agentadaptor.RuntimeServiceRunning, Lifecycle: agentadaptor.RuntimeLifecycleEphemeral,
		Health: agentadaptor.RuntimeHealthHealthy, Metadata: map[string]string{"run_id": req.RunID},
	}}, nil
}

func (m *manager) ReleaseByRun(_ context.Context, runID string) error {
	m.releasedRunID = runID
	return nil
}

func main() {
	runtimeManager := &manager{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(contractdriver.New(contractdriver.Config{},
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
				ID: "docs", Name: "docs", Lifecycle: agentadaptor.RuntimeLifecycleEphemeral,
			}),
		)),
		agentadaptor.WithRuntimeServiceManager(runtimeManager),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sdk.Run(ctx, "use the runtime service")
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("run failed: %s", result.Failure.Message)
	}
	if runtimeManager.releasedRunID != result.RunID {
		log.Fatalf("runtime was not released for run %s", result.RunID)
	}
	fmt.Printf("run=%s services=%v released=%s\n", result.RunID, result.RuntimeServices, runtimeManager.releasedRunID)
}
