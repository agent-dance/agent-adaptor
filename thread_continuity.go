package adaptor

import (
	"strings"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

const threadHistoryBudget = 8*1024 - 256

func hasUnstoredThreadGuidance(hub *messageHub, state *threadRunState) bool {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.cap.PersistsDelivered {
		return false
	}
	for _, item := range hub.items {
		if item.out.State == MessageDelivered && item.out.Mechanism != "core.prompt" && item.out.Mechanism != "core.dedupe" && !state.guidance[item.out.ID] {
			return true
		}
	}
	return false
}

func boundedThreadTexts(texts []string, budget int, count int) []string {
	if len(texts) > count {
		texts = texts[len(texts)-count:]
	}
	var newest []string
	for index := len(texts) - 1; index >= 0 && budget > 0; index-- {
		text := texts[index]
		if len(text) > budget {
			text = text[len(text)-budget:]
			for len(text) > 0 && !utf8.RuneStart(text[0]) {
				text = text[1:]
			}
		}
		if text != "" {
			newest = append(newest, text)
			budget -= len(text) + 2
		}
	}
	for left, right := 0, len(newest)-1; left < right; left, right = left+1, right-1 {
		newest[left], newest[right] = newest[right], newest[left]
	}
	return newest
}

func (s engineStore) updateHistory(record *threadstore.Record) {
	if previous := s.state.record; previous != nil {
		record.EarlierGuidance = append([]string(nil), previous.EarlierGuidance...)
		record.RecentTurns = append([]string(nil), previous.RecentTurns...)
	}
	if s.messages == nil {
		return
	}
	hub := s.messages
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if s.state.guidance == nil {
		s.state.guidance = make(map[string]bool)
	}
	if !hub.cap.PersistsDelivered {
		for _, item := range hub.items {
			if item.out.State == MessageDelivered && item.out.Mechanism != "core.prompt" && item.out.Mechanism != "core.dedupe" && !s.state.guidance[item.out.ID] {
				record.EarlierGuidance = append(record.EarlierGuidance, item.framed)
				s.state.guidance[item.out.ID] = true
			}
		}
	}
	for _, turn := range hub.turns[s.state.turns:] {
		record.RecentTurns = append(record.RecentTurns, turn.Text)
	}
	s.state.turns = len(hub.turns)
	record.EarlierGuidance = boundedThreadTexts(record.EarlierGuidance, threadHistoryBudget/2, 1000)
	record.RecentTurns = boundedThreadTexts(record.RecentTurns, threadHistoryBudget-threadHistoryBudget/2, 3)
}

func threadContinuityPreamble(record *threadstore.Record, fresh bool) string {
	if record == nil {
		return ""
	}
	var sections []string
	if len(record.EarlierGuidance) > 0 {
		sections = append(sections, "Earlier guidance (retain its author and authority; do not acknowledge separately):\n"+strings.Join(boundedThreadTexts(record.EarlierGuidance, threadHistoryBudget/2, 1000), "\n\n"))
	}
	if fresh && len(record.RecentTurns) > 0 {
		sections = append(sections, "Recent assistant turns before this fresh session:\n"+strings.Join(boundedThreadTexts(record.RecentTurns, threadHistoryBudget-threadHistoryBudget/2, 3), "\n\n"))
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n") + "\n\n"
}
