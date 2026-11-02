package appserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// turnUsage keeps cumulative provider snapshots in execution turn order. Late
// snapshots for an earlier turn update that turn's accounting, never the active
// turn's text/lifecycle. Public TurnCompleted remains the usage observed at its
// completion boundary; Response includes all usage observed before return.
type turnUsage struct {
	total     *driver.Usage
	fixed     *driver.Usage // explicit per-turn terminal usage takes precedence
	usage     *driver.Usage
	completed bool
}

func subtractUsage(a, b *driver.Usage) *driver.Usage {
	if a == nil || b == nil {
		return nil
	}
	return &driver.Usage{InputTokens: a.InputTokens - b.InputTokens, OutputTokens: a.OutputTokens - b.OutputTokens, CachedInputTokens: a.CachedInputTokens - b.CachedInputTokens}
}
func nonnegativeUsage(u *driver.Usage) bool {
	return u != nil && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.CachedInputTokens >= 0
}

func (s *runState) absorbUsage(params json.RawMessage) {
	var body ThreadTokenUsageUpdatedNotification
	if err := json.Unmarshal(params, &body); err != nil {
		s.recordProtocolError(fmt.Errorf("decode thread/tokenUsage/updated: %w", err))
		return
	}
	total := &driver.Usage{InputTokens: body.TokenUsage.Total.InputTokens, OutputTokens: body.TokenUsage.Total.OutputTokens, CachedInputTokens: body.TokenUsage.Total.CachedInputTokens}
	last := &driver.Usage{InputTokens: body.TokenUsage.Last.InputTokens, OutputTokens: body.TokenUsage.Last.OutputTokens, CachedInputTokens: body.TokenUsage.Last.CachedInputTokens}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.usageTurns[body.TurnID]
	if record == nil || s.protocolErr != nil {
		return
	}
	if !nonnegativeUsage(total) || !nonnegativeUsage(last) || !nonnegativeUsage(subtractUsage(total, last)) {
		s.recordProtocolErrorLocked(errors.New("invalid thread token usage counters"))
		s.signalDone()
		return
	}
	if record.total != nil && !nonnegativeUsage(subtractUsage(total, record.total)) {
		// A replay of an older snapshot for this same turn cannot roll usage back.
		return
	}
	if s.usageBaseline == nil {
		s.usageBaseline = subtractUsage(total, last)
		// Earlier turns may have supplied only per-turn terminal usage. The
		// first cumulative snapshot already includes those turns.
		for _, id := range s.usageOrder {
			if id == body.TurnID {
				break
			}
			if fixed := s.usageTurns[id].fixed; fixed != nil {
				s.usageBaseline = subtractUsage(s.usageBaseline, fixed)
			}
		}
	}
	if s.historicalUsage != nil && nonnegativeUsage(subtractUsage(total, s.historicalUsage)) {
		// A resumed-history snapshot provides a stronger baseline than Last,
		// which only describes the most recent inference of the first turn.
		s.usageBaseline = cloneUsage(s.historicalUsage)
	}
	record.total = total
	s.recomputeUsageLocked()
}

func (s *runState) recomputeUsageLocked() {
	previous := s.usageBaseline
	var gap *driver.Usage
	s.totalUsage = nil
	s.usage = nil
	for _, id := range s.usageOrder {
		record := s.usageTurns[id]
		usage := record.fixed
		if record.total != nil {
			if usage == nil {
				usage = subtractUsage(record.total, previous)
				if gap != nil {
					usage = subtractUsage(usage, gap)
				}
				if !nonnegativeUsage(usage) {
					usage = record.usage
				}
			}
			previous = record.total
			gap = nil
		} else {
			gap = addUsage(gap, usage)
		}
		record.usage = cloneUsage(usage)
		if record.completed {
			s.totalUsage = addUsage(s.totalUsage, usage)
		}
		if id == s.turnID {
			s.usage = cloneUsage(usage)
		}
	}
}

func (s *runState) absorbHistoricalUsage(params json.RawMessage) {
	var body ThreadTokenUsageUpdatedNotification
	if json.Unmarshal(params, &body) != nil {
		return
	} // unrelated history is audit-only
	total := &driver.Usage{InputTokens: body.TokenUsage.Total.InputTokens, OutputTokens: body.TokenUsage.Total.OutputTokens, CachedInputTokens: body.TokenUsage.Total.CachedInputTokens}
	if !nonnegativeUsage(total) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Unknown turns are history, not part of this execution. Accept only a
	// monotonic baseline no newer than any of our observed cumulative snapshots.
	if s.historicalUsage != nil && !nonnegativeUsage(subtractUsage(total, s.historicalUsage)) {
		return
	}
	for _, record := range s.usageTurns {
		if record.total != nil && !nonnegativeUsage(subtractUsage(record.total, total)) {
			return
		}
	}
	s.historicalUsage = total
	if s.usageBaseline != nil {
		// Last only estimates the baseline when the first snapshot follows
		// several inferences. A historical total can correct that estimate in
		// either direction. An already observed historical baseline cannot
		// move backwards because the monotonic check above rejects replays.
		s.usageBaseline = cloneUsage(total)
		s.recomputeUsageLocked()
	}
}
