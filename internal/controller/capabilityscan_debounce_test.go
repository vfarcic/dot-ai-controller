package controller

import (
	"context"
	"testing"
	"time"
)

func TestCapabilityScanBuffer_Record(t *testing.T) {
	tests := []struct {
		name          string
		changes       []*CRDChange
		wantScans     int
		wantDeletes   int
		wantScanIDs   []string
		wantDeleteIDs []string
	}{
		{
			name: "single scan event",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
			},
			wantScans:   1,
			wantDeletes: 0,
			wantScanIDs: []string{"Bucket.s3.aws.crossplane.io"},
		},
		{
			name: "single delete event",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: true},
			},
			wantScans:     0,
			wantDeletes:   1,
			wantDeleteIDs: []string{"Bucket.s3.aws.crossplane.io"},
		},
		{
			name: "multiple scan events deduplicated",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
			},
			wantScans:   1,
			wantDeletes: 0,
			wantScanIDs: []string{"Bucket.s3.aws.crossplane.io"},
		},
		{
			name: "multiple different resources",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
				{ResourceID: "RDSInstance.database.aws.crossplane.io", IsDelete: false},
				{ResourceID: "VPC.ec2.aws.crossplane.io", IsDelete: false},
			},
			wantScans:   3,
			wantDeletes: 0,
		},
		{
			name: "delete removes pending scan",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: true},
			},
			wantScans:     0,
			wantDeletes:   1,
			wantDeleteIDs: []string{"Bucket.s3.aws.crossplane.io"},
		},
		{
			name: "scan after delete is ignored",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: true},
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
			},
			wantScans:     0,
			wantDeletes:   1,
			wantDeleteIDs: []string{"Bucket.s3.aws.crossplane.io"},
		},
		{
			name: "mixed scans and deletes",
			changes: []*CRDChange{
				{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false},
				{ResourceID: "RDSInstance.database.aws.crossplane.io", IsDelete: true},
				{ResourceID: "VPC.ec2.aws.crossplane.io", IsDelete: false},
			},
			wantScans:   2,
			wantDeletes: 1,
		},
		{
			name:        "nil change ignored",
			changes:     []*CRDChange{nil},
			wantScans:   0,
			wantDeletes: 0,
		},
		{
			name: "empty resource ID ignored",
			changes: []*CRDChange{
				{ResourceID: "", IsDelete: false},
			},
			wantScans:   0,
			wantDeletes: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
				Window: 10 * time.Second,
			})

			for _, change := range tt.changes {
				buffer.record(change)
			}

			buffer.mu.Lock()
			gotScans := len(buffer.pendingScans)
			gotDeletes := len(buffer.pendingDeletes)
			buffer.mu.Unlock()

			if gotScans != tt.wantScans {
				t.Errorf("pendingScans count = %d, want %d", gotScans, tt.wantScans)
			}
			if gotDeletes != tt.wantDeletes {
				t.Errorf("pendingDeletes count = %d, want %d", gotDeletes, tt.wantDeletes)
			}

			// Verify specific IDs if provided
			if tt.wantScanIDs != nil {
				buffer.mu.Lock()
				for _, id := range tt.wantScanIDs {
					if _, exists := buffer.pendingScans[id]; !exists {
						t.Errorf("expected scan ID %s not found", id)
					}
				}
				buffer.mu.Unlock()
			}
			if tt.wantDeleteIDs != nil {
				buffer.mu.Lock()
				for _, id := range tt.wantDeleteIDs {
					if _, exists := buffer.pendingDeletes[id]; !exists {
						t.Errorf("expected delete ID %s not found", id)
					}
				}
				buffer.mu.Unlock()
			}
		})
	}
}

func TestCapabilityScanBuffer_Flush(t *testing.T) {
	t.Run("flush clears pending items", func(t *testing.T) {
		buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
			Window: 10 * time.Second,
			// No MCP client - flush will skip sending but still clear buffer
		})

		// Record some changes
		buffer.record(&CRDChange{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false})
		buffer.record(&CRDChange{ResourceID: "RDSInstance.database.aws.crossplane.io", IsDelete: false})

		// Verify items are pending
		if buffer.PendingCount() != 2 {
			t.Errorf("PendingCount before flush = %d, want 2", buffer.PendingCount())
		}

		// Flush (no MCP client, so will skip sending but clear buffer)
		ctx := context.Background()
		buffer.flush(ctx)

		// Items should be cleared
		if buffer.PendingCount() != 0 {
			t.Errorf("PendingCount after flush = %d, want 0", buffer.PendingCount())
		}
	})

	t.Run("flush calls onFlush callback", func(t *testing.T) {
		var callbackCalled bool
		var callbackScans, callbackDeletes int

		buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
			Window: 10 * time.Second,
			OnFlush: func(scans int, deletes int, err error) {
				callbackCalled = true
				callbackScans = scans
				callbackDeletes = deletes
			},
		})

		buffer.record(&CRDChange{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false})
		buffer.record(&CRDChange{ResourceID: "VPC.ec2.aws.crossplane.io", IsDelete: true})

		ctx := context.Background()
		buffer.flush(ctx)

		if !callbackCalled {
			t.Error("onFlush callback was not called")
		}
		if callbackScans != 1 {
			t.Errorf("callback scans = %d, want 1", callbackScans)
		}
		if callbackDeletes != 1 {
			t.Errorf("callback deletes = %d, want 1", callbackDeletes)
		}
	})

	t.Run("flush does nothing when buffer empty", func(t *testing.T) {
		var callbackCalled bool

		buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
			Window: 10 * time.Second,
			OnFlush: func(scans int, deletes int, err error) {
				callbackCalled = true
			},
		})

		ctx := context.Background()
		buffer.flush(ctx)

		if callbackCalled {
			t.Error("onFlush callback should not be called for empty buffer")
		}
	})
}

func TestCapabilityScanBuffer_Run(t *testing.T) {
	t.Run("processes changes from queue", func(t *testing.T) {
		buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
			Window: 50 * time.Millisecond, // Short window for testing
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Start the buffer in a goroutine
		go buffer.Run(ctx)

		// Send some changes
		buffer.ChangeQueue() <- &CRDChange{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false}
		buffer.ChangeQueue() <- &CRDChange{ResourceID: "RDSInstance.database.aws.crossplane.io", IsDelete: false}

		// Give some time for the changes to be recorded
		time.Sleep(20 * time.Millisecond)

		// Changes should be pending
		if buffer.PendingCount() != 2 {
			t.Errorf("PendingCount = %d, want 2", buffer.PendingCount())
		}

		// Wait for flush
		time.Sleep(60 * time.Millisecond)

		// Buffer should be empty after flush
		if buffer.PendingCount() != 0 {
			t.Errorf("PendingCount after flush = %d, want 0", buffer.PendingCount())
		}
	})

	t.Run("flushes on context cancellation", func(t *testing.T) {
		var flushed bool
		buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
			Window: 1 * time.Hour, // Long window - won't trigger naturally
			OnFlush: func(scans int, deletes int, err error) {
				flushed = true
			},
		})

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			buffer.Run(ctx)
			close(done)
		}()

		// Send a change
		buffer.ChangeQueue() <- &CRDChange{ResourceID: "Bucket.s3.aws.crossplane.io", IsDelete: false}
		time.Sleep(20 * time.Millisecond)

		// Cancel context - should trigger final flush
		cancel()

		// Wait for Run to complete
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("Run did not complete after context cancellation")
		}

		if !flushed {
			t.Error("final flush was not triggered on context cancellation")
		}
	})
}

func TestCapabilityScanBuffer_DefaultWindow(t *testing.T) {
	buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
		Window: 0, // Should default to 10s
	})

	if buffer.window != 10*time.Second {
		t.Errorf("default window = %v, want 10s", buffer.window)
	}
}
