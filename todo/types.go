// Package todo defines confirmed, full ordered plan snapshots. A snapshot is
// not an approval request; an empty Items array explicitly clears the scope.
package todo

import "time"

// Status is the closed state of a confirmed item.
type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Completed  Status = "completed"
	Cancelled  Status = "cancelled"
)

// Source is the formal confirmation that produced a snapshot.
type Source string

const (
	ToolResult Source = "tool_result"
	PlanUpdate Source = "plan_update"
)

// Item is one confirmed task. SyntheticID explicitly distinguishes an ID
// synthesized from invocation coordinates from a provider's real task ID.
type Item struct {
	ID          string
	Content     string
	Status      Status
	SyntheticID bool
}

// Snapshot is the full ordered state for one run and scope. Revision starts at
// one in each run/scope and advances only on changed confirmations. OccurredAt
// is nonzero UTC; parent coordinates are independent from consumer Thread keys.
// Items has 0..128 unique IDs, preserving confirmation order. Content is
// 1..4096 UTF-8 bytes with LF/tab allowed but other controls rejected. IDs use
// at most 2048 UTF-8 bytes without controls. Invalid updates are atomic failures;
// a synthetic ID cannot be matched as a provider ID for a later update.
type Snapshot struct {
	Items            []Item
	Source           Source
	ScopeID          string
	ParentScopeID    string
	ParentToolCallID string
	Revision         uint64
	OccurredAt       time.Time
}
