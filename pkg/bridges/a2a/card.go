package a2a

import (
	"fmt"
	"sort"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

func buildAgentCard(in AgentCard) (*a2aproto.AgentCard, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("a2a bridge: agent card name is required")
	}
	if in.Version == "" {
		return nil, fmt.Errorf("a2a bridge: agent card version is required")
	}
	if in.URL == "" && len(in.Interfaces) == 0 {
		return nil, fmt.Errorf("a2a bridge: agent card URL or interfaces are required")
	}

	card := &a2aproto.AgentCard{
		Name:               in.Name,
		Description:        in.Description,
		Version:            in.Version,
		DocumentationURL:   in.DocumentationURL,
		IconURL:            in.IconURL,
		DefaultInputModes:  defaultModes(in.DefaultInputModes),
		DefaultOutputModes: defaultModes(in.DefaultOutputModes),
		Capabilities: a2aproto.AgentCapabilities{
			Streaming:         in.Capabilities.Streaming.enabledByDefault(true),
			PushNotifications: in.Capabilities.PushNotifications,
			ExtendedAgentCard: in.Capabilities.ExtendedAgentCard,
		},
	}
	if in.Provider != nil {
		card.Provider = &a2aproto.AgentProvider{Org: in.Provider.Organization, URL: in.Provider.URL}
	}
	for _, ext := range in.Capabilities.Extensions {
		card.Capabilities.Extensions = append(card.Capabilities.Extensions, a2aproto.AgentExtension{
			URI: ext.URI, Description: ext.Description, Required: ext.Required, Params: cloneMap(ext.Params),
		})
	}
	if len(in.Interfaces) == 0 {
		card.SupportedInterfaces = append(card.SupportedInterfaces, a2aproto.NewAgentInterface(in.URL, a2aproto.TransportProtocolJSONRPC))
	} else {
		for _, iface := range in.Interfaces {
			u := iface.URL
			if u == "" {
				u = in.URL
			}
			if u == "" {
				return nil, fmt.Errorf("a2a bridge: interface URL is required")
			}
			protocol := a2aproto.TransportProtocol(iface.ProtocolBinding)
			if protocol == "" {
				protocol = a2aproto.TransportProtocolJSONRPC
			}
			up := a2aproto.NewAgentInterface(u, protocol)
			up.Tenant = iface.Tenant
			if iface.ProtocolVersion != "" {
				up.ProtocolVersion = a2aproto.ProtocolVersion(iface.ProtocolVersion)
			}
			card.SupportedInterfaces = append(card.SupportedInterfaces, up)
		}
	}
	for _, skill := range in.Skills {
		if skill.ID == "" {
			return nil, fmt.Errorf("a2a bridge: skill id is required")
		}
		card.Skills = append(card.Skills, a2aproto.AgentSkill{
			ID: skill.ID, Name: skill.Name, Description: skill.Description,
			Tags: append([]string(nil), skill.Tags...), Examples: append([]string(nil), skill.Examples...),
			InputModes: append([]string(nil), skill.InputModes...), OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	card.SecuritySchemes = convertSecuritySchemes(in.SecuritySchemes)
	card.SecurityRequirements = convertSecurityRequirements(in.Security)
	return card, nil
}

func convertSecuritySchemes(in []SecurityScheme) a2aproto.NamedSecuritySchemes {
	if len(in) == 0 {
		return nil
	}
	out := make(a2aproto.NamedSecuritySchemes, len(in))
	for _, scheme := range in {
		if scheme.Name == "" {
			continue
		}
		switch scheme.Type {
		case SecurityAPIKey:
			location := a2aproto.APIKeySecuritySchemeLocation(scheme.In)
			if location == "" {
				location = a2aproto.APIKeySecuritySchemeLocationHeader
			}
			out[a2aproto.SecuritySchemeName(scheme.Name)] = a2aproto.APIKeySecurityScheme{
				Description: scheme.Description, Location: location, Name: scheme.ParamName,
			}
		case SecurityMutualTLS:
			out[a2aproto.SecuritySchemeName(scheme.Name)] = a2aproto.MutualTLSSecurityScheme{Description: scheme.Description}
		default:
			httpScheme := scheme.Scheme
			if httpScheme == "" {
				httpScheme = "Bearer"
			}
			out[a2aproto.SecuritySchemeName(scheme.Name)] = a2aproto.HTTPAuthSecurityScheme{
				Description: scheme.Description, Scheme: httpScheme, BearerFormat: scheme.BearerFormat,
			}
		}
	}
	return out
}

func convertSecurityRequirements(in []SecurityRequirement) a2aproto.SecurityRequirementsOptions {
	if len(in) == 0 {
		return nil
	}
	out := make(a2aproto.SecurityRequirementsOptions, 0, len(in))
	for _, req := range in {
		one := a2aproto.SecurityRequirements{}
		for name, scopes := range req.Schemes {
			one[a2aproto.SecuritySchemeName(name)] = append(a2aproto.SecuritySchemeScopes(nil), scopes...)
		}
		if len(one) > 0 {
			out = append(out, one)
		}
	}
	return out
}

func publicSecuritySchemes(in a2aproto.NamedSecuritySchemes) []SecurityScheme {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for name := range in {
		names = append(names, string(name))
	}
	sort.Strings(names)

	out := make([]SecurityScheme, 0, len(names))
	for _, name := range names {
		scheme := in[a2aproto.SecuritySchemeName(name)]
		pub := SecurityScheme{Name: name}
		switch s := scheme.(type) {
		case a2aproto.APIKeySecurityScheme:
			pub.Type = SecurityAPIKey
			pub.Description = s.Description
			pub.In = string(s.Location)
			pub.ParamName = s.Name
		case a2aproto.HTTPAuthSecurityScheme:
			pub.Type = SecurityHTTP
			pub.Description = s.Description
			pub.Scheme = s.Scheme
			pub.BearerFormat = s.BearerFormat
		case a2aproto.MutualTLSSecurityScheme:
			pub.Type = SecurityMutualTLS
			pub.Description = s.Description
		default:
			continue
		}
		out = append(out, pub)
	}
	return out
}

func publicSecurityRequirements(in a2aproto.SecurityRequirementsOptions) []SecurityRequirement {
	if len(in) == 0 {
		return nil
	}
	out := make([]SecurityRequirement, 0, len(in))
	for _, req := range in {
		pub := SecurityRequirement{Schemes: map[string][]string{}}
		for name, scopes := range req {
			pub.Schemes[string(name)] = append([]string(nil), scopes...)
		}
		if len(pub.Schemes) > 0 {
			out = append(out, pub)
		}
	}
	return out
}

func defaultModes(modes []string) []string {
	if len(modes) == 0 {
		return []string{"text/plain"}
	}
	return append([]string(nil), modes...)
}

func (m CapabilityMode) enabledByDefault(fallback bool) bool {
	switch m {
	case CapabilityEnabled:
		return true
	case CapabilityDisabled:
		return false
	default:
		return fallback
	}
}
