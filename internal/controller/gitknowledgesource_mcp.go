package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

const (
	// DefaultMCPMaxRetries is the default number of retry attempts for MCP calls
	DefaultMCPMaxRetries = 3
	// DefaultMCPInitialBackoff is the default initial backoff duration
	DefaultMCPInitialBackoff = 1 * time.Second
	// DefaultMCPMaxBackoff is the default maximum backoff duration
	DefaultMCPMaxBackoff = 30 * time.Second
	// DefaultMCPTimeout is the default HTTP timeout for MCP calls
	DefaultMCPTimeout = 30 * time.Second
)

// MCPKnowledgeClientConfig holds the configuration for creating an MCPKnowledgeClient.
type MCPKnowledgeClientConfig struct {
	// Endpoint is the full URL for the manageKnowledge API
	// Example: http://mcp-server.dot-ai.svc:3456/api/v1/tools/manageKnowledge
	Endpoint string
	// AuthToken is the bearer token for authentication (optional)
	AuthToken string
	// HTTPClient is the HTTP client to use (optional, uses default if nil)
	HTTPClient *http.Client
	// MaxRetries is the maximum number of retry attempts (optional, default: 3)
	MaxRetries *int
	// InitialBackoff is the initial backoff duration (optional, default: 1s)
	InitialBackoff time.Duration
	// MaxBackoff is the maximum backoff duration (optional, default: 30s)
	MaxBackoff time.Duration
}

// MCPKnowledgeClient handles communication with the MCP manageKnowledge API.
type MCPKnowledgeClient struct {
	endpoint       string
	authToken      string
	httpClient     *http.Client
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// NewMCPKnowledgeClient creates a new MCPKnowledgeClient with the given configuration.
func NewMCPKnowledgeClient(cfg MCPKnowledgeClientConfig) *MCPKnowledgeClient {
	maxRetries := DefaultMCPMaxRetries
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}

	initialBackoff := cfg.InitialBackoff
	if initialBackoff == 0 {
		initialBackoff = DefaultMCPInitialBackoff
	}

	maxBackoff := cfg.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = DefaultMCPMaxBackoff
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: DefaultMCPTimeout,
		}
	}

	return &MCPKnowledgeClient{
		endpoint:       cfg.Endpoint,
		authToken:      cfg.AuthToken,
		httpClient:     httpClient,
		maxRetries:     maxRetries,
		initialBackoff: initialBackoff,
		maxBackoff:     maxBackoff,
	}
}

// IngestRequest represents a request to ingest a document into the knowledge base.
type IngestRequest struct {
	Operation string            `json:"operation"`
	URI       string            `json:"uri"`
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// IngestResponse represents the response from an ingest operation.
type IngestResponse struct {
	Success       bool     `json:"success"`
	Operation     string   `json:"operation,omitempty"`
	ChunksCreated int      `json:"chunksCreated,omitempty"`
	ChunkIDs      []string `json:"chunkIds,omitempty"`
	URI           string   `json:"uri,omitempty"`
	Message       string   `json:"message,omitempty"`
	Error         *struct {
		Message   string `json:"message"`
		Operation string `json:"operation,omitempty"`
		Hint      string `json:"hint,omitempty"`
	} `json:"error,omitempty"`
}

// DeleteRequest represents a request to delete a document from the knowledge base.
type DeleteRequest struct {
	Operation string `json:"operation"`
	URI       string `json:"uri"`
}

// DeleteResponse represents the response from a delete operation.
type DeleteResponse struct {
	Success       bool   `json:"success"`
	Operation     string `json:"operation,omitempty"`
	URI           string `json:"uri,omitempty"`
	ChunksDeleted int    `json:"chunksDeleted,omitempty"`
	Message       string `json:"message,omitempty"`
	Error         *struct {
		Message   string `json:"message"`
		Operation string `json:"operation,omitempty"`
		Hint      string `json:"hint,omitempty"`
	} `json:"error,omitempty"`
}

// IngestDocument sends a document to the MCP knowledge base.
// It returns the response and any error encountered.
// The operation is retried with exponential backoff on transient failures.
func (c *MCPKnowledgeClient) IngestDocument(ctx context.Context, uri, content string, metadata map[string]string) (*IngestResponse, error) {
	req := IngestRequest{
		Operation: "ingest",
		URI:       uri,
		Content:   content,
		Metadata:  metadata,
	}

	var resp IngestResponse
	if err := c.doRequestWithRetry(ctx, req, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		errMsg := "unknown error"
		if resp.Error != nil && resp.Error.Message != "" {
			errMsg = resp.Error.Message
		}
		return &resp, fmt.Errorf("MCP ingest failed: %s", errMsg)
	}

	return &resp, nil
}

// DeleteDocument removes a document from the MCP knowledge base by URI.
// It returns the response and any error encountered.
// The operation is idempotent - deleting a non-existent document succeeds with chunksDeleted=0.
func (c *MCPKnowledgeClient) DeleteDocument(ctx context.Context, uri string) (*DeleteResponse, error) {
	req := DeleteRequest{
		Operation: "deleteByUri",
		URI:       uri,
	}

	var resp DeleteResponse
	if err := c.doRequestWithRetry(ctx, req, &resp); err != nil {
		return nil, err
	}

	if !resp.Success {
		errMsg := "unknown error"
		if resp.Error != nil && resp.Error.Message != "" {
			errMsg = resp.Error.Message
		}
		return &resp, fmt.Errorf("MCP delete failed: %s", errMsg)
	}

	return &resp, nil
}

// doRequestWithRetry performs an HTTP POST request with retry logic.
func (c *MCPKnowledgeClient) doRequestWithRetry(ctx context.Context, reqBody interface{}, respBody interface{}) error {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		err := c.doRequest(ctx, reqBody, respBody)
		if err == nil {
			return nil
		}

		lastErr = err

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return fmt.Errorf("MCP request failed after %d attempts: %w", c.maxRetries+1, lastErr)
}

// doRequest performs a single HTTP POST request.
func (c *MCPKnowledgeClient) doRequest(ctx context.Context, reqBody interface{}, respBody interface{}) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for HTTP-level errors
	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("client error (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, respBody); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return nil
}

// calculateBackoff returns the backoff duration for the given attempt number.
// Uses exponential backoff with jitter.
func (c *MCPKnowledgeClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initialBackoff * 2^(attempt-1)
	backoff := c.initialBackoff
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff > c.maxBackoff {
			backoff = c.maxBackoff
			break
		}
	}

	// Add jitter: +/- 25%
	jitter := float64(backoff) * 0.25 * (rand.Float64()*2 - 1)
	backoff = time.Duration(float64(backoff) + jitter)

	if backoff > c.maxBackoff {
		backoff = c.maxBackoff
	}

	return backoff
}
