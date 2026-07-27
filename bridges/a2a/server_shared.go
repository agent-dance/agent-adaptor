package a2a

import (
	"context"
	"fmt"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// execCtxAdapter and inboundFromExecCtx isolate the official A2A executor
// DTO from the bridge's stable inbound request vocabulary.
type execCtxAdapter struct {
	*a2asrv.ExecutorContext
}

func inboundFromExecCtx(ctx *a2asrv.ExecutorContext) (InboundRequest, error) {
	if ctx == nil {
		return InboundRequest{}, nil
	}
	return inboundRequest(execCtxAdapter{ctx})
}

func (e execCtxAdapter) TaskIDString() string    { return string(e.TaskID) }
func (e execCtxAdapter) ContextIDString() string { return e.ContextID }
func (e execCtxAdapter) Message() *a2aproto.Message {
	return e.ExecutorContext.Message
}
func (e execCtxAdapter) MetadataMap() map[string]any { return e.Metadata }

func publicCard(card *a2aproto.AgentCard) AgentCard {
	if card == nil {
		return AgentCard{}
	}
	out := AgentCard{
		Name: card.Name, Description: card.Description, Version: card.Version,
		DocumentationURL: card.DocumentationURL, IconURL: card.IconURL,
		DefaultInputModes:  append([]string(nil), card.DefaultInputModes...),
		DefaultOutputModes: append([]string(nil), card.DefaultOutputModes...),
		Capabilities: Capabilities{
			Streaming:         capabilityMode(card.Capabilities.Streaming),
			PushNotifications: card.Capabilities.PushNotifications,
			ExtendedAgentCard: card.Capabilities.ExtendedAgentCard,
		},
		SecuritySchemes: publicSecuritySchemes(card.SecuritySchemes),
		Security:        publicSecurityRequirements(card.SecurityRequirements),
	}
	if card.Provider != nil {
		out.Provider = &Provider{Organization: card.Provider.Org, URL: card.Provider.URL}
	}
	for _, ext := range card.Capabilities.Extensions {
		out.Capabilities.Extensions = append(out.Capabilities.Extensions, Extension{
			URI: ext.URI, Description: ext.Description, Required: ext.Required, Params: cloneMap(ext.Params),
		})
	}
	for _, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		if out.URL == "" {
			out.URL = iface.URL
		}
		out.Interfaces = append(out.Interfaces, AgentInterface{
			URL: iface.URL, ProtocolBinding: string(iface.ProtocolBinding), Tenant: iface.Tenant,
			ProtocolVersion: string(iface.ProtocolVersion),
		})
	}
	for _, skill := range card.Skills {
		out.Skills = append(out.Skills, Skill{
			ID: skill.ID, Name: skill.Name, Description: skill.Description,
			Tags: append([]string(nil), skill.Tags...), Examples: append([]string(nil), skill.Examples...),
			InputModes: append([]string(nil), skill.InputModes...), OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	return out
}

func requestHandlerCapabilityOptions(card *a2aproto.AgentCard, pushSupport *PushNotificationSupport, extendedSupport *ExtendedAgentCardSupport) ([]a2asrv.RequestHandlerOption, error) {
	var out []a2asrv.RequestHandlerOption
	if card.Capabilities.PushNotifications {
		if pushSupport == nil {
			return nil, fmt.Errorf("a2a bridge: push notifications capability requires explicit PushNotifications support")
		}
		if pushSupport.Store == nil || pushSupport.Sender == nil {
			return nil, fmt.Errorf("a2a bridge: push notification support requires both Store and Sender")
		}
		out = append(out, a2asrv.WithPushNotifications(pushSupport.Store, pushSupport.Sender))
	} else if pushSupport != nil {
		return nil, fmt.Errorf("a2a bridge: push notifications support requires AgentCard.Capabilities.PushNotifications=true")
	}

	if card.Capabilities.ExtendedAgentCard {
		if extendedSupport == nil {
			return nil, fmt.Errorf("a2a bridge: extended agent card capability requires explicit ExtendedAgentCard support")
		}
		extendedOpt, err := extendedAgentCardOption(extendedSupport)
		if err != nil {
			return nil, err
		}
		out = append(out, extendedOpt)
	} else if extendedSupport != nil {
		return nil, fmt.Errorf("a2a bridge: extended agent card support requires AgentCard.Capabilities.ExtendedAgentCard=true")
	}
	return out, nil
}

func extendedAgentCardOption(support *ExtendedAgentCardSupport) (a2asrv.RequestHandlerOption, error) {
	if support == nil {
		return nil, fmt.Errorf("a2a bridge: extended agent card support is nil")
	}
	if support.Static != nil && support.Provider != nil {
		return nil, fmt.Errorf("a2a bridge: extended agent card support accepts either Static or Provider, not both")
	}
	if support.Static != nil {
		card, err := buildAgentCard(*support.Static)
		if err != nil {
			return nil, err
		}
		return a2asrv.WithExtendedAgentCard(card), nil
	}
	if support.Provider == nil {
		return nil, fmt.Errorf("a2a bridge: extended agent card support requires Static or Provider")
	}
	return a2asrv.WithExtendedAgentCardProducer(a2asrv.ExtendedAgentCardProducerFn(func(ctx context.Context, req *a2aproto.GetExtendedAgentCardRequest) (*a2aproto.AgentCard, error) {
		var tenant string
		if req != nil {
			tenant = req.Tenant
		}
		card, err := support.Provider.ExtendedCard(ctx, ExtendedAgentCardRequest{Tenant: tenant})
		if err != nil {
			return nil, err
		}
		return buildAgentCard(card)
	})), nil
}

func capabilityMode(enabled bool) CapabilityMode {
	if enabled {
		return CapabilityEnabled
	}
	return CapabilityDisabled
}
