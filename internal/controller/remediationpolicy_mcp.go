// remediationpolicy_mcp.go contains MCP (Model Context Protocol) client code
// for the RemediationPolicy controller. This file handles communication with
// the MCP endpoint for AI-powered remediation.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// McpResponse represents the response from MCP remediate endpoint
// Based on the actual OpenAPI schema: RestApiResponse + ToolExecutionResponse
type McpResponse struct {
	Success bool `json:"success"`
	Data    *struct {
		Result        map[string]interface{} `json:"result"`
		Tool          string                 `json:"tool"`
		ExecutionTime float64                `json:"executionTime"`
	} `json:"data,omitempty"`
	Error *struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Details map[string]interface{} `json:"details,omitempty"`
	} `json:"error,omitempty"`
	Meta *struct {
		Timestamp string `json:"timestamp"`
		RequestId string `json:"requestId"`
		Version   string `json:"version"`
	} `json:"meta,omitempty"`
}

// GetResultMessage extracts a meaningful message from the MCP response
func (r *McpResponse) GetResultMessage() string {
	if r.Data != nil && r.Data.Result != nil {
		// Try to extract a message from the result data
		if msg, ok := r.Data.Result["message"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := r.Data.Result["summary"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := r.Data.Result["output"].(string); ok && msg != "" {
			return msg
		}
		// If no specific message field, return a generic success message with execution time
		if r.Data.ExecutionTime > 0 {
			// ExecutionTime is in milliseconds per MCP API spec
			return fmt.Sprintf("remediation completed successfully (%.2fs)", r.Data.ExecutionTime/1000)
		}
		return "remediation completed successfully"
	}
	return "no result data"
}

// GetErrorMessage extracts error message from the MCP response
func (r *McpResponse) GetErrorMessage() string {
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

// generateIssueDescription creates a descriptive issue string from a Kubernetes event
func (r *RemediationPolicyReconciler) generateIssueDescription(event *corev1.Event) string {
	// Only proceed with structured description if we have object information
	if event.InvolvedObject.Kind != "" && event.InvolvedObject.Name != "" {
		// Use full resource identifier to avoid ambiguity with multiple CRDs
		resourceType := event.InvolvedObject.Kind
		if event.InvolvedObject.APIVersion != "" && event.InvolvedObject.APIVersion != "v1" {
			// Include API version for non-core resources to disambiguate (e.g., "SQL.devopstoolkit.live/v1beta1")
			resourceType = fmt.Sprintf("%s.%s", event.InvolvedObject.Kind, event.InvolvedObject.APIVersion)
		}

		objectDesc := fmt.Sprintf("%s %s", resourceType, event.InvolvedObject.Name)
		if event.InvolvedObject.Namespace != "" {
			objectDesc += fmt.Sprintf(" in namespace %s", event.InvolvedObject.Namespace)
		}

		baseDesc := ""
		if event.Reason != "" {
			baseDesc = fmt.Sprintf("%s has a %s event", objectDesc, event.Reason)
		} else {
			baseDesc = fmt.Sprintf("%s has an issue", objectDesc)
		}

		// Include the event message if available for additional context
		if event.Message != "" {
			return fmt.Sprintf("%s: %s", baseDesc, event.Message)
		}

		return baseDesc
	}

	// Fallback to event message when no object information is available
	return fmt.Sprintf("Kubernetes event: %s", event.Message)
}

// generateMcpRequest creates an MCP request from a Kubernetes event, policy, and selector
func (r *RemediationPolicyReconciler) generateMcpRequest(event *corev1.Event, policy *dotaiv1alpha1.RemediationPolicy, selector dotaiv1alpha1.EventSelector) *dotaiv1alpha1.McpRequest {
	effectiveMode := r.getEffectiveMode(selector, policy)

	request := &dotaiv1alpha1.McpRequest{
		Issue: r.generateIssueDescription(event),
		Mode:  effectiveMode,
	}

	// Only include confidence threshold and risk level for automatic mode
	if effectiveMode == "automatic" {
		confidenceThreshold := r.getEffectiveConfidenceThreshold(selector, policy)
		request.ConfidenceThreshold = &confidenceThreshold
		request.MaxRiskLevel = r.getEffectiveMaxRiskLevel(selector, policy)
	}

	return request
}

// generateAndLogMcpRequest generates an MCP request and logs it for validation
func (r *RemediationPolicyReconciler) generateAndLogMcpRequest(ctx context.Context, event *corev1.Event, policy *dotaiv1alpha1.RemediationPolicy, selector dotaiv1alpha1.EventSelector) (*dotaiv1alpha1.McpRequest, error) {
	logger := logf.FromContext(ctx)

	// Generate the MCP request
	mcpRequest := r.generateMcpRequest(event, policy, selector)

	// Marshal to JSON for logging and validation
	mcpRequestJSON, err := json.MarshalIndent(mcpRequest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MCP request to JSON: %w", err)
	}

	// Log the generated MCP request for validation
	logFields := []interface{}{
		"mcpEndpoint", policy.Spec.McpEndpoint,
		"mcpRequestJSON", string(mcpRequestJSON),
		"messageSize", len(mcpRequestJSON),
		"issue", mcpRequest.Issue,
		"mode", mcpRequest.Mode,
		"policyName", policy.Name,
	}

	// Add automatic mode specific fields to log if present
	if mcpRequest.ConfidenceThreshold != nil {
		logFields = append(logFields, "confidenceThreshold", *mcpRequest.ConfidenceThreshold)
	}
	if mcpRequest.MaxRiskLevel != "" {
		logFields = append(logFields, "maxRiskLevel", mcpRequest.MaxRiskLevel)
	}

	logger.Info("🚀 Generated MCP request", logFields...)

	// Generate Kubernetes Event for MCP message generation
	r.Recorder.Eventf(policy, corev1.EventTypeNormal, "McpMessageGenerated",
		"Generated MCP request for %s/%s event (mode: %s, size: %d bytes)",
		event.Type, event.Reason, mcpRequest.Mode, len(mcpRequestJSON))

	return mcpRequest, nil
}

// getMcpAuthToken resolves the MCP authentication token from a Secret reference
func (r *RemediationPolicyReconciler) getMcpAuthToken(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy) (string, error) {
	logger := logf.FromContext(ctx)
	secretRef := &policy.Spec.McpAuthSecretRef

	// Fetch the Secret
	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: policy.Namespace,
		Name:      secretRef.Name,
	}

	if err := r.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("MCP auth Secret '%s' not found in namespace '%s'",
				secretRef.Name, policy.Namespace)
		}
		return "", fmt.Errorf("failed to fetch MCP auth Secret: %w", err)
	}

	// Extract the key from Secret data
	tokenBytes, exists := secret.Data[secretRef.Key]
	if !exists {
		return "", fmt.Errorf("MCP auth Secret '%s' does not contain key '%s'",
			secretRef.Name, secretRef.Key)
	}

	if len(tokenBytes) == 0 {
		return "", fmt.Errorf("MCP auth Secret '%s' key '%s' is empty",
			secretRef.Name, secretRef.Key)
	}

	logger.V(1).Info("Resolved MCP auth token from Secret",
		"secretName", secretRef.Name,
		"secretKey", secretRef.Key)

	return string(tokenBytes), nil
}

// sendMcpRequest sends MCP request to the specified endpoint (single attempt, no retries)
func (r *RemediationPolicyReconciler) sendMcpRequest(ctx context.Context, mcpRequest *dotaiv1alpha1.McpRequest, endpoint string, authToken string) (*McpResponse, error) {
	logger := logf.FromContext(ctx)

	startTime := time.Now()
	logger.Info("🚀 Starting MCP request", "endpoint", endpoint, "startTime", startTime.Format(time.RFC3339Nano))

	// Marshal request to JSON
	requestBody, err := json.Marshal(mcpRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MCP request: %w", err)
	}

	logger.Info("📄 MCP request prepared", "contentLength", len(requestBody), "requestBody", string(requestBody))

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "dot-ai-controller/v1.0.0")

	// Add Authorization header if auth token is configured
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
		logger.V(1).Info("Authorization header set for MCP request")
	}

	logger.Info("🌐 Sending HTTP request", "method", "POST", "endpoint", endpoint)

	// Make the HTTP request
	resp, err := r.HttpClient.Do(req)
	requestDuration := time.Since(startTime)

	if err != nil {
		logger.Error(err, "❌ HTTP request failed", "duration", requestDuration, "error", err.Error())
		return nil, fmt.Errorf("HTTP request failed after %v: %w", requestDuration, err)
	}

	logger.Info("📡 HTTP response received",
		"statusCode", resp.StatusCode,
		"duration", requestDuration,
		"contentType", resp.Header.Get("Content-Type"),
		"contentLength", resp.Header.Get("Content-Length"))

	// Read response body
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error(err, "❌ Failed to read response body", "duration", requestDuration)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	totalDuration := time.Since(startTime)
	logger.Info("📄 Response body read",
		"bodyLength", len(responseBody),
		"totalDuration", totalDuration,
		"responseBody", string(responseBody))

	// Check HTTP status
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		logger.Error(nil, "❌ HTTP error status",
			"statusCode", resp.StatusCode,
			"responseBody", string(responseBody),
			"totalDuration", totalDuration)
		return &McpResponse{
			Success: false,
			Error: &struct {
				Code    string                 `json:"code"`
				Message string                 `json:"message"`
				Details map[string]interface{} `json:"details,omitempty"`
			}{
				Code:    fmt.Sprintf("%d", resp.StatusCode),
				Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(responseBody)),
			},
		}, nil
	}

	// Parse JSON response
	var mcpResponse McpResponse
	if err := json.Unmarshal(responseBody, &mcpResponse); err != nil {
		// If JSON parsing fails, treat as successful but log the raw response
		logger.Info("⚠️ MCP response is not JSON, treating as successful",
			"response", string(responseBody),
			"totalDuration", totalDuration)
		return &McpResponse{
			Success: true,
			Data: &struct {
				Result        map[string]interface{} `json:"result"`
				Tool          string                 `json:"tool"`
				ExecutionTime float64                `json:"executionTime"`
			}{
				Result: map[string]interface{}{
					"message": string(responseBody),
				},
				Tool:          "remediate",
				ExecutionTime: float64(totalDuration.Milliseconds()),
			},
		}, nil
	}

	// Return response
	logger.Info("✅ MCP request completed",
		"success", mcpResponse.Success,
		"message", mcpResponse.GetResultMessage(),
		"error", mcpResponse.GetErrorMessage(),
		"totalDuration", totalDuration)

	return &mcpResponse, nil
}
