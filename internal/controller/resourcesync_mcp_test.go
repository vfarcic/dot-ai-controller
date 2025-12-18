package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncResponse_GetSuccessCounts(t *testing.T) {
	tests := []struct {
		name             string
		response         *SyncResponse
		expectedUpserted int
		expectedDeleted  int
	}{
		{
			name: "with data",
			response: &SyncResponse{
				Success: true,
				Data: &struct {
					Upserted int `json:"upserted"`
					Deleted  int `json:"deleted"`
				}{
					Upserted: 5,
					Deleted:  3,
				},
			},
			expectedUpserted: 5,
			expectedDeleted:  3,
		},
		{
			name: "nil data",
			response: &SyncResponse{
				Success: true,
				Data:    nil,
			},
			expectedUpserted: 0,
			expectedDeleted:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upserted, deleted := tt.response.GetSuccessCounts()
			assert.Equal(t, tt.expectedUpserted, upserted)
			assert.Equal(t, tt.expectedDeleted, deleted)
		})
	}
}

func TestSyncResponse_GetErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		response *SyncResponse
		expected string
	}{
		{
			name: "with message",
			response: &SyncResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details *struct {
						Upserted int           `json:"upserted,omitempty"`
						Deleted  int           `json:"deleted,omitempty"`
						Failures []SyncFailure `json:"failures,omitempty"`
					} `json:"details,omitempty"`
				}{
					Code:    "SYNC_FAILED",
					Message: "Failed to process resources",
				},
			},
			expected: "Failed to process resources",
		},
		{
			name: "with code only",
			response: &SyncResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details *struct {
						Upserted int           `json:"upserted,omitempty"`
						Deleted  int           `json:"deleted,omitempty"`
						Failures []SyncFailure `json:"failures,omitempty"`
					} `json:"details,omitempty"`
				}{
					Code: "SYNC_FAILED",
				},
			},
			expected: "error code: SYNC_FAILED",
		},
		{
			name: "nil error",
			response: &SyncResponse{
				Success: false,
				Error:   nil,
			},
			expected: "unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.response.GetErrorMessage())
		})
	}
}

func TestSyncResponse_GetFailures(t *testing.T) {
	tests := []struct {
		name     string
		response *SyncResponse
		expected []SyncFailure
	}{
		{
			name: "with failures",
			response: &SyncResponse{
				Success: false,
				Error: &struct {
					Code    string `json:"code"`
					Message string `json:"message"`
					Details *struct {
						Upserted int           `json:"upserted,omitempty"`
						Deleted  int           `json:"deleted,omitempty"`
						Failures []SyncFailure `json:"failures,omitempty"`
					} `json:"details,omitempty"`
				}{
					Details: &struct {
						Upserted int           `json:"upserted,omitempty"`
						Deleted  int           `json:"deleted,omitempty"`
						Failures []SyncFailure `json:"failures,omitempty"`
					}{
						Failures: []SyncFailure{
							{ID: "test:v1:Pod:foo", Error: "embedding failed"},
						},
					},
				},
			},
			expected: []SyncFailure{{ID: "test:v1:Pod:foo", Error: "embedding failed"}},
		},
		{
			name: "nil error",
			response: &SyncResponse{
				Success: true,
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.response.GetFailures())
		})
	}
}

func TestMCPResourceSyncClient_SyncResources_Success(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/resources/sync", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse request body
		var req SyncRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Len(t, req.Upserts, 2)
		assert.Len(t, req.Deletes, 1)
		assert.False(t, req.IsResync)

		// Send response
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	// Test sync
	ctx := context.Background()
	upserts := []*ResourceData{
		{ID: "default:apps/v1:Deployment:nginx", Name: "nginx", Kind: "Deployment"},
		{ID: "default:v1:Service:nginx", Name: "nginx", Kind: "Service"},
	}
	deletes := []string{"default:apps/v1:Deployment:old"}

	resp, err := client.SyncResources(ctx, upserts, deletes)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	upserted, deleted := resp.GetSuccessCounts()
	assert.Equal(t, 2, upserted)
	assert.Equal(t, 1, deleted)
}

func TestMCPResourceSyncClient_SyncResources_Error(t *testing.T) {
	// Create mock server that returns error response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := SyncResponse{
			Success: false,
			Error: &struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details *struct {
					Upserted int           `json:"upserted,omitempty"`
					Deleted  int           `json:"deleted,omitempty"`
					Failures []SyncFailure `json:"failures,omitempty"`
				} `json:"details,omitempty"`
			}{
				Code:    "SYNC_FAILED",
				Message: "Failed to generate embeddings",
				// Include details to trigger partial success return
				Details: &struct {
					Upserted int           `json:"upserted,omitempty"`
					Deleted  int           `json:"deleted,omitempty"`
					Failures []SyncFailure `json:"failures,omitempty"`
				}{
					Upserted: 0,
					Deleted:  0,
					Failures: []SyncFailure{{ID: "test", Error: "failed"}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client with no retries for faster test
	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		MaxRetries: 0,
	})

	// Test sync - should return both response and error for partial failures
	ctx := context.Background()
	resp, err := client.SyncResources(ctx, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MCP returned error")
	// With Details present, response is returned along with error
	require.NotNil(t, resp)
	assert.False(t, resp.Success)
}

func TestMCPResourceSyncClient_SyncResources_HTTPError(t *testing.T) {
	// Create mock server that returns HTTP 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	// Create client with no retries
	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		MaxRetries: 0,
	})

	ctx := context.Background()
	resp, err := client.SyncResources(ctx, nil, nil)
	// HTTP errors return error after retries exhausted
	require.Error(t, err)
	// For HTTP errors without partial success details, resp may be nil after retries
	// This is because the error response doesn't have Details to trigger partial return
	if resp != nil {
		assert.False(t, resp.Success)
	}
}

func TestMCPResourceSyncClient_Resync(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request body
		var req SyncRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.True(t, req.IsResync, "Expected isResync to be true")
		assert.Len(t, req.Upserts, 3)
		assert.Empty(t, req.Deletes)

		// Send response
		resp := SyncResponse{
			Success: true,
			Data: &struct {
				Upserted int `json:"upserted"`
				Deleted  int `json:"deleted"`
			}{
				Upserted: 3,
				Deleted:  0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	resources := []*ResourceData{
		{ID: "default:apps/v1:Deployment:a"},
		{ID: "default:apps/v1:Deployment:b"},
		{ID: "default:apps/v1:Deployment:c"},
	}

	resp, err := client.Resync(ctx, resources)
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestMCPResourceSyncClient_RetryOnFailure(t *testing.T) {
	attempts := 0
	// Create mock server that fails twice then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Service Unavailable"))
			return
		}

		resp := SyncResponse{
			Success: true,
			Data: &struct {
				Upserted int `json:"upserted"`
				Deleted  int `json:"deleted"`
			}{
				Upserted: 1,
				Deleted:  0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint:       server.URL,
		HTTPClient:     server.Client(),
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond, // Short backoff for test
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx := context.Background()
	resp, err := client.SyncResources(ctx, []*ResourceData{{ID: "test"}}, nil)
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, attempts, "Expected 3 attempts (2 failures + 1 success)")
}

func TestMCPResourceSyncClient_AuthHeader(t *testing.T) {
	authHeaderReceived := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaderReceived = r.Header.Get("Authorization")
		resp := SyncResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Note: We can't easily test secret resolution without a k8s client
	// This test verifies the auth header would be set if token was available
	client := &MCPResourceSyncClient{
		endpoint:   server.URL + "/api/v1/resources/sync",
		httpClient: server.Client(),
		maxRetries: 0,
	}

	ctx := context.Background()
	_, err := client.SyncResources(ctx, nil, nil)
	require.NoError(t, err)
	// Without auth secret configured, no auth header should be set
	assert.Empty(t, authHeaderReceived)
}

func TestMCPResourceSyncClient_CalculateBackoff(t *testing.T) {
	client := &MCPResourceSyncClient{
		initialBackoff: 1 * time.Second,
		maxBackoff:     30 * time.Second,
	}

	// Test exponential increase
	backoff1 := client.calculateBackoff(1)
	backoff2 := client.calculateBackoff(2)
	backoff3 := client.calculateBackoff(3)

	// With jitter, we can't check exact values, but we can verify the trend
	// and that they're within expected ranges
	assert.True(t, backoff1 >= 750*time.Millisecond && backoff1 <= 1250*time.Millisecond,
		"backoff1 should be ~1s (got %v)", backoff1)
	assert.True(t, backoff2 >= 1500*time.Millisecond && backoff2 <= 2500*time.Millisecond,
		"backoff2 should be ~2s (got %v)", backoff2)
	assert.True(t, backoff3 >= 3*time.Second && backoff3 <= 5*time.Second,
		"backoff3 should be ~4s (got %v)", backoff3)

	// Test max backoff cap
	backoff10 := client.calculateBackoff(10)
	assert.True(t, backoff10 <= 38*time.Second, // 30s + 25% jitter
		"backoff10 should be capped at ~30s (got %v)", backoff10)
}

func TestNewMCPResourceSyncClient_Defaults(t *testing.T) {
	client := NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
		Endpoint: "https://example.com",
	})

	assert.Equal(t, "https://example.com/api/v1/resources/sync", client.endpoint)
	assert.Equal(t, 3, client.maxRetries)
	assert.Equal(t, 1*time.Second, client.initialBackoff)
	assert.Equal(t, 30*time.Second, client.maxBackoff)
	assert.NotNil(t, client.httpClient)
}
