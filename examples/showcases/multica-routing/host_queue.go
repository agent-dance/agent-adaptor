package main

import (
	"fmt"
	"strings"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

// hostMessage is a validated human comment awaiting a host routing decision.
// Target selects a roster entry; it never changes the comment's author.
type hostMessage struct {
	Thread   string         `json:"thread"`
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Target   string         `json:"target"`
	Author   adaptor.Author `json:"author"`
	Priority bool           `json:"priority"`
	Kind     string         `json:"kind"`
}

type hostThreadQueue struct {
	priority []hostMessage
	ordinary []hostMessage
	seen     map[string]hostMessage
}

// hostQueue contains only comments which the host has not dispatched to the
// SDK. Withdrawal cannot recall an input already accepted by Stream.Send.
type hostQueue struct {
	mu      sync.Mutex
	roster  map[string]struct{}
	threads map[string]*hostThreadQueue
}

func newHostQueue(roster []string) *hostQueue {
	q := &hostQueue{
		roster:  map[string]struct{}{"leader": {}},
		threads: make(map[string]*hostThreadQueue),
	}
	for _, member := range roster {
		if member != "" {
			q.roster[member] = struct{}{}
		}
	}
	return q
}

// Admit validates routing and authority before changing the queue. An identical
// duplicate is a no-op; reusing an ID for different content is an error. Empty
// Kind and Target select comment and leader respectively.
func (q *hostQueue) Admit(msg hostMessage) (bool, error) {
	if strings.TrimSpace(msg.Thread) == "" || strings.TrimSpace(msg.ID) == "" || strings.TrimSpace(msg.Text) == "" {
		return false, fmt.Errorf("comment requires a thread, ID and text")
	}
	if msg.Kind == "" {
		msg.Kind = "comment"
	}
	if msg.Kind != "comment" {
		return false, fmt.Errorf("unsupported comment kind %q", msg.Kind)
	}
	if msg.Author.Kind != adaptor.AuthorHuman {
		return false, fmt.Errorf("host comments require a human author")
	}
	if msg.Target == "" {
		msg.Target = "leader"
	}
	if _, exists := q.roster[msg.Target]; !exists {
		return false, fmt.Errorf("unknown comment target %q", msg.Target)
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	thread := q.threads[msg.Thread]
	if thread == nil {
		thread = &hostThreadQueue{seen: make(map[string]hostMessage)}
		q.threads[msg.Thread] = thread
	}
	if previous, exists := thread.seen[msg.ID]; exists {
		if previous == msg {
			return false, nil
		}
		return false, fmt.Errorf("comment ID %q already has different content in this thread", msg.ID)
	}
	thread.seen[msg.ID] = msg
	if msg.Priority {
		thread.priority = append(thread.priority, msg)
	} else {
		thread.ordinary = append(thread.ordinary, msg)
	}
	return true, nil
}

// Withdraw removes only an undispatched comment from the given thread. Its ID
// stays recorded for this queue's lifetime, so retries cannot resurrect it.
func (q *hostQueue) Withdraw(threadKey, id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	thread := q.threads[threadKey]
	if thread == nil {
		return false
	}
	var removed bool
	thread.priority, removed = removeHostMessage(thread.priority, id)
	if removed {
		return true
	}
	thread.ordinary, removed = removeHostMessage(thread.ordinary, id)
	return removed
}

func removeHostMessage(messages []hostMessage, id string) ([]hostMessage, bool) {
	for i, msg := range messages {
		if msg.ID == id {
			copy(messages[i:], messages[i+1:])
			messages[len(messages)-1] = hostMessage{}
			return messages[:len(messages)-1], true
		}
	}
	return messages, false
}

// Pop chooses the oldest priority comment first, then the oldest ordinary
// comment. It never draws work from a different thread or changes its target.
func (q *hostQueue) Pop(threadKey string) (hostMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	thread := q.threads[threadKey]
	if thread == nil {
		return hostMessage{}, false
	}
	queue := &thread.priority
	if len(*queue) == 0 {
		queue = &thread.ordinary
	}
	if len(*queue) == 0 {
		return hostMessage{}, false
	}
	msg := (*queue)[0]
	(*queue)[0] = hostMessage{}
	*queue = (*queue)[1:]
	return msg, true
}

func encodeThreadKey(workspaceID, discussionID string) string {
	return team.EncodeKey("workspace", workspaceID, "discussion", discussionID)
}
