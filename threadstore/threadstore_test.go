package threadstore

import (
	"errors"
	"strings"
	"testing"
)

func TestBusyErrorContract(t *testing.T) {
	err := error(&BusyError{Target: "key:thread:tenant-1/issue-1"})
	if !errors.Is(err, ErrBusy) {
		t.Fatal("BusyError must unwrap to ErrBusy")
	}
	if !strings.Contains(err.Error(), "tenant-1/issue-1") {
		t.Fatalf("Error() = %q, want the target included", err.Error())
	}
	if (&BusyError{}).Error() != ErrBusy.Error() {
		t.Fatalf("empty-target Error() = %q, want the bare sentinel text", (&BusyError{}).Error())
	}
}

func TestLeaseLostErrorContract(t *testing.T) {
	err := error(&LeaseLostError{Target: "sess-42"})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatal("LeaseLostError must unwrap to ErrLeaseLost")
	}
	if !strings.Contains(err.Error(), "sess-42") {
		t.Fatalf("Error() = %q, want the target included", err.Error())
	}
	if (&LeaseLostError{}).Error() != ErrLeaseLost.Error() {
		t.Fatalf("empty-target Error() = %q, want the bare sentinel text", (&LeaseLostError{}).Error())
	}
}

func TestAlreadyExistsErrorContract(t *testing.T) {
	typed := &AlreadyExistsError{Key: "tenant\x00一/issue"}
	err := error(typed)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatal("AlreadyExistsError must unwrap to ErrAlreadyExists")
	}
	if got := err.Error(); got != "threadstore: thread already exists: tenant\x00一/issue" {
		t.Fatalf("Error() = %q", got)
	}
	if (&AlreadyExistsError{}).Error() != ErrAlreadyExists.Error() {
		t.Fatalf("empty-key Error() = %q, want bare sentinel", (&AlreadyExistsError{}).Error())
	}
	if !typed.ThreadAlreadyExists() {
		t.Fatal("AlreadyExistsError must expose the coordinator conflict marker")
	}
}
