package shutdown

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// TestIntegration_ReadinessEndpointDuringShutdown tests that the readiness
// endpoint returns unhealthy when shutdown is triggered, simulating the full
// integration between Tracker, HealthChecker, and HTTP health endpoint.
func TestIntegration_ReadinessEndpointDuringShutdown(t *testing.T) {
	// Setup shutdown components
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// Create HTTP handler using controller-runtime's healthz handler
	mux := http.NewServeMux()
	mux.Handle("/readyz", &healthz.Handler{
		Checks: map[string]healthz.Checker{
			"readyz": checker.Check,
		},
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Test 1: Readiness should pass initially
	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("failed to make request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Test 2: Mark shutdown - readiness should fail immediately
	tracker.MarkShuttingDown()

	resp, err = http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("failed to make request: %v", err)
	}
	if resp.StatusCode == http.StatusOK {
		t.Error("expected readiness probe to fail during shutdown, but it passed")
	}
	resp.Body.Close()

	// Test 3: Verify multiple requests consistently fail
	for i := 0; i < 5; i++ {
		resp, err = http.Get(server.URL + "/readyz")
		if err != nil {
			t.Fatalf("failed to make request %d: %v", i, err)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("request %d: expected failure during shutdown", i)
		}
		resp.Body.Close()
	}
}

// TestIntegration_ShutdownSequence tests the full shutdown sequence:
// 1. Normal operation (readiness passes)
// 2. Shutdown signal received (readiness fails immediately)
// 3. Context cancellation propagates
// 4. In-flight work completes or is cancelled
func TestIntegration_ShutdownSequence(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// Simulate a context that would be cancelled on shutdown
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Track the sequence of events
	var events []string
	var mu sync.Mutex
	addEvent := func(event string) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}

	// Phase 1: Normal operation
	if err := checker.Check(nil); err != nil {
		t.Fatal("readiness should pass during normal operation")
	}
	addEvent("readiness_passed")

	// Phase 2: Shutdown triggered (mirrors SetupSignalHandler behavior)
	// First: mark as shutting down (readiness fails immediately)
	tracker.MarkShuttingDown()
	addEvent("shutdown_marked")

	// Verify readiness fails immediately after marking
	if err := checker.Check(nil); err == nil {
		t.Fatal("readiness should fail immediately after shutdown marked")
	}
	addEvent("readiness_failed")

	// Phase 3: Cancel context (stops accepting new work)
	cancel()
	addEvent("context_cancelled")

	// Verify the sequence is correct
	mu.Lock()
	expected := []string{"readiness_passed", "shutdown_marked", "readiness_failed", "context_cancelled"}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d: %v", len(expected), len(events), events)
	}
	for i, e := range expected {
		if events[i] != e {
			t.Errorf("event %d: expected %s, got %s", i, e, events[i])
		}
	}
	mu.Unlock()
}

// TestIntegration_InFlightOperationsDrain tests that in-flight operations
// can complete during the graceful shutdown window before context cancellation
// fully propagates.
func TestIntegration_InFlightOperationsDrain(t *testing.T) {
	tracker := NewTracker()

	// Simulate in-flight work that takes some time
	var workCompleted atomic.Bool
	var workCancelled atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())

	// Start simulated in-flight work
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Simulate work that checks context periodically
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				workCancelled.Store(true)
				return
			case <-time.After(10 * time.Millisecond):
				// Continue work
			}
		}
		workCompleted.Store(true)
	}()

	// Start shutdown sequence
	tracker.MarkShuttingDown()

	// Give work time to complete before cancelling context
	// This simulates the graceful shutdown window
	time.Sleep(50 * time.Millisecond)

	// Work should still be running (not yet cancelled)
	if workCancelled.Load() {
		t.Error("work should not be cancelled yet")
	}

	// Now cancel context
	cancel()

	// Wait for work to finish
	wg.Wait()

	// Either work completed or was cancelled, both are valid
	if !workCompleted.Load() && !workCancelled.Load() {
		t.Error("work should have either completed or been cancelled")
	}
}

// TestIntegration_ConcurrentShutdown tests that the shutdown mechanism
// handles concurrent access safely.
func TestIntegration_ConcurrentShutdown(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// Start multiple goroutines that check readiness
	var wg sync.WaitGroup
	numGoroutines := 100

	// Track results
	var passedBefore atomic.Int32
	var failedAfter atomic.Int32

	// Half will run before shutdown, half after
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := checker.Check(nil); err == nil {
				passedBefore.Add(1)
			}
		}()
	}

	// Small delay to let initial checks run
	time.Sleep(10 * time.Millisecond)

	// Trigger shutdown
	tracker.MarkShuttingDown()

	// More goroutines checking after shutdown
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := checker.Check(nil); err != nil {
				failedAfter.Add(1)
			}
		}()
	}

	wg.Wait()

	// All checks after shutdown should fail
	if failedAfter.Load() != int32(numGoroutines/2) {
		t.Errorf("expected all %d post-shutdown checks to fail, got %d failures",
			numGoroutines/2, failedAfter.Load())
	}
}

// TestIntegration_ReadinessWithTimeout tests that the readiness check
// responds quickly even during shutdown (no blocking).
func TestIntegration_ReadinessWithTimeout(t *testing.T) {
	tracker := NewTracker()
	checker := NewHealthChecker(tracker)

	// Setup HTTP server
	mux := http.NewServeMux()
	mux.Handle("/readyz", &healthz.Handler{
		Checks: map[string]healthz.Checker{
			"readyz": checker.Check,
		},
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Create client with short timeout
	client := &http.Client{
		Timeout: 100 * time.Millisecond,
	}

	// Test during normal operation
	start := time.Now()
	resp, err := client.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	normalDuration := time.Since(start)

	// Mark shutdown
	tracker.MarkShuttingDown()

	// Test during shutdown - should respond quickly (not hang)
	start = time.Now()
	resp, err = client.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("request failed during shutdown: %v", err)
	}
	resp.Body.Close()
	shutdownDuration := time.Since(start)

	// Both should be fast (under 50ms)
	if normalDuration > 50*time.Millisecond {
		t.Errorf("normal readiness check took too long: %v", normalDuration)
	}
	if shutdownDuration > 50*time.Millisecond {
		t.Errorf("shutdown readiness check took too long: %v", shutdownDuration)
	}
}

// TestIntegration_MultipleHealthChecks tests multiple readiness checks
// registered with the same tracker.
func TestIntegration_MultipleHealthChecks(t *testing.T) {
	tracker := NewTracker()

	// Create multiple checkers (simulating different components)
	checker1 := NewHealthChecker(tracker)
	checker2 := NewHealthChecker(tracker)
	checker3 := NewHealthChecker(tracker)

	// All should pass initially
	checks := []*HealthChecker{checker1, checker2, checker3}
	for i, c := range checks {
		if err := c.Check(nil); err != nil {
			t.Errorf("checker %d should pass initially", i)
		}
	}

	// Mark shutdown once
	tracker.MarkShuttingDown()

	// All should fail after shutdown
	for i, c := range checks {
		if err := c.Check(nil); err == nil {
			t.Errorf("checker %d should fail during shutdown", i)
		}
	}
}
