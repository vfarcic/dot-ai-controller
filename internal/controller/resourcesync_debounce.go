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
	metricsMu      sync.RWMutex
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
			// Final flush before exit
			b.flush(ctx)
			return

		case change, ok := <-b.changeQueue:
			if !ok {
				logger.Info("Change queue closed, stopping debounce buffer")
				b.flush(ctx)
				return
			}
			b.record(change)

		case <-ticker.C:
			b.flush(ctx)
		}
	}
}

// record adds a change to the buffer
// Last-state-wins for upserts, deletes always preserved
func (b *DebounceBuffer) record(change *ResourceChange) {
	if change == nil {
		return
	}

	logger := logf.Log.WithName("debounce-buffer")

	b.mu.Lock()
	defer b.mu.Unlock()

	id := change.ID
	if id == "" && change.Data != nil {
		id = change.Data.ID
	}

	if id == "" {
		logger.V(1).Info("Dropping change with empty ID")
		b.incrementDropped()
		return
	}

	existing, exists := b.changes[id]

	if change.Action == ActionDelete {
		// Delete always wins and is always preserved
		b.changes[id] = change
		logger.V(2).Info("Recorded delete", "id", id)
	} else if !exists || existing.Action != ActionDelete {
		// Upsert: keep latest state (unless already marked for delete)
		b.changes[id] = change
		logger.V(2).Info("Recorded upsert", "id", id)
	} else {
		// Upsert received but already marked for delete - ignore
		logger.V(2).Info("Ignoring upsert for resource marked for delete", "id", id)
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

	// Collect changes
	var upserts []*ResourceData
	var deletes []string
	for id, change := range b.changes {
		if change.Action == ActionDelete {
			deletes = append(deletes, id)
		} else if change.Data != nil {
			upserts = append(upserts, change.Data)
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
		// Re-queue failed changes for next flush
		b.requeueChanges(upserts, deletes)
		return
	}

	// Update metrics
	b.updateMetrics(resp, len(upserts), len(deletes))

	if !resp.Success {
		logger.Error(nil, "MCP sync returned error",
			"error", resp.GetErrorMessage(),
			"failures", resp.GetFailures(),
		)
		// For partial failures, we don't re-queue - the next resync will catch them
	}
}

// requeueChanges adds failed changes back to the buffer for retry
func (b *DebounceBuffer) requeueChanges(upserts []*ResourceData, deletes []string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, data := range upserts {
		if _, exists := b.changes[data.ID]; !exists {
			b.changes[data.ID] = &ResourceChange{
				Action: ActionUpsert,
				Data:   data,
				ID:     data.ID,
			}
		}
	}

	for _, id := range deletes {
		if _, exists := b.changes[id]; !exists {
			b.changes[id] = &ResourceChange{
				Action: ActionDelete,
				ID:     id,
			}
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
