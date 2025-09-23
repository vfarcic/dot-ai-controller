/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// RemediationPolicyReconciler reconciles a RemediationPolicy object
type RemediationPolicyReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	HttpClient *http.Client

	// processedEvents stores processed event keys to prevent duplicate processing
	// Key format: namespace/name/resourceVersion
	processedEvents   map[string]time.Time
	processedEventsMu sync.RWMutex

	// Rate limiting - tracks policy processing per minute and cooldown periods
	// Key format: policy-namespace/policy-name/involved-object-namespace/involved-object-name/event-reason
	rateLimitTracking map[string][]time.Time // Track processing times per policy+object+reason
	cooldownTracking  map[string]time.Time   // Track cooldown periods per policy+object+reason
	rateLimitMu       sync.RWMutex
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=remediationpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=remediationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=remediationpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// getEventKey creates a unique key for event deduplication
func (r *RemediationPolicyReconciler) getEventKey(event *corev1.Event) string {
	return fmt.Sprintf("%s/%s/%s", event.Namespace, event.Name, event.ResourceVersion)
}

// isEventProcessed checks if an event has already been processed
func (r *RemediationPolicyReconciler) isEventProcessed(eventKey string) bool {
	r.processedEventsMu.RLock()
	defer r.processedEventsMu.RUnlock()
	_, exists := r.processedEvents[eventKey]
	return exists
}

// getRateLimitKey creates a unique key for rate limiting tracking
func (r *RemediationPolicyReconciler) getRateLimitKey(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s",
		policy.Namespace, policy.Name,
		event.InvolvedObject.Namespace, event.InvolvedObject.Name,
		event.Reason)
}

// isRateLimited checks if processing should be rate limited based on policy configuration
func (r *RemediationPolicyReconciler) isRateLimited(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) (bool, string) {
	if policy.Spec.RateLimiting.EventsPerMinute == 0 && policy.Spec.RateLimiting.CooldownMinutes == 0 {
		// No rate limiting configured
		return false, ""
	}

	key := r.getRateLimitKey(policy, event)
	now := time.Now()

	r.rateLimitMu.Lock()
	defer r.rateLimitMu.Unlock()

	// Check cooldown period
	if cooldownEnd, exists := r.cooldownTracking[key]; exists && now.Before(cooldownEnd) {
		remaining := cooldownEnd.Sub(now)
		return true, fmt.Sprintf("cooldown active for %v more", remaining.Round(time.Second))
	}

	// Check events per minute limit
	if policy.Spec.RateLimiting.EventsPerMinute > 0 {
		// Initialize tracking if needed
		if r.rateLimitTracking == nil {
			r.rateLimitTracking = make(map[string][]time.Time)
		}

		// Get or create processing times for this key
		times, exists := r.rateLimitTracking[key]
		if !exists {
			times = []time.Time{}
		}

		// Remove times older than 1 minute
		oneMinuteAgo := now.Add(-time.Minute)
		filteredTimes := make([]time.Time, 0)
		for _, t := range times {
			if t.After(oneMinuteAgo) {
				filteredTimes = append(filteredTimes, t)
			}
		}

		// Check if we've exceeded the limit
		if len(filteredTimes) >= policy.Spec.RateLimiting.EventsPerMinute {
			return true, fmt.Sprintf("rate limit exceeded: %d/%d events in last minute",
				len(filteredTimes), policy.Spec.RateLimiting.EventsPerMinute)
		}

		// Update tracking
		filteredTimes = append(filteredTimes, now)
		r.rateLimitTracking[key] = filteredTimes

		// Set cooldown if configured
		if policy.Spec.RateLimiting.CooldownMinutes > 0 {
			if r.cooldownTracking == nil {
				r.cooldownTracking = make(map[string]time.Time)
			}
			r.cooldownTracking[key] = now.Add(time.Duration(policy.Spec.RateLimiting.CooldownMinutes) * time.Minute)
		}
	}

	return false, ""
}

// markEventProcessed marks an event as processed
func (r *RemediationPolicyReconciler) markEventProcessed(eventKey string) {
	r.processedEventsMu.Lock()
	defer r.processedEventsMu.Unlock()
	if r.processedEvents == nil {
		r.processedEvents = make(map[string]time.Time)
	}
	r.processedEvents[eventKey] = time.Now()
}

// cleanupProcessedEvents removes old processed events to prevent memory leaks
func (r *RemediationPolicyReconciler) cleanupProcessedEvents(maxAge time.Duration) {
	r.processedEventsMu.Lock()
	defer r.processedEventsMu.Unlock()

	if r.processedEvents == nil {
		return
	}

	now := time.Now()
	for key, processedAt := range r.processedEvents {
		if now.Sub(processedAt) > maxAge {
			delete(r.processedEvents, key)
		}
	}
}

// matchesPolicy checks if an event matches a RemediationPolicy's selectors
func (r *RemediationPolicyReconciler) matchesPolicy(event *corev1.Event, policy *dotaiv1alpha1.RemediationPolicy) bool {
	for _, selector := range policy.Spec.EventSelectors {
		if r.matchesSelector(event, selector) {
			return true
		}
	}
	return false
}

// matchesPolicyWithSelector checks if an event matches a RemediationPolicy's selectors and returns the matching selector
func (r *RemediationPolicyReconciler) matchesPolicyWithSelector(event *corev1.Event, policy *dotaiv1alpha1.RemediationPolicy) (bool, dotaiv1alpha1.EventSelector) {
	for _, selector := range policy.Spec.EventSelectors {
		if r.matchesSelector(event, selector) {
			return true, selector
		}
	}
	return false, dotaiv1alpha1.EventSelector{}
}

// getEffectiveMode returns the effective mode for a selector, using selector-level mode if specified, otherwise policy-level mode
func (r *RemediationPolicyReconciler) getEffectiveMode(selector dotaiv1alpha1.EventSelector, policy *dotaiv1alpha1.RemediationPolicy) string {
	if selector.Mode != "" {
		return selector.Mode
	}
	if policy.Spec.Mode != "" {
		return policy.Spec.Mode
	}
	return "manual" // Safe default
}

// getEffectiveConfidenceThreshold returns the effective confidence threshold for a selector
func (r *RemediationPolicyReconciler) getEffectiveConfidenceThreshold(selector dotaiv1alpha1.EventSelector, policy *dotaiv1alpha1.RemediationPolicy) float64 {
	if selector.ConfidenceThreshold != nil {
		return *selector.ConfidenceThreshold
	}
	if policy.Spec.ConfidenceThreshold != nil {
		return *policy.Spec.ConfidenceThreshold
	}
	return 0.8 // OpenAPI default
}

// getEffectiveMaxRiskLevel returns the effective max risk level for a selector
func (r *RemediationPolicyReconciler) getEffectiveMaxRiskLevel(selector dotaiv1alpha1.EventSelector, policy *dotaiv1alpha1.RemediationPolicy) string {
	if selector.MaxRiskLevel != "" {
		return selector.MaxRiskLevel
	}
	if policy.Spec.MaxRiskLevel != "" {
		return policy.Spec.MaxRiskLevel
	}
	return "low" // OpenAPI default
}

// generateIssueDescription creates a human-readable issue description from the event
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

// sendMcpRequest sends MCP request to the specified endpoint (single attempt, no retries)
func (r *RemediationPolicyReconciler) sendMcpRequest(ctx context.Context, mcpRequest *dotaiv1alpha1.McpRequest, endpoint string) (*McpResponse, error) {
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
	req.Header.Set("User-Agent", "dot-ai-controller/v1.0.0")

	logger.Info("🌐 Sending HTTP request", "method", "POST", "headers", req.Header)

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
	responseBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
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

// matchesSelector checks if an event matches a specific EventSelector
func (r *RemediationPolicyReconciler) matchesSelector(event *corev1.Event, selector dotaiv1alpha1.EventSelector) bool {
	// Check event type
	if selector.Type != "" && event.Type != selector.Type {
		return false
	}

	// Check event reason
	if selector.Reason != "" && event.Reason != selector.Reason {
		return false
	}

	// Check involved object kind
	if selector.InvolvedObjectKind != "" && event.InvolvedObject.Kind != selector.InvolvedObjectKind {
		return false
	}

	// Check namespace
	if selector.Namespace != "" && event.Namespace != selector.Namespace {
		return false
	}

	return true
}

// processEvent handles processing of a single event
func (r *RemediationPolicyReconciler) processEvent(ctx context.Context, event *corev1.Event, policy *dotaiv1alpha1.RemediationPolicy, selector dotaiv1alpha1.EventSelector) error {
	effectiveMode := r.getEffectiveMode(selector, policy)
	logger := logf.FromContext(ctx).WithValues(
		"event", fmt.Sprintf("%s/%s", event.Namespace, event.Name),
		"policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name),
		"eventType", event.Type,
		"eventReason", event.Reason,
		"involvedObject", fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
		"effectiveMode", effectiveMode,
	)

	logger.Info("Processing event that matches policy",
		"eventMessage", event.Message,
		"firstTimestamp", event.FirstTimestamp,
		"lastTimestamp", event.LastTimestamp,
		"effectiveMode", effectiveMode,
	)

	// Generate Kubernetes Event for policy activity
	r.Recorder.Eventf(policy, corev1.EventTypeNormal, "EventMatched",
		"Policy '%s' matched %s/%s event for %s %s: %s",
		policy.Name, event.Type, event.Reason,
		event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Message)

	// MILESTONE 4A: Generate MCP request message
	mcpRequest, err := r.generateAndLogMcpRequest(ctx, event, policy, selector)
	if err != nil {
		logger.Error(err, "failed to generate MCP request")
		// Generate error event
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "McpMessageGenerationFailed",
			"Failed to generate MCP request: %v", err)
		// Update policy status with failure
		if statusErr := r.updatePolicyStatus(ctx, policy, false, true); statusErr != nil {
			logger.Error(statusErr, "failed to update policy status after MCP generation failure")
		}
		return err
	}

	logger.Info("📝 MCP request generated successfully",
		"issue", mcpRequest.Issue,
		"mode", mcpRequest.Mode,
	)

	// MILESTONE 4C: Send optional "start" notification
	if err := r.sendSlackNotification(ctx, policy, event, "start", mcpRequest, nil); err != nil {
		logger.Error(err, "failed to send Slack start notification")
		// Don't fail the entire process for notification errors, just log and continue
	}

	// MILESTONE 4B: Send HTTP request to MCP endpoint
	mcpResponse, err := r.sendMcpRequest(ctx, mcpRequest, policy.Spec.McpEndpoint)
	if err != nil {
		logger.Error(err, "failed to send MCP request")
		// Generate error event
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "McpRequestFailed",
			"Failed to send MCP request to %s: %v", policy.Spec.McpEndpoint, err)
		// Update policy status with failure
		if statusErr := r.updatePolicyStatus(ctx, policy, false, false); statusErr != nil {
			logger.Error(statusErr, "failed to update policy status after MCP request failure")
		}
		return err
	}

	// Check MCP response
	var mcpSuccess bool
	if mcpResponse.Success {
		mcpSuccess = true
		logger.Info("🎉 MCP request successful",
			"endpoint", policy.Spec.McpEndpoint,
			"response", mcpResponse.GetResultMessage())
		// Generate success event
		r.Recorder.Eventf(policy, corev1.EventTypeNormal, "McpRequestSucceeded",
			"MCP remediation succeeded for %s/%s event: %s",
			event.Type, event.Reason, mcpResponse.GetResultMessage())
	} else {
		mcpSuccess = false
		logger.Error(fmt.Errorf("MCP request failed: %s", mcpResponse.GetErrorMessage()), "MCP returned failure")
		// Generate error event
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "McpRemediationFailed",
			"MCP remediation failed: %s", mcpResponse.GetErrorMessage())
	}

	// Update policy status with MCP result
	if err := r.updatePolicyStatus(ctx, policy, mcpSuccess, true); err != nil {
		logger.Error(err, "failed to update policy status")
		// Generate error event
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "StatusUpdateFailed",
			"Failed to update status after processing event: %v", err)
		return err
	}

	// MILESTONE 4C: Send mandatory "complete" notification
	if err := r.sendSlackNotification(ctx, policy, event, "complete", mcpRequest, mcpResponse); err != nil {
		logger.Error(err, "failed to send Slack complete notification")
		// Don't fail the entire process for notification errors, just log and continue
	}

	// Log final success
	if mcpSuccess {
		logger.Info("✅ Event processed successfully - MCP request sent and remediation successful")
	} else {
		logger.Info("⚠️ Event processed but MCP remediation failed - status updated")
	}
	return nil
}

// updatePolicyStatus updates the RemediationPolicy status with processing statistics with retry logic
func (r *RemediationPolicyReconciler) updatePolicyStatus(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, success bool, mcpMessageGenerated ...bool) error {
	// Retry configuration for status updates
	maxRetries := 3
	baseDelay := 100 * time.Millisecond
	maxDelay := 1 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
			if delay > maxDelay {
				delay = maxDelay
			}
			// Add jitter (±25%)
			jitter := time.Duration(float64(delay) * 0.25 * (2*rand.Float64() - 1))
			delay += jitter

			time.Sleep(delay)
		}

		// Fetch fresh copy to avoid conflicts
		fresh := &dotaiv1alpha1.RemediationPolicy{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(policy), fresh); err != nil {
			lastErr = fmt.Errorf("failed to fetch fresh policy: %w", err)
			continue
		}

		// Update statistics
		fresh.Status.TotalEventsProcessed++
		if success {
			fresh.Status.SuccessfulRemediations++
		} else {
			fresh.Status.FailedRemediations++
		}

		// Update MCP message generation statistics if specified
		if len(mcpMessageGenerated) > 0 && mcpMessageGenerated[0] {
			fresh.Status.TotalMcpMessagesGenerated++
			now := metav1.NewTime(time.Now())
			fresh.Status.LastMcpMessageGenerated = &now
		}

		// Update last processed event timestamp
		now := metav1.NewTime(time.Now())
		fresh.Status.LastProcessedEvent = &now

		// Update conditions
		var conditionMessage string
		if fresh.Status.TotalMcpMessagesGenerated > 0 {
			conditionMessage = fmt.Sprintf("Successfully processed %d events and generated %d MCP messages",
				fresh.Status.TotalEventsProcessed, fresh.Status.TotalMcpMessagesGenerated)
		} else {
			conditionMessage = fmt.Sprintf("Successfully processed %d events", fresh.Status.TotalEventsProcessed)
		}

		readyCondition := metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "EventProcessed",
			Message:            conditionMessage,
		}

		// Find and update existing Ready condition or append new one
		updated := false
		for i, condition := range fresh.Status.Conditions {
			if condition.Type == "Ready" {
				fresh.Status.Conditions[i] = readyCondition
				updated = true
				break
			}
		}
		if !updated {
			fresh.Status.Conditions = append(fresh.Status.Conditions, readyCondition)
		}

		// Update status subresource with retry logic
		if err := r.Status().Update(ctx, fresh); err != nil {
			if apierrors.IsConflict(err) {
				// Resource version conflict - retry with fresh copy
				lastErr = fmt.Errorf("resource conflict (attempt %d/%d): %w", attempt+1, maxRetries+1, err)
				continue
			}
			// Non-conflict error - don't retry
			return fmt.Errorf("failed to update policy status: %w", err)
		}

		// Success!
		return nil
	}

	// All retries exhausted
	return fmt.Errorf("failed to update policy status after %d attempts: %w", maxRetries+1, lastErr)
}

// updateRateLimitStatus updates the RemediationPolicy status with rate limiting statistics
func (r *RemediationPolicyReconciler) updateRateLimitStatus(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy) error {
	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.RemediationPolicy{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(policy), fresh); err != nil {
		return fmt.Errorf("failed to fetch fresh policy: %w", err)
	}

	// Update rate limiting statistics
	fresh.Status.RateLimitedEvents++
	now := metav1.NewTime(time.Now())
	fresh.Status.LastRateLimitedEvent = &now

	// Update status subresource
	if err := r.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("failed to update rate limit status: %w", err)
	}

	return nil
}

// initializePolicyStatus initializes the status for a new RemediationPolicy
func (r *RemediationPolicyReconciler) initializePolicyStatus(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy) error {
	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.RemediationPolicy{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(policy), fresh); err != nil {
		return fmt.Errorf("failed to fetch fresh policy: %w", err)
	}

	// Initialize status fields
	fresh.Status.TotalEventsProcessed = 0
	fresh.Status.SuccessfulRemediations = 0
	fresh.Status.FailedRemediations = 0
	fresh.Status.TotalMcpMessagesGenerated = 0
	fresh.Status.LastProcessedEvent = nil
	fresh.Status.LastMcpMessageGenerated = nil
	fresh.Status.RateLimitedEvents = 0
	fresh.Status.LastRateLimitedEvent = nil

	// Set initial Ready condition
	now := metav1.NewTime(time.Now())
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "PolicyInitialized",
		Message:            fmt.Sprintf("Policy ready to process events with %d selectors", len(fresh.Spec.EventSelectors)),
	}

	fresh.Status.Conditions = []metav1.Condition{readyCondition}

	// Update status subresource
	if err := r.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("failed to initialize policy status: %w", err)
	}

	return nil
}

// Reconcile processes both Events and RemediationPolicies.
// For Events: checks them against RemediationPolicy filters and processes matches.
// For RemediationPolicies: initializes/updates their status and conditions.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *RemediationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Try to fetch as Event first
	var event corev1.Event
	if err := r.Get(ctx, req.NamespacedName, &event); err == nil {
		// This is an Event - process it
		return r.reconcileEvent(ctx, &event)
	}

	// Try to fetch as RemediationPolicy
	var policy dotaiv1alpha1.RemediationPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err == nil {
		// This is a RemediationPolicy - initialize/update its status
		return r.reconcilePolicy(ctx, &policy)
	}

	// Neither found - resource was probably deleted
	logger.V(1).Info("Resource not found - likely deleted", "resource", req.NamespacedName)
	return ctrl.Result{}, nil
}

// reconcilePolicy handles RemediationPolicy creation and status management
func (r *RemediationPolicyReconciler) reconcilePolicy(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name))

	logger.Info("Reconciling RemediationPolicy",
		"eventSelectors", len(policy.Spec.EventSelectors),
		"mcpEndpoint", policy.Spec.McpEndpoint,
		"mode", policy.Spec.Mode,
	)

	// Validate Slack configuration
	if err := r.validateSlackConfiguration(policy); err != nil {
		logger.Error(err, "invalid Slack configuration")
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "InvalidSlackConfiguration",
			"Invalid Slack configuration: %v", err)
		return ctrl.Result{}, err
	}

	// Initialize status if this is a new policy (no status yet)
	needsStatusUpdate := false
	if policy.Status.TotalEventsProcessed == 0 && policy.Status.LastProcessedEvent == nil && len(policy.Status.Conditions) == 0 {
		needsStatusUpdate = true
		logger.Info("Initializing status for new RemediationPolicy")

		// Generate Kubernetes Event for policy creation
		r.Recorder.Eventf(policy, corev1.EventTypeNormal, "PolicyCreated",
			"RemediationPolicy '%s' created and ready to process events with %d selectors",
			policy.Name, len(policy.Spec.EventSelectors))
	}

	// Update status if needed
	if needsStatusUpdate {
		if err := r.initializePolicyStatus(ctx, policy); err != nil {
			logger.Error(err, "failed to initialize policy status")
			// Generate error event
			r.Recorder.Eventf(policy, corev1.EventTypeWarning, "StatusInitializationFailed",
				"Failed to initialize status: %v", err)
			return ctrl.Result{}, err
		}
		logger.Info("✅ RemediationPolicy status initialized successfully")
	}

	// Periodic cleanup of processed events cache
	r.cleanupProcessedEvents(10 * time.Minute)

	// Requeue periodically to perform maintenance
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// reconcileEvent handles Event processing against policies
func (r *RemediationPolicyReconciler) reconcileEvent(ctx context.Context, event *corev1.Event) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues(
		"event", fmt.Sprintf("%s/%s", event.Namespace, event.Name),
		"eventType", event.Type,
		"eventReason", event.Reason,
	)

	// Check if we've already processed this event
	eventKey := r.getEventKey(event)
	if r.isEventProcessed(eventKey) {
		logger.V(1).Info("Event already processed, skipping")
		return ctrl.Result{}, nil
	}

	logger.Info("Processing Kubernetes event",
		"involvedObject", fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
		"message", event.Message,
	)

	// Get all active RemediationPolicies to check for matches
	var policies dotaiv1alpha1.RemediationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		logger.Error(err, "failed to list RemediationPolicies")
		return ctrl.Result{}, err
	}

	if len(policies.Items) == 0 {
		logger.Info("No RemediationPolicies found - event will not be processed")
		return ctrl.Result{}, nil
	}

	// Check if event matches any policy
	matched := false
	for _, policy := range policies.Items {
		if matches, matchingSelector := r.matchesPolicyWithSelector(event, &policy); matches {
			effectiveMode := r.getEffectiveMode(matchingSelector, &policy)

			// Check rate limiting before processing
			if rateLimited, reason := r.isRateLimited(&policy, event); rateLimited {
				logger.Info("Event processing rate limited",
					"policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name),
					"reason", reason,
					"eventsPerMinute", policy.Spec.RateLimiting.EventsPerMinute,
					"cooldownMinutes", policy.Spec.RateLimiting.CooldownMinutes,
				)

				// Update policy status with rate limiting statistics
				if err := r.updateRateLimitStatus(ctx, &policy); err != nil {
					logger.Error(err, "failed to update rate limit status")
				}

				// Mark as matched but skip processing
				matched = true
				break
			}

			logger.Info("🎯 Event MATCHES RemediationPolicy!",
				"policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name),
				"matchedSelectors", len(policy.Spec.EventSelectors),
				"effectiveMode", effectiveMode,
			)

			// Mark event as processed before processing to avoid duplicates
			r.markEventProcessed(eventKey)
			matched = true

			// Process the event
			if err := r.processEvent(ctx, event, &policy, matchingSelector); err != nil {
				logger.Error(err, "failed to process event",
					"policy", fmt.Sprintf("%s/%s", policy.Namespace, policy.Name))
				return ctrl.Result{}, err
			}

			// Process for the first matching policy only to avoid duplicate processing
			break
		}
	}

	if !matched {
		logger.Info("Event does not match any RemediationPolicy selectors",
			"availablePolicies", len(policies.Items),
		)
	}

	return ctrl.Result{}, nil
}

// SlackMessage represents the structure of a Slack webhook message
type SlackMessage struct {
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	IconEmoji   string            `json:"icon_emoji,omitempty"`
	Attachments []SlackAttachment `json:"attachments"`
}

// SlackAttachment represents a Slack message attachment for rich formatting
type SlackAttachment struct {
	Color     string       `json:"color"`
	Title     string       `json:"title"`
	Text      string       `json:"text"`
	Fields    []SlackField `json:"fields"`
	Footer    string       `json:"footer"`
	Timestamp int64        `json:"ts"`
}

// SlackField represents a field in a Slack attachment
type SlackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// validateSlackConfiguration validates Slack notification settings
func (r *RemediationPolicyReconciler) validateSlackConfiguration(policy *dotaiv1alpha1.RemediationPolicy) error {
	slack := policy.Spec.Notifications.Slack

	// If Slack is disabled, no validation needed
	if !slack.Enabled {
		return nil
	}

	// If enabled, webhook URL is required
	if slack.WebhookUrl == "" {
		return fmt.Errorf("Slack webhook URL is required when notifications are enabled")
	}

	// Basic URL format validation
	if !strings.HasPrefix(slack.WebhookUrl, "https://hooks.slack.com/") {
		return fmt.Errorf("invalid Slack webhook URL format - must start with https://hooks.slack.com/")
	}

	return nil
}

// sendSlackNotification sends a notification to Slack if configured
func (r *RemediationPolicyReconciler) sendSlackNotification(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) error {
	logger := logf.FromContext(ctx)

	// Check if Slack notifications are enabled
	if !policy.Spec.Notifications.Slack.Enabled {
		logger.V(1).Info("Slack notifications disabled, skipping")
		return nil
	}

	// Check if webhook URL is configured
	if policy.Spec.Notifications.Slack.WebhookUrl == "" {
		logger.V(1).Info("Slack webhook URL not configured, skipping notification")
		return nil
	}

	// Check notification type against policy configuration
	if notificationType == "start" && !policy.Spec.Notifications.Slack.NotifyOnStart {
		logger.V(1).Info("Start notifications disabled, skipping")
		return nil
	}
	if notificationType == "complete" && !policy.Spec.Notifications.Slack.NotifyOnComplete {
		logger.V(1).Info("Complete notifications disabled, skipping")
		return nil
	}

	// Create Slack message
	message := r.createSlackMessage(policy, event, notificationType, mcpRequest, mcpResponse)

	// Send the message
	if err := r.sendSlackWebhook(ctx, policy.Spec.Notifications.Slack.WebhookUrl, message); err != nil {
		logger.Error(err, "failed to send Slack notification",
			"notificationType", notificationType,
			"webhookUrl", policy.Spec.Notifications.Slack.WebhookUrl)
		return err
	}

	logger.Info("📱 Slack notification sent successfully",
		"notificationType", notificationType,
		"channel", policy.Spec.Notifications.Slack.Channel)
	return nil
}

// createSlackMessage creates a formatted Slack message
func (r *RemediationPolicyReconciler) createSlackMessage(policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event, notificationType string, mcpRequest *dotaiv1alpha1.McpRequest, mcpResponse *McpResponse) SlackMessage {
	var color, title, text string
	var fields []SlackField

	switch notificationType {
	case "start":
		color = "warning"
		title = "🔄 Remediation Started"
		text = fmt.Sprintf("Started processing event: %s", mcpRequest.Issue)
		fields = []SlackField{
			{Title: "Event Type", Value: fmt.Sprintf("%s/%s", event.Type, event.Reason), Short: true},
			{Title: "Resource", Value: fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name), Short: true},
			{Title: "Namespace", Value: event.InvolvedObject.Namespace, Short: true},
			{Title: "Mode", Value: mcpRequest.Mode, Short: true},
			{Title: "Policy", Value: policy.Name, Short: false},
		}

	case "complete":
		if mcpResponse != nil && mcpResponse.Success {
			// Check if MCP actually executed commands vs just provided recommendations
			executed := r.getMcpExecutedStatus(mcpResponse)
			if executed {
				color = "good"
				title = "✅ Remediation Completed Successfully"
				text = fmt.Sprintf("Issue resolved: %s", mcpResponse.GetResultMessage())
			} else {
				color = "#3AA3E3"
				title = "📋 Analysis Completed - Manual Action Required"
				text = fmt.Sprintf("Analysis completed: %s", mcpResponse.GetResultMessage())
			}
		} else {
			color = "danger"
			title = "❌ Remediation Failed"
			if mcpResponse != nil {
				text = fmt.Sprintf("Remediation failed: %s", mcpResponse.GetErrorMessage())
			} else {
				text = "Remediation failed with unknown error"
			}
		}

		fields = []SlackField{
			{Title: "Original Issue", Value: mcpRequest.Issue, Short: false},
			{Title: "Event Type", Value: fmt.Sprintf("%s/%s", event.Type, event.Reason), Short: true},
			{Title: "Resource", Value: fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name), Short: true},
			{Title: "Namespace", Value: event.InvolvedObject.Namespace, Short: true},
			{Title: "Mode", Value: mcpRequest.Mode, Short: true},
			{Title: "Policy", Value: policy.Name, Short: false},
		}

		// Add detailed MCP response information for completion notifications
		if mcpResponse != nil {
			r.addMcpDetailFields(&fields, mcpResponse)
		}
	}

	attachment := SlackAttachment{
		Color:     color,
		Title:     title,
		Text:      text,
		Fields:    fields,
		Footer:    "dot-ai Kubernetes Event Controller",
		Timestamp: time.Now().Unix(),
	}

	message := SlackMessage{
		Username:    "dot-ai-controller",
		IconEmoji:   ":robot_face:",
		Attachments: []SlackAttachment{attachment},
	}

	// Set channel if configured
	if policy.Spec.Notifications.Slack.Channel != "" {
		message.Channel = policy.Spec.Notifications.Slack.Channel
	}

	return message
}

// getMcpExecutedStatus checks if MCP actually executed commands or just provided recommendations
func (r *RemediationPolicyReconciler) getMcpExecutedStatus(mcpResponse *McpResponse) bool {
	if mcpResponse.Data != nil && mcpResponse.Data.Result != nil {
		if executed, ok := mcpResponse.Data.Result["executed"].(bool); ok {
			return executed
		}
	}
	return false
}

// addMcpDetailFields extracts detailed information from MCP response and adds to Slack fields
func (r *RemediationPolicyReconciler) addMcpDetailFields(fields *[]SlackField, mcpResponse *McpResponse) {
	executed := r.getMcpExecutedStatus(mcpResponse)
	if mcpResponse.Data != nil && mcpResponse.Data.Result != nil {
		result := mcpResponse.Data.Result

		// Add execution time if available
		if mcpResponse.Data.ExecutionTime > 0 {
			executionTimeSeconds := mcpResponse.Data.ExecutionTime / 1000
			*fields = append(*fields, SlackField{
				Title: "Execution Time",
				Value: fmt.Sprintf("%.2fs", executionTimeSeconds),
				Short: true,
			})
		}

		// Extract confidence level if available
		if confidence, ok := result["confidence"].(float64); ok {
			*fields = append(*fields, SlackField{
				Title: "Confidence",
				Value: fmt.Sprintf("%.0f%%", confidence*100),
				Short: true,
			})
		}

		// Extract analysis information
		if analysis, ok := result["analysis"].(map[string]interface{}); ok {
			if rootCause, ok := analysis["rootCause"].(string); ok && rootCause != "" {
				*fields = append(*fields, SlackField{
					Title: "Root Cause",
					Value: rootCause,
					Short: false,
				})
			}
			if confLevel, ok := analysis["confidence"].(float64); ok {
				*fields = append(*fields, SlackField{
					Title: "Analysis Confidence",
					Value: fmt.Sprintf("%.0f%%", confLevel*100),
					Short: true,
				})
			}
		}

		// Extract remediation actions/commands if available
		if remediation, ok := result["remediation"].(map[string]interface{}); ok {
			if actions, ok := remediation["actions"].([]interface{}); ok && len(actions) > 0 {
				var commands []string
				for _, action := range actions {
					if actionMap, ok := action.(map[string]interface{}); ok {
						if cmd, ok := actionMap["command"].(string); ok && cmd != "" {
							// Truncate very long commands for Slack display
							if len(cmd) > 200 {
								cmd = cmd[:200] + "..."
							}
							commands = append(commands, fmt.Sprintf("• %s", cmd))
						}
					}
				}
				if len(commands) > 0 {
					commandsTitle := "Commands Executed"
					if !executed {
						commandsTitle = "Recommended Commands"
					}
					*fields = append(*fields, SlackField{
						Title: commandsTitle,
						Value: strings.Join(commands, "\n"),
						Short: false,
					})
				}
			}
		}

		// Extract validation results if available
		if validation, ok := result["validation"].(map[string]interface{}); ok {
			if success, ok := validation["success"].(bool); ok {
				status := "❌ Failed"
				if success {
					status = "✅ Passed"
				}
				*fields = append(*fields, SlackField{
					Title: "Validation",
					Value: status,
					Short: true,
				})
			}
		}

		// Extract action count/results summary
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			*fields = append(*fields, SlackField{
				Title: "Actions Taken",
				Value: fmt.Sprintf("%d remediation actions", len(results)),
				Short: true,
			})

			// Extract first result's action and output for details
			if firstResult, ok := results[0].(map[string]interface{}); ok {
				if action, ok := firstResult["action"].(string); ok && action != "" {
					// Truncate for Slack display
					if len(action) > 300 {
						action = action[:300] + "..."
					}
					*fields = append(*fields, SlackField{
						Title: "Primary Action",
						Value: action,
						Short: false,
					})
				}
			}
		}
	}

	// Add error details for failed responses
	if !mcpResponse.Success && mcpResponse.Error != nil {
		if mcpResponse.Error.Code != "" {
			*fields = append(*fields, SlackField{
				Title: "Error Code",
				Value: mcpResponse.Error.Code,
				Short: true,
			})
		}
		if mcpResponse.Error.Details != nil {
			if reason, ok := mcpResponse.Error.Details["reason"].(string); ok && reason != "" {
				*fields = append(*fields, SlackField{
					Title: "Error Details",
					Value: reason,
					Short: false,
				})
			}
		}
	}
}

// sendSlackWebhook sends the actual HTTP request to Slack webhook
func (r *RemediationPolicyReconciler) sendSlackWebhook(ctx context.Context, webhookUrl string, message SlackMessage) error {
	logger := logf.FromContext(ctx)

	// Marshal message to JSON
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal Slack message: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", webhookUrl, bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to create Slack webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request with timeout
	response, err := r.HttpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send Slack webhook: %w", err)
	}
	defer response.Body.Close()

	// Check response
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("Slack webhook returned status %d: %s", response.StatusCode, string(body))
	}

	logger.V(1).Info("Slack webhook sent successfully",
		"statusCode", response.StatusCode,
		"payloadSize", len(payload))

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RemediationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Event{}).
		Watches(
			&dotaiv1alpha1.RemediationPolicy{},
			&handler.EnqueueRequestForObject{},
		).
		Named("remediationpolicy").
		Complete(r)
}
