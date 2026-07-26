package agentadaptor

import (
	"context"
	"time"
)

// decisionRequestBase is the common field set embedded in the per-Kind
// typed request structs.
type decisionRequestBase struct {
	RequestID    string
	RunID        string
	ThreadID     string
	Source       string
	ToolCallID   string
	CreatedAt    time.Time
	Deadline     time.Time
	RetryAttempt int
}

// PermissionRequest is the Permission class typed request (binary-approval
// tool / command gate).
type PermissionRequest struct {
	decisionRequestBase
	Tool   string
	Prompt string
	Args   map[string]any
}

// PlanReviewRequest is the PlanReview class typed request (plan-mode
// approval).
type PlanReviewRequest struct {
	decisionRequestBase
	Prompt string
	Plan   string
	Extra  map[string]any
}

// QuestionRequest is the Question class typed request (structured
// clarification).
type QuestionRequest struct {
	decisionRequestBase
	Prompt  string
	Schema  map[string]any
	Choices []DecisionChoice
}

// ApprovalResult narrows Permission / PlanReview handler responses to the
// legal binary-approval values.
type ApprovalResult string

const (
	// ApprovalApproved is returned by Permission/PlanReview handlers to approve.
	ApprovalApproved ApprovalResult = "approved"
	// ApprovalRejected is returned by Permission/PlanReview handlers to reject.
	ApprovalRejected ApprovalResult = "rejected"
)

// QuestionResult narrows Question handler responses to the legal
// structured-question values.
type QuestionResult string

const (
	// QuestionAnswered means the handler supplied an Answer/Choice payload.
	QuestionAnswered QuestionResult = "answered"
	// QuestionRejected means the handler declined to answer the question.
	QuestionRejected QuestionResult = "rejected"
)

// PermissionResponse is the typed Response returned by PermissionHandler.
type PermissionResponse struct {
	RequestID string
	Result    ApprovalResult
	Text      string
}

// PlanReviewResponse is the typed Response returned by PlanReviewHandler.
type PlanReviewResponse struct {
	RequestID string
	Result    ApprovalResult
	Text      string
}

// QuestionResponse is the typed Response returned by QuestionHandler.
type QuestionResponse struct {
	RequestID string
	Result    QuestionResult
	Choice    string
	Answer    map[string]any
	Text      string
}

// PermissionHandler processes Permission-class decisions synchronously.
type PermissionHandler func(ctx context.Context, req PermissionRequest) (PermissionResponse, error)

// PlanReviewHandler processes PlanReview-class decisions synchronously.
type PlanReviewHandler func(ctx context.Context, req PlanReviewRequest) (PlanReviewResponse, error)

// QuestionHandler processes Question-class decisions synchronously.
type QuestionHandler func(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
