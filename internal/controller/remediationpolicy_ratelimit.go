// remediationpolicy_ratelimit.go contains rate limiting and cooldown logic
// for the RemediationPolicy controller. This file handles tracking event
// processing rates and enforcing cooldown periods.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// parseCronJobNameFromPodName attempts to extract a CronJob name from a pod name
// that follows the pattern: {cronjob-name}-{timestamp}-{suffix}
// where timestamp is numeric (Unix timestamp) and suffix is alphanumeric.
// Returns the parsed name and true if successful, empty string and false otherwise.
func parseCronJobNameFromPodName(podName string) (string, bool) {
	segments := strings.Split(podName, "-")

	// Need at least 3 segments: name + timestamp + suffix
	if len(segments) < 3 {
		return "", false
	}

	// Second-to-last segment must be numeric (timestamp)
	timestampSegment := segments[len(segments)-2]
	if len(timestampSegment) == 0 {
		return "", false
	}
	for _, c := range timestampSegment {
		if c < '0' || c > '9' {
			return "", false
		}
	}

	// Last segment is the random suffix (alphanumeric, typically 5 chars)
	suffixSegment := segments[len(segments)-1]
	if len(suffixSegment) == 0 {
		return "", false
	}

	// Join all segments except last two to get CronJob name
	cronJobName := strings.Join(segments[:len(segments)-2], "-")
	if len(cronJobName) == 0 {
		return "", false
	}

	return cronJobName, true
}

// resolveOwnerForRateLimiting resolves the ultimate owner for rate limiting purposes.
// For Pods owned by Jobs/CronJobs, it returns the Job or CronJob name to ensure
// all pods from the same Job/CronJob share the same rate limit key.
//
// Owner resolution chain:
//   - Pod -> Job -> CronJob: returns ("cronjob", cronjob-name)
//   - Pod -> Job (no CronJob): returns ("job", job-name)
//   - Pod (no Job owner): returns ("", pod-name)
//   - Non-Pod resources: returns ("", object-name)
func (r *RemediationPolicyReconciler) resolveOwnerForRateLimiting(ctx context.Context, involvedObject corev1.ObjectReference) (kind string, name string) {
	logger := logf.FromContext(ctx)

	// Default: use original object name with no kind prefix
	name = involvedObject.Name
	kind = ""

	// Only resolve ownership for Pods
	if involvedObject.Kind != "Pod" {
		return kind, name
	}

	// Fetch the Pod to check its ownerReferences
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{
		Namespace: involvedObject.Namespace,
		Name:      involvedObject.Name,
	}

	if err := r.Get(ctx, podKey, pod); err != nil {
		// Only try CronJob name parsing for NotFound errors (deleted pods)
		// Other errors (transient API/RBAC issues) should fall back to original name
		if apierrors.IsNotFound(err) {
			if parsedName, ok := parseCronJobNameFromPodName(involvedObject.Name); ok {
				logger.V(1).Info("Pod not found, parsed CronJob name from pod name pattern",
					"pod", podKey,
					"parsedCronJob", parsedName)
				return "cronjob", parsedName
			}
		}
		// Parsing not applicable or failed - fall back to original name
		logger.V(1).Info("Failed to fetch Pod for owner resolution, using pod name",
			"pod", podKey,
			"error", err)
		return kind, name
	}

	// Look for Job owner
	var jobOwnerRef *metav1.OwnerReference
	for i := range pod.OwnerReferences {
		if pod.OwnerReferences[i].Kind == "Job" {
			jobOwnerRef = &pod.OwnerReferences[i]
			break
		}
	}

	if jobOwnerRef == nil {
		// No Job owner - use pod name
		return kind, name
	}

	// Found Job owner - fetch the Job to check for CronJob owner
	job := &batchv1.Job{}
	jobKey := client.ObjectKey{
		Namespace: involvedObject.Namespace,
		Name:      jobOwnerRef.Name,
	}

	if err := r.Get(ctx, jobKey, job); err != nil {
		// Job not found or error - use Job name as fallback
		logger.V(1).Info("Failed to fetch Job for owner resolution, using job name",
			"job", jobKey,
			"error", err)
		return "job", jobOwnerRef.Name
	}

	// Look for CronJob owner
	for _, ownerRef := range job.OwnerReferences {
		if ownerRef.Kind == "CronJob" {
			// Found CronJob owner - use CronJob name
			logger.V(1).Info("Resolved owner chain for rate limiting",
				"pod", involvedObject.Name,
				"job", job.Name,
				"cronjob", ownerRef.Name)
			return "cronjob", ownerRef.Name
		}
	}

	// Job has no CronJob owner - use Job name
	logger.V(1).Info("Resolved owner for rate limiting",
		"pod", involvedObject.Name,
		"job", job.Name)
	return "job", job.Name
}

// getRateLimitKey creates a unique key for rate limiting tracking.
// The key does NOT include event.Reason, so all events for the same object share
// one rate limit bucket regardless of the specific event reason (e.g., Failed,
// ErrImagePull, ImagePullBackOff all share the same cooldown).
// For Pods owned by Jobs/CronJobs, the key uses the owner name instead of the pod name
// to ensure all pods from the same Job/CronJob share the same rate limit bucket.
func (r *RemediationPolicyReconciler) getRateLimitKey(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) string {
	// Resolve owner for rate limiting (handles Job/CronJob ownership)
	ownerKind, ownerName := r.resolveOwnerForRateLimiting(ctx, event.InvolvedObject)

	// Build the object identifier with optional kind prefix
	var objectIdentifier string
	if ownerKind != "" {
		objectIdentifier = fmt.Sprintf("%s:%s", ownerKind, ownerName)
	} else {
		objectIdentifier = ownerName
	}

	return fmt.Sprintf("%s/%s/%s/%s",
		policy.Namespace, policy.Name,
		event.InvolvedObject.Namespace, objectIdentifier)
}

// isRateLimited checks if processing should be rate limited based on policy configuration
func (r *RemediationPolicyReconciler) isRateLimited(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) (bool, string) {
	if policy.Spec.RateLimiting.EventsPerMinute == 0 && policy.Spec.RateLimiting.CooldownMinutes == 0 {
		// No rate limiting configured
		return false, ""
	}

	key := r.getRateLimitKey(ctx, policy, event)
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
			cooldownEnd := now.Add(time.Duration(policy.Spec.RateLimiting.CooldownMinutes) * time.Minute)
			r.cooldownTracking[key] = cooldownEnd

			// Mark for persistence
			if r.CooldownPersistence != nil {
				r.CooldownPersistence.MarkDirty(key, cooldownEnd)
			}
		}
	}

	return false, ""
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

// getObjectCooldownKey creates a unique key for object-level cooldown tracking.
// Uses the same key format as rate limiting to ensure consistency.
func (r *RemediationPolicyReconciler) getObjectCooldownKey(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) string {
	return r.getRateLimitKey(ctx, policy, event)
}

// isObjectInCooldown checks if an object is currently in cooldown period.
// This provides object-level deduplication independent of rate limiting configuration.
// Returns true if the object is in cooldown and should not be processed.
func (r *RemediationPolicyReconciler) isObjectInCooldown(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) (bool, string) {
	key := r.getObjectCooldownKey(ctx, policy, event)
	now := time.Now()

	r.objectCooldownsMu.RLock()
	defer r.objectCooldownsMu.RUnlock()

	if r.objectCooldowns == nil {
		return false, ""
	}

	if cooldownEnd, exists := r.objectCooldowns[key]; exists && now.Before(cooldownEnd) {
		remaining := cooldownEnd.Sub(now)
		return true, fmt.Sprintf("object cooldown active for %v more", remaining.Round(time.Second))
	}

	return false, ""
}

// setObjectCooldown marks an object as in cooldown after remediation is triggered.
// The cooldown duration is DefaultObjectCooldownMinutes.
func (r *RemediationPolicyReconciler) setObjectCooldown(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) {
	key := r.getObjectCooldownKey(ctx, policy, event)
	cooldownEnd := time.Now().Add(time.Duration(DefaultObjectCooldownMinutes) * time.Minute)

	r.objectCooldownsMu.Lock()
	defer r.objectCooldownsMu.Unlock()

	if r.objectCooldowns == nil {
		r.objectCooldowns = make(map[string]time.Time)
	}

	r.objectCooldowns[key] = cooldownEnd

	logger := logf.FromContext(ctx)
	logger.V(1).Info("Object cooldown set",
		"key", key,
		"cooldownEnd", cooldownEnd,
		"durationMinutes", DefaultObjectCooldownMinutes)
}

// cleanupObjectCooldowns removes expired cooldowns to prevent memory leaks.
func (r *RemediationPolicyReconciler) cleanupObjectCooldowns() {
	r.objectCooldownsMu.Lock()
	defer r.objectCooldownsMu.Unlock()

	if r.objectCooldowns == nil {
		return
	}

	now := time.Now()
	for key, cooldownEnd := range r.objectCooldowns {
		if now.After(cooldownEnd) {
			delete(r.objectCooldowns, key)
		}
	}
}
