package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDebounceBuffer_Record(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour, // Long window so we can inspect buffer state
		ChangeQueue: changeQueue,
	})

	// Test upsert
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})
	assert.Equal(t, 1, buffer.PendingCount())

	// Test upsert updates existing (last-state-wins)
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo-updated", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})
	assert.Equal(t, 1, buffer.PendingCount()) // Still 1, not 2

	// Test delete
	buffer.record(&ResourceChange{
		Action:           ActionDelete,
		ID:               "test:v1:Pod:bar",
		DeleteIdentifier: &ResourceIdentifier{Name: "bar", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})
	assert.Equal(t, 2, buffer.PendingCount())
}

func TestDebounceBuffer_DeleteOverwritesUpsert(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	// First upsert
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	// Then delete - should overwrite upsert
	buffer.record(&ResourceChange{
		Action:           ActionDelete,
		ID:               "test:v1:Pod:foo",
		DeleteIdentifier: &ResourceIdentifier{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	assert.Equal(t, 1, buffer.PendingCount())

	// Verify the change is a delete
	buffer.mu.Lock()
	change := buffer.changes["test:v1:Pod:foo"]
	buffer.mu.Unlock()
	assert.Equal(t, ActionDelete, change.Action)
}

func TestDebounceBuffer_UpsertIgnoredAfterDelete(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	// First delete
	buffer.record(&ResourceChange{
		Action:           ActionDelete,
		ID:               "test:v1:Pod:foo",
		DeleteIdentifier: &ResourceIdentifier{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	// Then upsert - should be ignored because delete is preserved
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	assert.Equal(t, 1, buffer.PendingCount())

	// Verify the change is still a delete
	buffer.mu.Lock()
	change := buffer.changes["test:v1:Pod:foo"]
	buffer.mu.Unlock()
	assert.Equal(t, ActionDelete, change.Action)
}

func TestDebounceBuffer_EmptyIDDropped(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	// Record with empty ID
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "",
		Data:   &ResourceData{Name: ""},
	})

	assert.Equal(t, 0, buffer.PendingCount())

	// Check metrics
	metrics := buffer.GetMetrics()
	assert.Equal(t, int64(1), metrics.TotalDropped)
}

func TestDebounceBuffer_NilChangeIgnored(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	buffer.record(nil)
	assert.Equal(t, 0, buffer.PendingCount())
}

func TestDebounceBuffer_FlushSendsToMCP(t *testing.T) {
	var receivedRequest SyncRequest
	var requestMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		defer requestMu.Unlock()
		json.NewDecoder(r.Body).Decode(&receivedRequest)

		resp := SyncResponse{
			Success: true,
			Data: &struct {
				Upserted int `json:"upserted"`
				Deleted  int `json:"deleted"`
			}{
				Upserted: len(receivedRequest.Upserts),
				Deleted:  len(receivedRequest.Deletes),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mcpClient := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL + "/api/v1/resources/sync",
		HTTPClient: server.Client(),
	})

	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		MCPClient:   mcpClient,
		ChangeQueue: changeQueue,
	})

	// Add some changes
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:bar",
		Data:   &ResourceData{Name: "bar", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})
	buffer.record(&ResourceChange{
		Action:           ActionDelete,
		ID:               "test:v1:Pod:old",
		DeleteIdentifier: &ResourceIdentifier{Name: "old", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	assert.Equal(t, 3, buffer.PendingCount())

	// Flush
	ctx := context.Background()
	buffer.flush(ctx)

	// Verify buffer is cleared
	assert.Equal(t, 0, buffer.PendingCount())

	// Verify request was sent correctly
	requestMu.Lock()
	defer requestMu.Unlock()
	assert.Len(t, receivedRequest.Upserts, 2)
	assert.Len(t, receivedRequest.Deletes, 1)
	assert.False(t, receivedRequest.IsResync)
}

func TestDebounceBuffer_FlushRequeuesOnError(t *testing.T) {
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	mcpClient := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL + "/api/v1/resources/sync",
		HTTPClient: server.Client(),
		MaxRetries: 0, // No retries for faster test
	})

	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		MCPClient:   mcpClient,
		ChangeQueue: changeQueue,
	})

	// Add a change
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	// Flush - should fail and requeue
	ctx := context.Background()
	buffer.flush(ctx)

	// Change should be requeued
	assert.Equal(t, 1, buffer.PendingCount())
}

func TestDebounceBuffer_RunProcessesChanges(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		resp := SyncResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mcpClient := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL + "/api/v1/resources/sync",
		HTTPClient: server.Client(),
	})

	changeQueue := make(chan *ResourceChange, 100)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      50 * time.Millisecond, // Short window for test
		MCPClient:   mcpClient,
		ChangeQueue: changeQueue,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start buffer
	go buffer.Run(ctx)

	// Send changes through the queue
	for i := 0; i < 5; i++ {
		changeQueue <- &ResourceChange{
			Action: ActionUpsert,
			ID:     "test:v1:Pod:foo",
			Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
		}
	}

	// Wait for flush
	time.Sleep(150 * time.Millisecond)

	// Should have flushed (at least once, possibly multiple due to timing)
	assert.True(t, atomic.LoadInt32(&requestCount) >= 1)
	assert.Equal(t, 0, buffer.PendingCount())
}

func TestDebounceBuffer_StopsOnContextCancel(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		buffer.Run(ctx)
		close(done)
	}()

	// Cancel context
	cancel()

	// Should exit quickly
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("buffer.Run did not exit after context cancel")
	}
}

func TestDebounceBuffer_StopsOnChannelClose(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		buffer.Run(ctx)
		close(done)
	}()

	// Close channel
	close(changeQueue)

	// Should exit quickly
	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("buffer.Run did not exit after channel close")
	}
}

func TestDebounceBuffer_GetMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SyncResponse{
			Success: true,
			Data: &struct {
				Upserted int `json:"upserted"`
				Deleted  int `json:"deleted"`
			}{
				Upserted: 2,
				Deleted:  1,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mcpClient := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL + "/api/v1/resources/sync",
		HTTPClient: server.Client(),
	})

	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		MCPClient:   mcpClient,
		ChangeQueue: changeQueue,
	})

	// Add and flush changes
	buffer.record(&ResourceChange{Action: ActionUpsert, ID: "a", Data: &ResourceData{Name: "a", Kind: "Pod", APIVersion: "v1"}})
	buffer.record(&ResourceChange{Action: ActionUpsert, ID: "b", Data: &ResourceData{Name: "b", Kind: "Pod", APIVersion: "v1"}})
	buffer.record(&ResourceChange{Action: ActionDelete, ID: "c", DeleteIdentifier: &ResourceIdentifier{Name: "c", Kind: "Pod", APIVersion: "v1"}})

	ctx := context.Background()
	buffer.flush(ctx)

	metrics := buffer.GetMetrics()
	assert.Equal(t, int64(2), metrics.TotalUpserts)
	assert.Equal(t, int64(1), metrics.TotalDeletes)
	assert.Equal(t, int64(1), metrics.TotalFlushes)
	assert.Equal(t, 0, metrics.PendingChanges)
	assert.Equal(t, 3, metrics.LastFlushCount)
	assert.False(t, metrics.LastFlushTime.IsZero())
}

func TestDebounceBuffer_DefaultWindow(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      0, // Should default to 10s
		ChangeQueue: changeQueue,
	})

	assert.Equal(t, 10*time.Second, buffer.window)
}

func TestDebounceBuffer_SetMCPClient(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		ChangeQueue: changeQueue,
	})

	assert.Nil(t, buffer.mcpClient)

	client := &MCPResourceSyncClient{}
	buffer.SetMCPClient(client)

	assert.Equal(t, client, buffer.mcpClient)
}

func TestDebounceBuffer_FlushWithNilMCPClient(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		MCPClient:   nil, // No client configured
		ChangeQueue: changeQueue,
	})

	// Add a change
	buffer.record(&ResourceChange{
		Action: ActionUpsert,
		ID:     "test:v1:Pod:foo",
		Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
	})

	require.Equal(t, 1, buffer.PendingCount())

	// Flush should not panic, just skip
	ctx := context.Background()
	buffer.flush(ctx)

	// Buffer should be cleared even without MCP client
	assert.Equal(t, 0, buffer.PendingCount())
}

func TestDebounceBuffer_ConcurrentRecords(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 100)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	// Concurrent writes
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buffer.record(&ResourceChange{
				Action: ActionUpsert,
				ID:     "test:v1:Pod:foo", // Same ID, last-state-wins
				Data:   &ResourceData{Name: "foo", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
			})
		}(i)
	}

	wg.Wait()

	// Should only have 1 entry (last-state-wins)
	assert.Equal(t, 1, buffer.PendingCount())
}

func TestDebounceBuffer_DeduplicationPerformance(t *testing.T) {
	changeQueue := make(chan *ResourceChange, 10)
	buffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      1 * time.Hour,
		ChangeQueue: changeQueue,
	})

	// Simulate rapid updates to the same resource
	start := time.Now()
	for i := 0; i < 10000; i++ {
		buffer.record(&ResourceChange{
			Action: ActionUpsert,
			ID:     "test:v1:Pod:frequently-updated",
			Data:   &ResourceData{Name: "frequently-updated", Kind: "Pod", APIVersion: "v1", Namespace: "test"},
		})
	}
	duration := time.Since(start)

	// Should complete quickly (deduplication keeps buffer small)
	assert.Less(t, duration, 1*time.Second)
	assert.Equal(t, 1, buffer.PendingCount())
}
