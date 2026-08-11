// capabilityscan_mcp.go contains the MCP client for capability scanning.
// This file handles HTTP communication with the MCP server's manageOrgData endpoint
// for listing, scanning, and deleting capabilities.
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

// ManageOrgDataRequest is the request body for POST /api/v1/tools/manageOrgData
type ManageOrgDataRequest struct {
	DataType     string `json:"dataType"`
	Operation    string `json:"operation"`
	Collection   string `json:"collection,omitempty"`
	Mode         string `json:"mode,omitempty"`         // For scan operation: "full"
	ResourceList string `json:"resourceList,omitempty"` // For targeted scan
	ID           string `json:"id,omitempty"`           // For get/delete operations
	Limit        int    `json:"limit,omitempty"`        // For list operation
}

// CapabilityInfo represents a capability returned from MCP.
// ID is a UUID used for deletion; ResourceName is the "Kind.group" identifier
// used to match capabilities against cluster resources during diffing.
type CapabilityInfo struct {
	ID           string `json:"id"`
	ResourceName string `json:"resourceName"`
}

// ManageOrgDataListData holds the list payload. The MCP server nests it under
// result.data (i.e. data.result.data.{capabilities,totalCount,returnedCount}).
// TotalCount and ReturnedCount are pointers so an omitted count is distinguished
// from a real zero; a missing count leaves completeness unproven and must block
// deletions rather than pass a 0 == 0 check.
type ManageOrgDataListData struct {
	Capabilities  []CapabilityInfo `json:"capabilities,omitempty"`
	TotalCount    *int             `json:"totalCount,omitempty"`
	ReturnedCount *int             `json:"returnedCount,omitempty"`
}

// ManageOrgDataResult is the operation result nested under data.result.
// Success here is the operation outcome, distinct from the transport-level
// Success on ManageOrgDataResponse.
type ManageOrgDataResult struct {
	Success   bool                   `json:"success"`
	Status    string                 `json:"status,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Operation string                 `json:"operation,omitempty"`
	Data      *ManageOrgDataListData `json:"data,omitempty"`
}

// ManageOrgDataEnvelope is the data envelope nested under the top-level data.
type ManageOrgDataEnvelope struct {
	Result *ManageOrgDataResult `json:"result,omitempty"`
}

// ManageOrgDataError is the top-level error object.
type ManageOrgDataError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ManageOrgDataResponse is the response from POST /api/v1/tools/manageOrgData
type ManageOrgDataResponse struct {
	Success bool                   `json:"success"`
	Data    *ManageOrgDataEnvelope `json:"data,omitempty"`
	Error   *ManageOrgDataError    `json:"error,omitempty"`
}

// result returns the nested operation result, or nil if absent.
func (r *ManageOrgDataResponse) result() *ManageOrgDataResult {
	if r.Data == nil {
		return nil
	}
	return r.Data.Result
}

// GetErrorMessage extracts error message from the response
func (r *ManageOrgDataResponse) GetErrorMessage() string {
	if r.Error != nil {
		if r.Error.Message != "" {
			return r.Error.Message
		}
		if r.Error.Code != "" {
			return fmt.Sprintf("error code: %s", r.Error.Code)
		}
	}
	if res := r.result(); res != nil && res.Message != "" {
		return res.Message
	}
	return "unknown error"
}

// HasResultError reports whether the operation failed. manageOrgData catches
// every error and returns HTTP 200 with the transport Success flag set, so the
// operation outcome only lives in data.result.success. A missing nested result
// is itself a failure: a bare {"success": true} envelope must not be accepted
// as a completed operation.
func (r *ManageOrgDataResponse) HasResultError() bool {
	res := r.result()
	return res == nil || !res.Success
}

// GetTotalCount returns the total count of capabilities from a list response
func (r *ManageOrgDataResponse) GetTotalCount() int {
	if res := r.result(); res != nil && res.Data != nil && res.Data.TotalCount != nil {
		return *res.Data.TotalCount
	}
	return 0
}

// GetCapabilities returns the capabilities from a list response
func (r *ManageOrgDataResponse) GetCapabilities() []CapabilityInfo {
	res := r.result()
	if res == nil || res.Data == nil {
		return nil
	}
	return res.Data.Capabilities
}

// IsListComplete reports whether a list response returned every capability.
// The server does not cap totalCount, so returnedCount == totalCount proves the
// list is complete even against a server that still limits the returned page.
// An empty collection (both zero) is complete. Both counts must be present: an
// omitted count leaves completeness unproven, which must block deletions.
func (r *ManageOrgDataResponse) IsListComplete() bool {
	res := r.result()
	if res == nil || res.Data == nil {
		return false
	}
	d := res.Data
	return d.TotalCount != nil && d.ReturnedCount != nil && *d.ReturnedCount == *d.TotalCount
}

// MCPCapabilityScanClient handles HTTP communication with the MCP capability scan endpoint
type MCPCapabilityScanClient struct {
	endpoint            string
	collection          string
	httpClient          *http.Client
	k8sClient           client.Client
	authSecretRef       dotaiv1alpha1.SecretReference
	authSecretNamespace string
	maxRetries          int
	initialBackoff      time.Duration
	maxBackoff          time.Duration
}

// MCPCapabilityScanClientConfig holds configuration for creating an MCPCapabilityScanClient
type MCPCapabilityScanClientConfig struct {
	Endpoint            string
	Collection          string
	HTTPClient          *http.Client
	K8sClient           client.Client
	AuthSecretRef       dotaiv1alpha1.SecretReference
	AuthSecretNamespace string
	MaxRetries          *int // Pointer to distinguish "not set" (nil->default 3) from "set to 0"
	InitialBackoff      time.Duration
	MaxBackoff          time.Duration
}

// NewMCPCapabilityScanClient creates a new MCP capability scan client
func NewMCPCapabilityScanClient(cfg MCPCapabilityScanClientConfig) *MCPCapabilityScanClient {
	// Apply defaults
	maxRetries := 3
	if cfg.MaxRetries != nil {
		maxRetries = *cfg.MaxRetries
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 5 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 300 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}
	if cfg.Collection == "" {
		cfg.Collection = "capabilities"
	}

	// Build endpoint URL
	endpoint := strings.TrimSuffix(cfg.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/api/v1/tools/manageOrgData") {
		endpoint = endpoint + "/api/v1/tools/manageOrgData"
	}

	return &MCPCapabilityScanClient{
		endpoint:            endpoint,
		collection:          cfg.Collection,
		httpClient:          cfg.HTTPClient,
		k8sClient:           cfg.K8sClient,
		authSecretRef:       cfg.AuthSecretRef,
		authSecretNamespace: cfg.AuthSecretNamespace,
		maxRetries:          maxRetries,
		initialBackoff:      cfg.InitialBackoff,
		maxBackoff:          cfg.MaxBackoff,
	}
}

// ListCapabilities checks how many capabilities exist in the database
func (c *MCPCapabilityScanClient) ListCapabilities(ctx context.Context) (int, error) {
	req := ManageOrgDataRequest{
		DataType:   "capabilities",
		Operation:  "list",
		Collection: c.collection,
		Limit:      1, // We only need to know if any exist
	}

	resp, err := c.sendWithRetry(ctx, req)
	if err != nil {
		return 0, err
	}

	if !resp.Success {
		return 0, fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
	}

	return resp.GetTotalCount(), nil
}

// ListCapabilityInfos returns all capabilities (id + resourceName) from the
// database, along with whether the returned list is complete. Completeness is
// derived from returnedCount == totalCount; because the server does not cap
// totalCount, this holds even against a server that still limits the returned
// page. An incomplete list must not drive deletions.
func (c *MCPCapabilityScanClient) ListCapabilityInfos(ctx context.Context) ([]CapabilityInfo, bool, error) {
	// Use a large limit to get all capabilities
	// Most clusters have < 1000 CRDs, so this should be sufficient
	req := ManageOrgDataRequest{
		DataType:   "capabilities",
		Operation:  "list",
		Collection: c.collection,
		Limit:      10000,
	}

	resp, err := c.sendWithRetry(ctx, req)
	if err != nil {
		return nil, false, err
	}

	if !resp.Success {
		return nil, false, fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
	}

	return resp.GetCapabilities(), resp.IsListComplete(), nil
}

// TriggerFullScan triggers a full cluster capability scan
func (c *MCPCapabilityScanClient) TriggerFullScan(ctx context.Context) error {
	req := ManageOrgDataRequest{
		DataType:   "capabilities",
		Operation:  "scan",
		Mode:       "full",
		Collection: c.collection,
	}

	resp, err := c.sendWithRetry(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
	}

	return nil
}

// TriggerScan triggers a targeted scan for specific resources
func (c *MCPCapabilityScanClient) TriggerScan(ctx context.Context, resourceList string) error {
	req := ManageOrgDataRequest{
		DataType:     "capabilities",
		Operation:    "scan",
		ResourceList: resourceList,
		Collection:   c.collection,
	}

	resp, err := c.sendWithRetry(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
	}

	return nil
}

// DeleteCapability deletes a capability by ID
func (c *MCPCapabilityScanClient) DeleteCapability(ctx context.Context, id string) error {
	req := ManageOrgDataRequest{
		DataType:   "capabilities",
		Operation:  "delete",
		ID:         id,
		Collection: c.collection,
	}

	resp, err := c.sendWithRetry(ctx, req)
	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("MCP returned error: %s", resp.GetErrorMessage())
	}

	return nil
}

// sendWithRetry sends the request with exponential backoff retry
func (c *MCPCapabilityScanClient) sendWithRetry(ctx context.Context, req ManageOrgDataRequest) (*ManageOrgDataResponse, error) {
	logger := logf.FromContext(ctx).WithName("capabilityscan-mcp")

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
		if err == nil && resp.Success && !resp.HasResultError() {
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
		} else {
			// Envelope reports success but the operation result reports a
			// failure (e.g. the vector backend is unavailable). Treat it as a
			// retryable error so it is surfaced instead of masked as an empty
			// result — otherwise a not-ready backend looks like an empty
			// collection on a fresh install.
			lastErr = fmt.Errorf("MCP operation failed: %s", resp.GetErrorMessage())
			logger.V(1).Info("MCP operation reported failure",
				"attempt", attempt,
				"error", resp.GetErrorMessage(),
			)
		}

		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	return nil, fmt.Errorf("failed after %d retries: %w", c.maxRetries, lastErr)
}

// calculateBackoff returns the backoff duration with jitter for the given attempt
func (c *MCPCapabilityScanClient) calculateBackoff(attempt int) time.Duration {
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
func (c *MCPCapabilityScanClient) send(ctx context.Context, req ManageOrgDataRequest) (*ManageOrgDataResponse, error) {
	logger := logf.FromContext(ctx).WithName("capabilityscan-mcp")

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

	logger.V(1).Info("Sending request",
		"endpoint", c.endpoint,
		"operation", req.Operation,
		"mode", req.Mode,
		"resourceList", req.ResourceList,
		"id", req.ID,
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
		logger.Error(err, "❌ HTTP request failed", "duration", duration)
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
		return &ManageOrgDataResponse{
			Success: false,
			Error: &ManageOrgDataError{
				Code:    fmt.Sprintf("%d", resp.StatusCode),
				Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(responseBody)),
			},
		}, nil
	}

	// Parse JSON response
	var mcpResponse ManageOrgDataResponse
	if err := json.Unmarshal(responseBody, &mcpResponse); err != nil {
		// If JSON parsing fails but HTTP was successful, treat as success
		logger.Info("⚠️ Response is not JSON, treating as successful",
			"response", string(responseBody),
			"parseError", err.Error(),
		)
		return &ManageOrgDataResponse{
			Success: true,
		}, nil
	}

	logger.Info("✅ Request completed",
		"success", mcpResponse.Success,
		"operation", req.Operation,
		"duration", duration,
	)

	return &mcpResponse, nil
}

// getAuthToken resolves the auth token from the secret reference
func (c *MCPCapabilityScanClient) getAuthToken(ctx context.Context) (string, error) {
	// If no auth secret is configured, skip auth
	if c.authSecretRef.Name == "" {
		return "", nil
	}

	logger := logf.FromContext(ctx).WithName("capabilityscan-mcp")

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
