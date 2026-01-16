// resourcesync_debounce.go implements the debounce buffer for resource sync.
// The buffer collects changes over a configurable time window, deduplicates them
// (last-state-wins), and sends batched updates to the MCP endpoint.
package controller

import (
	"context"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DebounceBuffer collects resource changes and flushes them in batches
type DebounceBuffer struct {
	// changes holds pending changes keyed by resource ID
	changes map[string]*ResourceChange
	mu      sync.Mutex

	// window is the debounce time window
	window time.Duration

	// mcpClient sends batched changes to MCP
	mcpClient *MCPResourceSyncClient

	// changeQueue is the input channel for changes from informer handlers
	changeQueue <-chan *ResourceChange

	// metrics for observability
	totalUpserts   int64
	totalDeletes   int64
	totalFlushes   int64
	totalDropped   int64
	lastFlushTime  time.Time
	lastFlushCount int
	// lastError stores the most recent sync error
	lastError     string
	lastErrorTime time.Time
	metricsMu     sync.RWMutex
}

// DebounceBufferConfig holds configuration for creating a DebounceBuffer
type DebounceBufferConfig struct {
	Window      time.Duration
	MCPClient   *MCPResourceSyncClient
	ChangeQueue <-chan *ResourceChange
}

// NewDebounceBuffer creates a new debounce buffer
func NewDebounceBuffer(cfg DebounceBufferConfig) *DebounceBuffer {
	if cfg.Window <= 0 {
		cfg.Window = 10 * time.Second // Default 10-second window
	}

	return &DebounceBuffer{
		changes:     make(map[string]*ResourceChange),
		window:      cfg.Window,
		mcpClient:   cfg.MCPClient,
		changeQueue: cfg.ChangeQueue,
	}
}

// Run starts the debounce buffer processing loop
// It reads from the change queue and periodically flushes to MCP
func (b *DebounceBuffer) Run(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("debounce-buffer")
	logger.Info("Starting debounce buffer", "window", b.window)

	ticker := time.NewTicker(b.window)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Debounce buffer stopping, performing final flush")
			// Use a fresh context with timeout for final flush since the original
			// context is cancelled. This ensures pending changes (especially deletes)
			// are synced to MCP before shutdown.
			flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			b.flush(flushCtx)
			cancel()
			return

		case change, ok := <-b.changeQueue:
			if !ok {
				logger.Info("Change queue closed, stopping debounce buffer")
				// Use a fresh context with timeout since the original context may be cancelled
				flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				b.flush(flushCtx)
				cancel()
				return
			}
			b.record(change)

		case <-ticker.C:
			b.flush(ctx)
		}
	}
}

// record adds a change to the buffer
// Last-state-wins: the most recent event always takes precedence.
// This ensures that if a resource is deleted and recreated (with the same name),
// the new resource will be properly synced to MCP.
func (b *DebounceBuffer) record(change *ResourceChange) {
	if change == nil {
		return
	}

	logger := logf.Log.WithName("debounce-buffer")

	b.mu.Lock()
	defer b.mu.Unlock()

	// ID is the internal identifier for deduplication
	id := change.ID
	if id == "" {
		logger.V(1).Info("Dropping change with empty ID")
		b.incrementDropped()
		return
	}

	// Last-state-wins: always use the most recent event.
	// This handles the delete-then-recreate case correctly:
	// - Delete arrives first -> recorded
	// - Upsert arrives for new resource with same name -> replaces delete
	// - Result: new resource is synced to MCP
	b.changes[id] = change
	if change.Action == ActionDelete {
		logger.V(2).Info("Recorded delete", "id", id)
	} else {
		logger.V(2).Info("Recorded upsert", "id", id)
	}
}

// flush sends all pending changes to MCP
func (b *DebounceBuffer) flush(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("debounce-buffer")

	b.mu.Lock()
	if len(b.changes) == 0 {
		b.mu.Unlock()
		return
	}

	// Collect changes - store both ID (for requeue) and full change data
	var upserts []*ResourceData
	var deletes []*ResourceIdentifier
	// Keep track of changes for potential requeue
	changesToRequeue := make(map[string]*ResourceChange)

	for id, change := range b.changes {
		if change.Action == ActionDelete {
			if change.DeleteIdentifier != nil {
				deletes = append(deletes, change.DeleteIdentifier)
				changesToRequeue[id] = change
			}
		} else if change.Data != nil {
			upserts = append(upserts, change.Data)
			changesToRequeue[id] = change
		}
	}

	// Clear the buffer
	b.changes = make(map[string]*ResourceChange)
	b.mu.Unlock()

	// Skip if nothing to send
	if len(upserts) == 0 && len(deletes) == 0 {
		return
	}

	logger.Info("Flushing changes to MCP",
		"upserts", len(upserts),
		"deletes", len(deletes),
	)

	// Send to MCP
	if b.mcpClient == nil {
		logger.V(1).Info("MCP client not configured, skipping flush")
		return
	}

	resp, err := b.mcpClient.SyncResources(ctx, upserts, deletes)
	if err != nil {
		logger.Error(err, "Failed to sync resources to MCP",
			"upserts", len(upserts),
			"deletes", len(deletes),
		)
		// Record the error for status reporting
		b.recordError(err.Error())
		// Re-queue failed changes for next flush
		b.requeueChangesFromMap(changesToRequeue)
		return
	}

	// Update metrics
	b.updateMetrics(resp, len(upserts), len(deletes))

	if !resp.Success {
		errMsg := resp.GetErrorMessage()
		logger.Error(nil, "MCP sync returned error",
			"error", errMsg,
			"failures", resp.GetFailures(),
		)
		// Record the error for status reporting
		b.recordError(errMsg)
		// For partial failures, we don't re-queue - the next resync will catch them
	} else {
		// Clear error on success
		b.clearError()
	}
}

// requeueChangesFromMap adds failed changes back to the buffer for retry
func (b *DebounceBuffer) requeueChangesFromMap(changes map[string]*ResourceChange) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, change := range changes {
		if _, exists := b.changes[id]; !exists {
			b.changes[id] = change
		}
	}
}

// updateMetrics updates the buffer metrics after a flush
func (b *DebounceBuffer) updateMetrics(resp *SyncResponse, attemptedUpserts, attemptedDeletes int) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()

	b.totalFlushes++
	b.lastFlushTime = time.Now()
	b.lastFlushCount = attemptedUpserts + attemptedDeletes

	if resp != nil && resp.Success {
		upserted, deleted := resp.GetSuccessCounts()
		b.totalUpserts += int64(upserted)
		b.totalDeletes += int64(deleted)
	}
}

// incrementDropped increments the dropped changes counter
func (b *DebounceBuffer) incrementDropped() {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.totalDropped++
}

// recordError stores the last sync error
func (b *DebounceBuffer) recordError(errMsg string) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.lastError = errMsg
	b.lastErrorTime = time.Now()
}

// clearError clears the last sync error (called on successful sync)
func (b *DebounceBuffer) clearError() {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.lastError = ""
}

// GetMetrics returns current buffer metrics
func (b *DebounceBuffer) GetMetrics() DebounceBufferMetrics {
	b.metricsMu.RLock()
	defer b.metricsMu.RUnlock()

	b.mu.Lock()
	pendingCount := len(b.changes)
	b.mu.Unlock()

	return DebounceBufferMetrics{
		TotalUpserts:   b.totalUpserts,
		TotalDeletes:   b.totalDeletes,
		TotalFlushes:   b.totalFlushes,
		TotalDropped:   b.totalDropped,
		PendingChanges: pendingCount,
		LastFlushTime:  b.lastFlushTime,
		LastFlushCount: b.lastFlushCount,
		LastError:      b.lastError,
		LastErrorTime:  b.lastErrorTime,
	}
}

// DebounceBufferMetrics holds metrics about the debounce buffer
type DebounceBufferMetrics struct {
	TotalUpserts   int64
	TotalDeletes   int64
	TotalFlushes   int64
	TotalDropped   int64
	PendingChanges int
	LastFlushTime  time.Time
	LastFlushCount int
	// LastError contains the last sync error message (empty if last sync succeeded)
	LastError string
	// LastErrorTime is when the last error occurred
	LastErrorTime time.Time
}

// PendingCount returns the number of pending changes in the buffer
func (b *DebounceBuffer) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.changes)
}

// SetMCPClient sets the MCP client (useful for late initialization or testing)
func (b *DebounceBuffer) SetMCPClient(client *MCPResourceSyncClient) {
	b.mcpClient = client
}

// SetLastFlushTimeForTesting sets the lastFlushTime for testing purposes
func (b *DebounceBuffer) SetLastFlushTimeForTesting(t time.Time) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	b.lastFlushTime = t
}
