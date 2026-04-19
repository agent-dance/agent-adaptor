package agentadaptor

func summarizeEnvironment(driverType string, checks []EnvironmentCheck) EnvironmentReport {
	report := EnvironmentReport{
		DriverType: driverType,
		Status:     EnvironmentPass,
		Healthy:    true,
		Checks:     append([]EnvironmentCheck(nil), checks...),
	}
	for _, check := range checks {
		switch check.Level {
		case "error":
			report.Status = EnvironmentFail
			report.Healthy = false
		case "warn":
			if report.Status != EnvironmentFail {
				report.Status = EnvironmentWarn
			}
		}
	}
	if len(checks) == 0 {
		report.Summary = "no environment checks reported"
		return report
	}
	switch report.Status {
	case EnvironmentFail:
		report.Summary = "environment checks failed"
	case EnvironmentWarn:
		report.Summary = "environment checks completed with warnings"
	default:
		report.Summary = "environment checks passed"
	}
	return report
}
