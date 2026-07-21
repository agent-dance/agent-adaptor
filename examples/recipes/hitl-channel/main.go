package main

import (
	"context"
	"fmt"
	"log"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/contractdriver"
)

func main() {
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(contractdriver.New(contractdriver.Config{
		DecisionKind: agentadaptor.HumanDecisionPlanReview,
	})))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	handle, err := sdk.Start(ctx, "request a plan review",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
			PlanReview: agentadaptor.HumanDecisionAsk,
			Timeout:    3 * time.Second,
		}}),
	)
	if err != nil {
		log.Fatal(err)
	}

	select {
	case request := <-handle.DecisionRequests():
		fmt.Printf("request id=%s kind=%s prompt=%q\n", request.RequestID, request.Kind, request.Prompt)
		if err := handle.ResolveDecision(request.RequestID, agentadaptor.DecisionResponse{
			Result: agentadaptor.DecisionApproved,
		}); err != nil {
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Fatal(ctx.Err())
	}

	result, err := handle.Wait(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("decision failed: %s", result.Failure.Message)
	}
	fmt.Println(result.Output)
}
