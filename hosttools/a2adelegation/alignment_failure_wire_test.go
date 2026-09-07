package a2adelegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func incomingFailure(value any, state clienta2a.TaskState) clienta2a.TaskStatus {
	return clienta2a.TaskStatus{State: state, Message: &clienta2a.Message{ID: "failure", Role: localRoleAgent, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "safe remote failure", Metadata: map[string]any{"agentadaptor.failure": value}}}}}
}
func TestAlignmentFailureWireFrozenFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/alignment/budget-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		ID, State, Code string
		Failure         any
		Limit           int64 `json:"limit_ms"`
	}
	var fixture struct {
		Accepted []item
		Rejected []item `json:"not_promoted"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range append(fixture.Accepted, fixture.Rejected...) {
		t.Run(tc.ID, func(t *testing.T) {
			state := clienta2a.TaskStateFailed
			if tc.State == "canceled" {
				state = clienta2a.TaskStateCanceled
			}
			status := incomingFailure(tc.Failure, state)
			c := &alignmentClient{send: func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error) {
				return clienta2a.Task{ID: "task", Status: status, Artifacts: []clienta2a.Artifact{{ID: "partial", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "partial artifact"}}}}}, nil
			}}
			d := alignmentDelegator(t, DelegationPolicy{}, c)
			out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", IncludeRemoteArtifacts: true})
			if err == nil || out.Error.Code != tc.Code || delegationErr(err).Code != tc.Code || len(out.RemoteArtifacts) != 1 {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if tc.Code == "remote_failed" || tc.Code == "remote_cancelled" {
				if out.Error.Metadata["failure_payload_invalid"] != true {
					t.Fatal("invalid promotion unreported")
				}
				return
			}
			if tc.Code == "active_execution_timeout" {
				if !errors.Is(err, adaptor.ErrActiveExecutionTimeout) || out.Error.Retryable {
					t.Fatal("active classification")
				}
				var typed *adaptor.ActiveExecutionTimeoutError
				got := errors.As(err, &typed)
				if tc.Limit == 0 && got {
					t.Fatal("unknown Limit invented")
				}
				if tc.Limit > 0 && (!got || typed.Limit != budgetDuration(tc.Limit)) {
					t.Fatalf("limit=%+v", typed)
				}
			}
		})
	}
}
func TestAlignmentFailureWireRejectsWholeConflictedObject(t *testing.T) {
	invalids := []any{nil, []any{}, "bad", map[string]any{}, map[string]any{"code": "secret"}, map[string]any{"code": "active_execution_timeout", "limit_ms": math.NaN()}, map[string]any{"code": "active_execution_timeout", "limit_ms": math.Inf(1)}}
	for i, v := range invalids {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if out, invalid := failureFromStatus(incomingFailure(v, clienta2a.TaskStateFailed)); out != nil || !invalid {
				t.Fatalf("promoted %+v", out)
			}
		})
	}
	first := map[string]any{"code": "active_execution_timeout", "limit_ms": int64(100)}
	status := incomingFailure(first, clienta2a.TaskStateFailed)
	status.Message.Parts = append(status.Message.Parts, status.Message.Parts[0])
	if out, bad := failureFromStatus(status); bad || out == nil {
		t.Fatal("identical controls rejected")
	}
	status.Message.Parts[1] = clienta2a.Part{Kind: clienta2a.PartText, Metadata: map[string]any{"agentadaptor.failure": map[string]any{"code": "approval_denied"}}}
	if out, bad := failureFromStatus(status); !bad || out != nil {
		t.Fatal("conflicting controls promoted")
	}
	for _, kind := range []clienta2a.PartKind{clienta2a.PartData, clienta2a.PartURL} {
		status = incomingFailure(first, clienta2a.TaskStateFailed)
		status.Message.Parts[0].Kind = kind
		if out, bad := failureFromStatus(status); out != nil || bad {
			t.Fatal("wrong Part treated as control")
		}
	}
	for _, value := range []any{json.Number("100"), int64(100), float64(100)} {
		out, bad := failureFromStatus(incomingFailure(map[string]any{"code": "active_execution_timeout", "limit_ms": value}, clienta2a.TaskStateFailed))
		if bad || out.Metadata["limit_ms"] != int64(100) {
			t.Fatal("valid mathematical domain rejected")
		}
	}
}
func TestAlignmentLocalReasonCauseAndPartialParity(t *testing.T) {
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonActiveExecutionTimeout, adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonCancelled, adaptor.ReasonDeadlineExceeded, adaptor.ReasonAgentError, adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure} {
		t.Run(string(reason), func(t *testing.T) {
			typed := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
			original := &adaptor.RunError{Reason: reason, Result: &adaptor.Result{Text: "partial assistant", Summary: "partial summary"}, Cause: errors.Join(typed, context.Canceled)}
			team, err := NewService(Config{Agents: []AgentRef{Local("member", alignmentRunner{err: original}, Policy{})}})
			if err != nil {
				t.Fatal(err)
			}
			defer team.Close()
			out, err := team.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member", IncludeRemoteArtifacts: true})
			if err == nil || delegationErr(err).Code != string(reason) || !errors.Is(err, original) || !errors.Is(err, context.Canceled) || out.Summary != "partial summary" {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			var re *adaptor.RunError
			if !errors.As(err, &re) || re != original {
				t.Fatal("root cause/partial lost")
			}
			if len(out.Messages) != 1 || out.Messages[0].Text != "partial assistant" || len(out.RemoteArtifacts) != 1 {
				t.Fatal("local partial layers lost")
			}
			// A handwritten equivalent peer payload verifies the independent receiver.
			state := clienta2a.TaskStateFailed
			if reason == adaptor.ReasonCancelled {
				state = clienta2a.TaskStateCanceled
			}
			control := map[string]any{"code": string(reason)}
			if reason == adaptor.ReasonActiveExecutionTimeout {
				control["limit_ms"] = int64(100)
			}
			promoted, bad := failureFromStatus(incomingFailure(control, state))
			if bad || promoted.Code != out.Error.Code || !reflect.DeepEqual(promoted.Metadata, out.Error.Metadata) {
				t.Fatalf("wire/local differ %+v %+v", promoted, out.Error)
			}
		})
	}
}
