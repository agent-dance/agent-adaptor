package main

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/adaptertest"
)

func TestAdapterConformance(t *testing.T) {
	adaptertest.Run(t, adaptertest.Subject{
		Name:    "example echo adapter",
		Adapter: adapter{},
		Config:  config{Prefix: "echo: "},
	})
}
