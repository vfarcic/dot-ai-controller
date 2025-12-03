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
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"regexp"
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

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=remediationpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=remediationpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch

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

	// Check message pattern
	if selector.Message != "" {
		matched, err := regexp.MatchString(selector.Message, event.Message)
		if err != nil {
			// Invalid regex pattern - log error and treat as non-match
			// This prevents invalid patterns from blocking all events
			logger := logf.FromContext(context.Background())
			logger.Error(err, "Invalid regex pattern in message selector",
				"pattern", selector.Message)
			return false
		}
		if !matched {
			return false
		}
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
	if err := r.sendGoogleChatNotification(ctx, policy, event, "start", mcpRequest, nil); err != nil {
		logger.Error(err, "failed to send Google Chat start notification")
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
	if err := r.sendGoogleChatNotification(ctx, policy, event, "complete", mcpRequest, mcpResponse); err != nil {
		logger.Error(err, "failed to send Google Chat complete notification")
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

	// Validate Google Chat configuration
	if err := r.validateGoogleChatConfiguration(policy); err != nil {
		logger.Error(err, "invalid Google Chat configuration")
		r.Recorder.Eventf(policy, corev1.EventTypeWarning, "InvalidGoogleChatConfiguration",
			"Invalid Google Chat configuration: %v", err)
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
			if rateLimited, reason := r.isRateLimited(ctx, &policy, event); rateLimited {
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
