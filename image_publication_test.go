package adaptor_test

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestHostImageUsesUnifiedPublisherAndAuthoritativeOrder(t *testing.T) {
	var publisher adaptor.RunEventPublisher
	provider := observerProvider(adaptor.RunAttachment{BindEvents: func(p adaptor.RunEventPublisher) error {
		publisher = p
		if err := p(context.Background(), adaptor.ImageContent{}); err == nil {
			t.Fatal("invalid image admitted")
		}
		for _, url := range []string{"/one", "/two"} {
			image := adaptor.WithEventMeta(adaptor.ImageContent{
				MessageID: "message", Type: "binary", MIMEType: "image/png", URL: url,
				Role: adaptor.RoleUser, UserID: "host-user",
			}, adaptor.EventMeta{RunID: "forged-run", Sequence: 99})
			if err := p(context.Background(), image); err != nil {
				return err
			}
		}
		return nil
	}})
	agent := adaptor.New(newFakeDriver(), adaptor.WithRunServices(provider))
	defer agent.Close(context.Background())
	stream := agent.Stream(context.Background(), "work")
	var images []adaptor.ImageContent
	var last uint64
	for event := range stream.Events() {
		if event.Meta().RunID != stream.RunID() || event.Meta().Sequence <= last {
			t.Fatalf("non-authoritative envelope: %+v", event.Meta())
		}
		last = event.Meta().Sequence
		if image, ok := event.(adaptor.ImageContent); ok {
			images = append(images, image)
		}
	}
	if _, err := stream.Result(); err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].URL != "/one" || images[1].URL != "/two" || images[0].UserID != "host-user" {
		t.Fatalf("images lost, changed or reordered: %+v", images)
	}
	if err := publisher(context.Background(), images[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("publisher survived run cleanup: %v", err)
	}
}
