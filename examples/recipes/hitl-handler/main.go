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

	result, err := sdk.Run(ctx, "request a plan review",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
			PlanReview: agentadaptor.HumanDecisionAsk,
			Timeout:    3 * time.Second,
		}}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, req agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			fmt.Println("PLAN:", req.Plan)
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("decision failed: %s", result.Failure.Message)
	}
	fmt.Println(result.Output)
}
