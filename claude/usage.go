package claude

import "github.com/agent-dance/agent-adaptor/v2/driver"

// Assistant usage describes one model message, whereas terminal result usage
// describes the whole agent loop, potentially containing several Stop segments.
func (p *claudeParser) observeAssistantUsage(message map[string]any) {
	raw := claudeTopLevelObject(message, "usage")
	input, hasInput := claudeTopLevelInt(raw, "input_tokens")
	output, hasOutput := claudeTopLevelInt(raw, "output_tokens")
	cached, hasCached := claudeTopLevelInt(raw, "cache_read_input_tokens")
	if !hasInput && !hasOutput && !hasCached {
		return
	}
	id := claudeExactString(message, "id")
	previous := p.turnMessageUsage[id]
	if id == "" {
		previous = driver.Usage{}
	}
	current := previous
	if hasInput {
		current.InputTokens = input
	}
	if hasOutput {
		current.OutputTokens = output
	}
	if hasCached {
		current.CachedInputTokens = cached
	}
	if id != "" {
		if p.turnMessageUsage == nil {
			p.turnMessageUsage = make(map[string]driver.Usage)
		}
		// Multiple assistant blocks can carry snapshots of the same message.
		p.turnMessageUsage[id] = current
	}
	delta := driver.Usage{
		InputTokens:       current.InputTokens - previous.InputTokens,
		OutputTokens:      current.OutputTokens - previous.OutputTokens,
		CachedInputTokens: current.CachedInputTokens - previous.CachedInputTokens,
	}
	p.turnUsage = addClaudeUsage(p.turnUsage, &delta)
}

func addClaudeUsage(a, b *driver.Usage) *driver.Usage {
	if a == nil && b == nil {
		return nil
	}
	u := driver.Usage{}
	if a != nil {
		u = *a
	}
	if b != nil {
		u.InputTokens += b.InputTokens
		u.OutputTokens += b.OutputTokens
		u.CachedInputTokens += b.CachedInputTokens
		u.EstimatedCostMilli += b.EstimatedCostMilli
	}
	return &u
}

func (p *claudeParser) ensureAggregateUsage() {
	if p.usage == nil {
		p.usage = addClaudeUsage(p.completedUsage, p.turnUsage)
	}
}

func (p *claudeParser) finalTurnUsage() *driver.Usage {
	if p.usage == nil {
		return p.turnUsage
	}
	// Reconcile the final segment against terminal aggregate usage, excluding
	// prior segments. This also covers transports without assistant usage and
	// preserves aggregate fields that assistant snapshots do not report.
	u := *p.usage
	if previous := p.completedUsage; previous != nil {
		u.InputTokens -= previous.InputTokens
		u.OutputTokens -= previous.OutputTokens
		u.CachedInputTokens -= previous.CachedInputTokens
		u.EstimatedCostMilli -= previous.EstimatedCostMilli
		if u.InputTokens < 0 || u.OutputTokens < 0 || u.CachedInputTokens < 0 || u.EstimatedCostMilli < 0 {
			return p.turnUsage
		}
	}
	return &u
}
