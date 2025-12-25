// capabilityscan_debounce.go implements the debounce buffer for capability scanning.
// The buffer collects CRD events over a configurable time window, then sends them
// to MCP as a batched request (comma-separated resourceList for scans).
package controller

import (
	"context"
	"strings"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// CRDChange represents a CRD create/update or delete event
type CRDChange struct {
	// ResourceID is the capability ID (Kind.group or Kind for core resources)
	ResourceID string
	// IsDelete indicates if this is a delete event
	IsDelete bool
}

// CapabilityScanBuffer collects CRD changes and flushes them in batches
type CapabilityScanBuffer struct {
	// pendingScans holds resource IDs to scan (create/update events)
	pendingScans map[string]struct{}
	// pendingDeletes holds resource IDs to delete
	pendingDeletes map[string]struct{}
	mu             sync.Mutex

	// window is the debounce time window
	window time.Duration

	// mcpClient sends batched changes to MCP
	mcpClient *MCPCapabilityScanClient

	// changeQueue is the input channel for CRD changes
	changeQueue chan *CRDChange

	// onFlush is called after each flush (for status updates)
	onFlush func(scans int, deletes int, err error)
}

// CapabilityScanBufferConfig holds configuration for creating a CapabilityScanBuffer
type CapabilityScanBufferConfig struct {
	Window    time.Duration
	MCPClient *MCPCapabilityScanClient
	OnFlush   func(scans int, deletes int, err error)
}

// NewCapabilityScanBuffer creates a new capability scan debounce buffer
func NewCapabilityScanBuffer(cfg CapabilityScanBufferConfig) *CapabilityScanBuffer {
	if cfg.Window <= 0 {
		cfg.Window = 10 * time.Second // Default 10-second window
	}

	return &CapabilityScanBuffer{
		pendingScans:   make(map[string]struct{}),
		pendingDeletes: make(map[string]struct{}),
		window:         cfg.Window,
		mcpClient:      cfg.MCPClient,
		changeQueue:    make(chan *CRDChange, 100), // Buffer up to 100 events
		onFlush:        cfg.OnFlush,
	}
}

// ChangeQueue returns the channel to send CRD changes to
func (b *CapabilityScanBuffer) ChangeQueue() chan<- *CRDChange {
	return b.changeQueue
}

// Run starts the debounce buffer processing loop
// It reads from the change queue and periodically flushes to MCP
func (b *CapabilityScanBuffer) Run(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("capabilityscan-debounce")
	logger.Info("Starting capability scan debounce buffer", "window", b.window)

	ticker := time.NewTicker(b.window)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Debounce buffer stopping, performing final flush")
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
func (b *CapabilityScanBuffer) record(change *CRDChange) {
	if change == nil || change.ResourceID == "" {
		return
	}

	logger := logf.Log.WithName("capabilityscan-debounce")

	b.mu.Lock()
	defer b.mu.Unlock()

	if change.IsDelete {
		// Delete: remove from pending scans (no point scanning then deleting)
		// and add to pending deletes
		delete(b.pendingScans, change.ResourceID)
		b.pendingDeletes[change.ResourceID] = struct{}{}
		logger.V(1).Info("Recorded delete", "resourceID", change.ResourceID)
	} else {
		// Scan: add to pending scans (unless already marked for delete)
		if _, markedForDelete := b.pendingDeletes[change.ResourceID]; !markedForDelete {
			b.pendingScans[change.ResourceID] = struct{}{}
			logger.V(1).Info("Recorded scan", "resourceID", change.ResourceID)
		}
	}
}

// flush sends all pending changes to MCP
func (b *CapabilityScanBuffer) flush(ctx context.Context) {
	logger := logf.FromContext(ctx).WithName("capabilityscan-debounce")

	b.mu.Lock()
	if len(b.pendingScans) == 0 && len(b.pendingDeletes) == 0 {
		b.mu.Unlock()
		return
	}

	// Collect pending changes
	scans := make([]string, 0, len(b.pendingScans))
	for id := range b.pendingScans {
		scans = append(scans, id)
	}
	deletes := make([]string, 0, len(b.pendingDeletes))
	for id := range b.pendingDeletes {
		deletes = append(deletes, id)
	}

	// Clear the buffers
	b.pendingScans = make(map[string]struct{})
	b.pendingDeletes = make(map[string]struct{})
	b.mu.Unlock()

	logger.Info("Flushing CRD changes to MCP",
		"scans", len(scans),
		"deletes", len(deletes),
	)

	var flushErr error

	// Only send to MCP if client is configured
	if b.mcpClient != nil {
		// Send batched scan request
		if len(scans) > 0 {
			resourceList := strings.Join(scans, ",")
			if err := b.mcpClient.TriggerScan(ctx, resourceList); err != nil {
				logger.Error(err, "❌ Failed to trigger batched scan", "resources", len(scans))
				flushErr = err
			} else {
				logger.Info("✅ Triggered batched scan", "resources", len(scans))
			}
		}

		// Send individual delete requests
		// (MCP delete API only supports single ID per request)
		for _, id := range deletes {
			if err := b.mcpClient.DeleteCapability(ctx, id); err != nil {
				logger.Error(err, "❌ Failed to delete capability", "id", id)
				if flushErr == nil {
					flushErr = err
				}
			} else {
				logger.Info("✅ Deleted capability", "id", id)
			}
		}
	} else {
		logger.V(1).Info("MCP client not configured, skipping MCP calls")
	}

	// Call onFlush callback for status updates
	if b.onFlush != nil {
		b.onFlush(len(scans), len(deletes), flushErr)
	}
}

// PendingCount returns the total number of pending changes in the buffer
func (b *CapabilityScanBuffer) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pendingScans) + len(b.pendingDeletes)
}
