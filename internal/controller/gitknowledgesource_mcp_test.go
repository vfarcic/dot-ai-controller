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
	"k8s.io/utils/ptr"
)

func TestMCPKnowledgeClient_IngestDocument_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse request body
		var req IngestRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "ingest", req.Operation)
		assert.Equal(t, "https://github.com/acme/repo/blob/main/docs/guide.md", req.URI)
		assert.Equal(t, "# Guide\n\nThis is a guide.", req.Content)
		assert.Equal(t, "test-source", req.Metadata["source"])

		// Send success response
		resp := IngestResponse{
			Success:       true,
			Operation:     "ingest",
			ChunksCreated: 2,
			ChunkIDs:      []string{"chunk-1", "chunk-2"},
			URI:           req.URI,
			Message:       "Successfully ingested document into 2 chunks",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	resp, err := client.IngestDocument(ctx,
		"https://github.com/acme/repo/blob/main/docs/guide.md",
		"# Guide\n\nThis is a guide.",
		map[string]string{"source": "test-source"},
	)

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 2, resp.ChunksCreated)
	assert.Len(t, resp.ChunkIDs, 2)
}

func TestMCPKnowledgeClient_IngestDocument_EmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := IngestResponse{
			Success:       true,
			Operation:     "ingest",
			ChunksCreated: 0,
			ChunkIDs:      []string{},
			Message:       "Empty or whitespace-only content - no chunks created",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	resp, err := client.IngestDocument(ctx, "https://example.com/doc.md", "", nil)

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 0, resp.ChunksCreated)
}

func TestMCPKnowledgeClient_IngestDocument_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := IngestResponse{
			Success: false,
			Error: &struct {
				Message   string `json:"message"`
				Operation string `json:"operation,omitempty"`
				Hint      string `json:"hint,omitempty"`
			}{
				Message:   "Failed to generate embeddings",
				Operation: "ingest",
				Hint:      "Check OpenAI API key",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		MaxRetries: ptr.To(0), // No retries for faster test
	})

	ctx := context.Background()
	resp, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Failed to generate embeddings")
	assert.NotNil(t, resp)
	assert.False(t, resp.Success)
}

func TestMCPKnowledgeClient_DeleteDocument_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		var req DeleteRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "deleteByUri", req.Operation)
		assert.Equal(t, "https://github.com/acme/repo/blob/main/docs/old.md", req.URI)

		// Send success response
		resp := DeleteResponse{
			Success:       true,
			Operation:     "deleteByUri",
			URI:           req.URI,
			ChunksDeleted: 3,
			Message:       "Successfully deleted 3 chunks for URI",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	resp, err := client.DeleteDocument(ctx, "https://github.com/acme/repo/blob/main/docs/old.md")

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, resp.ChunksDeleted)
}

func TestMCPKnowledgeClient_DeleteDocument_NotFound(t *testing.T) {
	// Deleting a non-existent document should succeed (idempotent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeleteResponse{
			Success:       true,
			Operation:     "deleteByUri",
			ChunksDeleted: 0,
			Message:       "No chunks found for URI",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	resp, err := client.DeleteDocument(ctx, "https://example.com/nonexistent.md")

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 0, resp.ChunksDeleted)
}

func TestMCPKnowledgeClient_RetryOnServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Service Unavailable"))
			return
		}

		resp := IngestResponse{
			Success:       true,
			ChunksCreated: 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:       server.URL,
		HTTPClient:     server.Client(),
		MaxRetries:     ptr.To(3),
		InitialBackoff: 10 * time.Millisecond, // Short for test
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx := context.Background()
	resp, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, attempts, "Expected 3 attempts (2 failures + 1 success)")
}

func TestMCPKnowledgeClient_AuthHeader(t *testing.T) {
	var receivedAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		resp := IngestResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		AuthToken:  "test-token-123",
	})

	ctx := context.Background()
	_, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.NoError(t, err)
	assert.Equal(t, "Bearer test-token-123", receivedAuthHeader)
}

func TestMCPKnowledgeClient_NoAuthHeader(t *testing.T) {
	var receivedAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		resp := IngestResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		// No AuthToken
	})

	ctx := context.Background()
	_, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.NoError(t, err)
	assert.Empty(t, receivedAuthHeader)
}

func TestMCPKnowledgeClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		resp := IngestResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
}

func TestMCPKnowledgeClient_CalculateBackoff(t *testing.T) {
	client := &MCPKnowledgeClient{
		initialBackoff: 1 * time.Second,
		maxBackoff:     30 * time.Second,
	}

	// Test exponential increase with jitter tolerance
	backoff1 := client.calculateBackoff(1)
	backoff2 := client.calculateBackoff(2)
	backoff3 := client.calculateBackoff(3)

	// With jitter, values should be roughly 1s, 2s, 4s (+/- 25%)
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

func TestNewMCPKnowledgeClient_Defaults(t *testing.T) {
	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint: "https://example.com/api/v1/tools/manageKnowledge",
	})

	assert.Equal(t, "https://example.com/api/v1/tools/manageKnowledge", client.endpoint)
	assert.Equal(t, DefaultMCPMaxRetries, client.maxRetries)
	assert.Equal(t, DefaultMCPInitialBackoff, client.initialBackoff)
	assert.Equal(t, DefaultMCPMaxBackoff, client.maxBackoff)
	assert.NotNil(t, client.httpClient)
}

func TestMCPKnowledgeClient_HTTP4xxError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid request"}`))
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		MaxRetries: ptr.To(0),
	})

	ctx := context.Background()
	_, err := client.IngestDocument(ctx, "https://example.com/doc.md", "content", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "client error (HTTP 400)")
}

func TestMCPKnowledgeClient_DeleteBySource_Success(t *testing.T) {
	var receivedMethod string
	var receivedRawPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		// Use RawPath to verify URL encoding is preserved in the request
		// (Path is automatically decoded by Go's http package)
		receivedRawPath = r.URL.RawPath

		resp := DeleteBySourceResponse{
			Success: true,
			Data: &struct {
				SourceIdentifier string `json:"sourceIdentifier,omitempty"`
				ChunksDeleted    int    `json:"chunksDeleted,omitempty"`
			}{
				SourceIdentifier: "default/platform-docs",
				ChunksDeleted:    42,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	deleteURL := server.URL + "/api/v1/knowledge/source/default%2Fplatform-docs"
	resp, err := client.DeleteBySource(ctx, deleteURL)

	require.NoError(t, err)
	assert.Equal(t, "DELETE", receivedMethod)
	// RawPath preserves the %2F encoding
	assert.Equal(t, "/api/v1/knowledge/source/default%2Fplatform-docs", receivedRawPath)
	assert.True(t, resp.Success)
	assert.NotNil(t, resp.Data)
	assert.Equal(t, 42, resp.Data.ChunksDeleted)
}

func TestMCPKnowledgeClient_DeleteBySource_NonExistent(t *testing.T) {
	// Deleting a non-existent source should succeed (idempotent)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeleteBySourceResponse{
			Success: true,
			Data: &struct {
				SourceIdentifier string `json:"sourceIdentifier,omitempty"`
				ChunksDeleted    int    `json:"chunksDeleted,omitempty"`
			}{
				SourceIdentifier: "default/nonexistent",
				ChunksDeleted:    0,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
	})

	ctx := context.Background()
	deleteURL := server.URL + "/api/v1/knowledge/source/default%2Fnonexistent"
	resp, err := client.DeleteBySource(ctx, deleteURL)

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.NotNil(t, resp.Data)
	assert.Equal(t, 0, resp.Data.ChunksDeleted)
}

func TestMCPKnowledgeClient_DeleteBySource_RetryOnServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("Service Unavailable"))
			return
		}

		resp := DeleteBySourceResponse{
			Success: true,
			Data: &struct {
				SourceIdentifier string `json:"sourceIdentifier,omitempty"`
				ChunksDeleted    int    `json:"chunksDeleted,omitempty"`
			}{
				ChunksDeleted: 10,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:       server.URL,
		HTTPClient:     server.Client(),
		MaxRetries:     ptr.To(3),
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})

	ctx := context.Background()
	deleteURL := server.URL + "/api/v1/knowledge/source/default%2Ftest"
	resp, err := client.DeleteBySource(ctx, deleteURL)

	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 3, attempts, "Expected 3 attempts (2 failures + 1 success)")
}

func TestMCPKnowledgeClient_DeleteBySource_AuthHeader(t *testing.T) {
	var receivedAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		resp := DeleteBySourceResponse{Success: true}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		AuthToken:  "delete-token-456",
	})

	ctx := context.Background()
	deleteURL := server.URL + "/api/v1/knowledge/source/ns%2Fname"
	_, err := client.DeleteBySource(ctx, deleteURL)

	require.NoError(t, err)
	assert.Equal(t, "Bearer delete-token-456", receivedAuthHeader)
}

func TestMCPKnowledgeClient_DeleteBySource_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeleteBySourceResponse{
			Success: false,
			Error: &struct {
				Message string `json:"message"`
			}{
				Message: "Internal error during deletion",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewMCPKnowledgeClient(MCPKnowledgeClientConfig{
		Endpoint:   server.URL,
		HTTPClient: server.Client(),
		MaxRetries: ptr.To(0),
	})

	ctx := context.Background()
	deleteURL := server.URL + "/api/v1/knowledge/source/ns%2Fname"
	resp, err := client.DeleteBySource(ctx, deleteURL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Internal error during deletion")
	assert.NotNil(t, resp)
	assert.False(t, resp.Success)
}
