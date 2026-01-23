// Package shutdown provides graceful shutdown handling for the controller.
package shutdown

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ErrShuttingDown is returned by health checks when the controller is shutting down.
var ErrShuttingDown = errors.New("shutting down")

// Tracker manages graceful shutdown state.
// It tracks whether the controller has received a shutdown signal and should
// stop accepting new work while draining in-flight operations.
type Tracker struct {
	shuttingDown atomic.Bool
}

// NewTracker creates a new shutdown tracker.
func NewTracker() *Tracker {
	return &Tracker{}
}

// MarkShuttingDown sets the shutdown state to true.
// This should be called immediately when a shutdown signal is received.
func (t *Tracker) MarkShuttingDown() {
	t.shuttingDown.Store(true)
}

// IsShuttingDown returns true if the controller is in shutdown state.
func (t *Tracker) IsShuttingDown() bool {
	return t.shuttingDown.Load()
}

// HealthChecker implements healthz.Checker interface with shutdown awareness.
// When the tracker is in shutdown state, Check returns an error causing the
// readiness probe to fail. This signals Kubernetes to stop routing traffic
// to this pod while it drains in-flight operations.
type HealthChecker struct {
	tracker *Tracker
}

// NewHealthChecker creates a new shutdown-aware health checker.
func NewHealthChecker(tracker *Tracker) *HealthChecker {
	return &HealthChecker{tracker: tracker}
}

// Check implements healthz.Checker interface.
// Returns nil when healthy, ErrShuttingDown when in shutdown state.
func (h *HealthChecker) Check(_ *http.Request) error {
	if h.tracker.IsShuttingDown() {
		return ErrShuttingDown
	}
	return nil
}

// SetupSignalHandler creates a context that is cancelled when a shutdown signal
// is received. It also marks the tracker as shutting down immediately when the
// first signal is received, allowing health checks to fail before the context
// is cancelled and the manager starts draining.
//
// The returned context should be passed to the controller manager's Start method.
// On first SIGTERM/SIGINT: marks tracker as shutting down and cancels context.
// On second signal: forces immediate exit.
func SetupSignalHandler(tracker *Tracker) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	logger := log.FromContext(context.Background()).WithName("shutdown")

	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-c
		logger.Info("received shutdown signal, initiating graceful shutdown",
			"signal", sig.String(),
			"action", "marking pod not-ready and draining in-flight operations")
		tracker.MarkShuttingDown()
		logger.Info("shutdown state set, cancelling context to stop accepting new work")
		cancel()
		logger.Info("graceful shutdown in progress, waiting for in-flight operations to complete")

		sig = <-c
		logger.Info("received second signal, forcing immediate exit",
			"signal", sig.String(),
			"warning", "in-flight operations may be abandoned")
		os.Exit(1)
	}()

	return ctx
}
