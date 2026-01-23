package shutdown

import (
	"net/http"
	"testing"
)

func TestTracker_InitialState(t *testing.T) {
	tracker := NewTracker()

	if tracker.IsShuttingDown() {
		t.Error("expected new tracker to not be in shutdown state")
	}
}

func TestTracker_MarkShuttingDown(t *testing.T) {
	tracker := NewTracker()

	tracker.MarkShuttingDown()

	if !tracker.IsShuttingDown() {
		t.Error("expected tracker to be in shutdown state after MarkShuttingDown")
	}
}

func TestTracker_MarkShuttingDownIdempotent(t *testing.T) {
	tracker := NewTracker()

	// Calling multiple times should be safe
	tracker.MarkShuttingDown()
	tracker.MarkShuttingDown()
	tracker.MarkShuttingDown()

	if !tracker.IsShuttingDown() {
		t.Error("expected tracker to be in shutdown state")
	}
}

func TestHealthChecker_Healthy(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	err := checker.Check(&http.Request{})

	if err != nil {
		t.Errorf("expected no error when not shutting down, got: %v", err)
	}
}

func TestHealthChecker_ShuttingDown(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	tracker.MarkShuttingDown()
	err := checker.Check(&http.Request{})

	if err == nil {
		t.Error("expected error when shutting down")
	}
	if err != ErrShuttingDown {
		t.Errorf("expected ErrShuttingDown, got: %v", err)
	}
}

func TestHealthChecker_NilRequest(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// Check should work with nil request (interface allows it)
	err := checker.Check(nil)

	if err != nil {
		t.Errorf("expected no error with nil request, got: %v", err)
	}
}

func TestHealthChecker_TransitionToShutdown(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// First check should pass
	if err := checker.Check(nil); err != nil {
		t.Errorf("first check should pass: %v", err)
	}

	// Mark shutdown
	tracker.MarkShuttingDown()

	// Second check should fail
	if err := checker.Check(nil); err != ErrShuttingDown {
		t.Errorf("second check should return ErrShuttingDown, got: %v", err)
	}
}
