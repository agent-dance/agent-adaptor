package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/contractdriver"
)

func main() {
	fail := flag.Bool("fail", false, "Return a completed run with a structured failure")
	flag.Parse()

	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(contractdriver.New(contractdriver.Config{
		Output: "deterministic assistant output",
		Fail:   *fail,
	})))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sdk.Run(ctx, "demonstrate the result contract")
	if err != nil {
		log.Fatalf("infrastructure error: %v", err)
	}
	if result.Failure != nil {
		fmt.Printf("business failure code=%s message=%s\n", result.Failure.Code, result.Failure.Message)
		return
	}

	fmt.Printf("output=%q summary=%q transcript=%d result=%v stdout=%q\n",
		result.Output, result.Summary, len(result.Transcript), result.Result, result.RawStreams.Stdout)
}
