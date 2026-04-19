package agentadaptor

func extractCommonConfig(cfg any) CommonConfig {
	switch value := cfg.(type) {
	case CommonConfig:
		return value
	case CodexConfig:
		return value.CommonConfig
	case ClaudeConfig:
		return value.CommonConfig
	case CursorConfig:
		return value.CommonConfig
	case *CodexConfig:
		if value != nil {
			return value.CommonConfig
		}
	case *ClaudeConfig:
		if value != nil {
			return value.CommonConfig
		}
	case *CursorConfig:
		if value != nil {
			return value.CommonConfig
		}
	}
	return CommonConfig{}
}
