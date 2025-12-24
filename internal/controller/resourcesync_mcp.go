// resourcesync_mcp.go contains the MCP client for syncing resources to the MCP server.
// This file handles HTTP communication, request/response types, and retry logic
// for the resource sync endpoint.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// SyncRequest is the request body for POST /api/v1/resources/sync
type SyncRequest struct {
	// Upserts contains resources to create or update
	Upserts []*ResourceData `json:"upserts,omitempty"`
	// Deletes contains resource identifiers to delete
	// MCP constructs the ID from namespace/apiVersion/kind/name
	Deletes []*ResourceIdentifier `json:"deletes,omitempty"`
	// IsResync indicates this is a full resync (MCP should diff against Qdrant)
	IsResync bool `json:"isResync,omitempty"`
}

// SyncResponse is the response from POST /api/v1/resources/sync
// Follows MCP's RestApiResponse pattern
type SyncResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Upserted int `json:"upserted"`
		Deleted  int `json:"deleted"`
	} `json:"data,omitempty"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details *struct {
			Upserted int           `json:"upserted,omitempty"`
			Deleted  int           `json:"deleted,omitempty"`
			Failures []SyncFailure `json:"failures,omitempty"`
		} `json:"details,omitempty"`
	} `json:"error,omitempty"`
	Meta *struct {
		Timestamp string `json:"timestamp"`
		RequestId string `json:"requestId"`
		Version   string `json:"version"`
	} `json:"meta,omitempty"`
}

// SyncFailure represents a single resource that failed to sync
type SyncFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// GetSuccessCounts returns the number of upserted and deleted resources
func (r *SyncResponse) GetSuccessCounts() (upserted, deleted int) {
	if r.Data != nil {
		return r.Data.Upserted, r.Data.Deleted
	}
	return 0, 0
}

// GetErrorMessage extracts error message from the response
func (r *SyncResponse) GetErrorMessage() string {
	if r.Error != nil {
		if r.Error.Message != "" {
			return r.Error.Message
		}
		if r.Error.Code != "" {
			return fmt.Sprintf("error code: %s", r.Error.Code)
		}
	}
	return "unknown error"
}

// GetFailures returns the list of individual resource failures
func (r *SyncResponse) GetFailures() []SyncFailure {
	if r.Error != nil && r.Error.Details != nil {
		return r.Error.Details.Failures
	}
	return nil
}

// MCPResourceSyncClient handles HTTP communication with the MCP resource sync endpoint
type MCPResourceSyncClient struct {
	// endpoint is the full URL for the sync endpoint (e.g., https://mcp.example.com/api/v1/resources/sync)
	endpoint string
	// httpClient is the HTTP client with configured timeout
	httpClient *http.Client
	// k8sClient is for fetching secrets
	k8sClient client.Client
	// authSecretRef references the secret containing the API key
	authSecretRef dotaiv1alpha1.SecretReference
	// authSecretNamespace is the namespace where the auth secret is located
	authSecretNamespace string

	// Retry configuration
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// MCPResourceSyncClientConfig holds configuration for creating an MCPResourceSyncClient
type MCPResourceSyncClientConfig struct {
	Endpoint            string
	HTTPClient          *http.Client
	K8sClient           client.Client
	AuthSecretRef       dotaiv1alpha1.SecretReference
	AuthSecretNamespace string
	MaxRetries          *int // Pointer to distinguish "not set" (nil->default 3) from "set to 0"
	InitialBackoff      time.Duration
	MaxBackoff          time.Duration
}

// NewMCPResourceSyncClient creates a new MCP resource sync client
func NewMCPResourceSyncClient(cfg MCPResourceSyncClientConfig) *MCPResourceSyncClient {
	// Apply defaults
	maxRetries := 3
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 1 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}

	return &MCPResourceSyncClient{
		endpoint:            strings.TrimSuffix(cfg.Endpoint, "/"),
		httpClient:          cfg.HTTPClient,
		k8sClient:           cfg.K8sClient,
		authSecretRef:       cfg.AuthSecretRef,
		authSecretNamespace: cfg.AuthSecretNamespace,
		maxRetries:          maxRetries,
		initialBackoff:      cfg.InitialBackoff,
		maxBackoff:          cfg.MaxBackoff,
	}
}

// SyncResources sends upserts and deletes to the MCP endpoint
func (c *MCPResourceSyncClient) SyncResources(ctx context.Context, upserts []*ResourceData, deletes []*ResourceIdentifier) (*SyncResponse, error) {
	req := SyncRequest{
		Upserts:  upserts,
		Deletes:  deletes,
		IsResync: false,
	}
	return c.sendWithRetry(ctx, req)
}

// Resync sends a full resync request to the MCP endpoint
// MCP will diff against Qdrant and handle deletions of orphaned records
func (c *MCPResourceSyncClient) Resync(ctx context.Context, allResources []*ResourceData) (*SyncResponse, error) {
	req := SyncRequest{
		Upserts:  allResources,
		IsResync: true,
	}
	return c.sendWithRetry(ctx, req)
}

// sendWithRetry sends the request with exponential backoff retry
func (c *MCPResourceSyncClient) sendWithRetry(ctx context.Context, req SyncRequest) (*SyncResponse, error) {
	logger := logf.FromContext(ctx).WithName("resourcesync-mcp")

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Calculate backoff with jitter
			backoff := c.calculateBackoff(attempt)
			logger.V(1).Info("Retrying MCP request",
				"attempt", attempt,
				"maxRetries", c.maxRetries,
				"backoff", backoff,
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		resp, err := c.send(ctx, req)
		if err == nil && resp.Success {
			return resp, nil
		}

		if err != nil {
			lastErr = err
			logger.V(1).Info("MCP request failed",
				"attempt", attempt,
				"error", err,
			)
		} else if !resp.Success {
			lastErr = fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
			logger.V(1).Info("MCP returned error response",
				"attempt", attempt,
				"error", resp.GetErrorMessage(),
			)
			// If we got a response but with partial success, return it
			if resp.Error != nil && resp.Error.Details != nil {
				return resp, lastErr
			}
		}

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", c.maxRetries, lastErr)
}

// calculateBackoff returns the backoff duration with jitter for the given attempt
func (c *MCPResourceSyncClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initialBackoff * 2^attempt
	backoff := float64(c.initialBackoff) * math.Pow(2, float64(attempt-1))

	// Cap at maxBackoff
	if backoff > float64(c.maxBackoff) {
		backoff = float64(c.maxBackoff)
	}

	// Add jitter (±25%)
	jitter := backoff * 0.25 * (rand.Float64()*2 - 1)
	return time.Duration(backoff + jitter)
}

// send performs a single HTTP request to the MCP endpoint
func (c *MCPResourceSyncClient) send(ctx context.Context, req SyncRequest) (*SyncResponse, error) {
	logger := logf.FromContext(ctx).WithName("resourcesync-mcp")

	// Get auth token if configured
	authToken, err := c.getAuthToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	// Marshal request
	requestBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.V(1).Info("Sending sync request",
		"endpoint", c.endpoint,
		"upserts", len(req.Upserts),
		"deletes", len(req.Deletes),
		"isResync", req.IsResync,
		"bodySize", len(requestBody),
	)

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "dot-ai-controller/v1.0.0")
	if authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+authToken)
	}

	// Send request
	startTime := time.Now()
	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(startTime)

	if err != nil {
		logger.Error(err, "HTTP request failed",
			"duration", duration,
		)
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	logger.V(1).Info("Received response",
		"statusCode", resp.StatusCode,
		"duration", duration,
		"bodySize", len(responseBody),
	)

	// Handle HTTP errors
	if resp.StatusCode >= 400 {
		return &SyncResponse{
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
				Code:    fmt.Sprintf("%d", resp.StatusCode),
				Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(responseBody)),
			},
		}, nil
	}

	// Parse JSON response
	var syncResponse SyncResponse
	if err := json.Unmarshal(responseBody, &syncResponse); err != nil {
		// If JSON parsing fails but HTTP was successful, treat as success
		// Log at Info level (not V(1)) to make unexpected responses visible
		logger.Info("Response is not JSON, treating as successful",
			"response", string(responseBody),
			"parseError", err.Error(),
		)
		return &SyncResponse{
			Success: true,
			Data: &struct {
				Upserted int `json:"upserted"`
				Deleted  int `json:"deleted"`
			}{
				Upserted: len(req.Upserts),
				Deleted:  len(req.Deletes),
			},
		}, nil
	}

	upserted, deleted := syncResponse.GetSuccessCounts()
	logger.Info("Sync completed",
		"success", syncResponse.Success,
		"upserted", upserted,
		"deleted", deleted,
		"duration", duration,
	)

	return &syncResponse, nil
}

// getAuthToken resolves the auth token from the secret reference
func (c *MCPResourceSyncClient) getAuthToken(ctx context.Context) (string, error) {
	// If no auth secret is configured (empty name), skip auth
	// This allows tests to work without auth while production requires it via API validation
	if c.authSecretRef.Name == "" {
		return "", nil
	}

	logger := logf.FromContext(ctx).WithName("resourcesync-mcp")

	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: c.authSecretNamespace,
		Name:      c.authSecretRef.Name,
	}

	if err := c.k8sClient.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("auth secret '%s' not found in namespace '%s'",
				c.authSecretRef.Name, c.authSecretNamespace)
		}
		return "", fmt.Errorf("failed to fetch auth secret: %w", err)
	}

	tokenBytes, exists := secret.Data[c.authSecretRef.Key]
	if !exists {
		return "", fmt.Errorf("auth secret '%s' does not contain key '%s'",
			c.authSecretRef.Name, c.authSecretRef.Key)
	}

	if len(tokenBytes) == 0 {
		return "", fmt.Errorf("auth secret '%s' key '%s' is empty",
			c.authSecretRef.Name, c.authSecretRef.Key)
	}

	logger.V(2).Info("Resolved auth token from secret",
		"secretName", c.authSecretRef.Name,
		"secretKey", c.authSecretRef.Key,
	)

	return string(tokenBytes), nil
}
