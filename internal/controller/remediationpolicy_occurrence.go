// remediationpolicy_occurrence.go contains occurrence threshold logic for the
// RemediationPolicy controller. It tracks how many times a matching event has
// fired within a sliding time window so that transient (single-occurrence)
// failures can be filtered out before triggering remediation.
package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// DefaultOccurrenceWindowSeconds is the default sliding window when an effective
// MinOccurrences is configured but no window is specified.
const DefaultOccurrenceWindowSeconds = 300

// getEffectiveMinOccurrences returns the effective minimum-occurrence threshold
// for a selector. Pointer types let an explicitly-set selector value override
// a non-zero policy default (including with 0 or 1 to disable filtering for
// that selector). Returns 0 (no filtering) when neither is set.
func (r *RemediationPolicyReconciler) getEffectiveMinOccurrences(selector dotaiv1alpha1.EventSelector, policy *dotaiv1alpha1.RemediationPolicy) int {
	if selector.MinOccurrences != nil {
		return *selector.MinOccurrences
	}
	if policy.Spec.MinOccurrences != nil {
		return *policy.Spec.MinOccurrences
	}
	return 0
}

// getEffectiveOccurrenceWindow returns the effective sliding-window duration.
// Selector value wins (when set), then policy default (when set), then
// DefaultOccurrenceWindowSeconds.
func (r *RemediationPolicyReconciler) getEffectiveOccurrenceWindow(selector dotaiv1alpha1.EventSelector, policy *dotaiv1alpha1.RemediationPolicy) time.Duration {
	if selector.OccurrenceWindowSeconds != nil && *selector.OccurrenceWindowSeconds > 0 {
		return time.Duration(*selector.OccurrenceWindowSeconds) * time.Second
	}
	if policy.Spec.OccurrenceWindowSeconds != nil && *policy.Spec.OccurrenceWindowSeconds > 0 {
		return time.Duration(*policy.Spec.OccurrenceWindowSeconds) * time.Second
	}
	return time.Duration(DefaultOccurrenceWindowSeconds) * time.Second
}

// getOccurrenceKey creates a unique key for occurrence tracking. Includes the
// event reason and a hash of the message so that different failure modes for the
// same object are counted independently — a transient probe blip won't mask a
// genuine failure with a different message.
func (r *RemediationPolicyReconciler) getOccurrenceKey(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, event *corev1.Event) string {
	ownerKind, ownerName := r.resolveOwnerForRateLimiting(ctx, event.InvolvedObject)

	var objectIdentifier string
	if ownerKind != "" {
		objectIdentifier = fmt.Sprintf("%s:%s", ownerKind, ownerName)
	} else {
		objectIdentifier = ownerName
	}

	messageHash := hashMessage(event.Message)

	return fmt.Sprintf("%s/%s/%s/%s/%s/%s",
		policy.Namespace, policy.Name,
		event.InvolvedObject.Namespace, objectIdentifier,
		event.Reason, messageHash)
}

// hashMessage returns a short hex digest of the event message used in tracking
// keys. Truncated to 12 chars — collisions across distinct messages would only
// merge unrelated counters, which is acceptable for a best-effort threshold.
func hashMessage(message string) string {
	sum := sha1.Sum([]byte(message))
	return hex.EncodeToString(sum[:])[:12]
}

// recordOccurrenceAndCheckThreshold appends "now" to the occurrence list for the
// given key, prunes entries older than the window, and returns:
//   - allowed: true when the count meets or exceeds minOccurrences (proceed)
//   - count:   the number of occurrences within the current window after recording
func (r *RemediationPolicyReconciler) recordOccurrenceAndCheckThreshold(key string, minOccurrences int, window time.Duration) (allowed bool, count int) {
	now := time.Now()
	cutoff := now.Add(-window)

	r.occurrenceMu.Lock()
	defer r.occurrenceMu.Unlock()

	if r.occurrenceTracking == nil {
		r.occurrenceTracking = make(map[string][]time.Time)
	}

	times := r.occurrenceTracking[key]

	filtered := times[:0:0]
	for _, t := range times {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}

	filtered = append(filtered, now)
	r.occurrenceTracking[key] = filtered

	count = len(filtered)
	allowed = count >= minOccurrences
	return allowed, count
}

// resetOccurrenceCount clears the occurrence list for a key after the threshold
// has been met and remediation triggered. This prevents the next event from
// immediately re-triggering — the counter must rebuild from zero before the
// next remediation is allowed.
func (r *RemediationPolicyReconciler) resetOccurrenceCount(key string) {
	r.occurrenceMu.Lock()
	defer r.occurrenceMu.Unlock()
	if r.occurrenceTracking == nil {
		return
	}
	delete(r.occurrenceTracking, key)
}

// maxOccurrenceWindow returns the largest effective occurrence window configured
// across all active RemediationPolicies, with a small safety slack. Used as the
// cleanup TTL so we never prune in-window entries for any policy. Falls back to
// the default window when no policies are listable or none configure a window.
func (r *RemediationPolicyReconciler) maxOccurrenceWindow(ctx context.Context) time.Duration {
	logger := logf.FromContext(ctx)

	defaultWindow := time.Duration(DefaultOccurrenceWindowSeconds) * time.Second
	maxSeconds := DefaultOccurrenceWindowSeconds

	var policies dotaiv1alpha1.RemediationPolicyList
	if err := r.List(ctx, &policies); err != nil {
		logger.V(1).Info("Failed to list policies for cleanup TTL, using default window",
			"error", err)
		return defaultWindow
	}

	for i := range policies.Items {
		policy := &policies.Items[i]
		if policy.Spec.OccurrenceWindowSeconds != nil && *policy.Spec.OccurrenceWindowSeconds > maxSeconds {
			maxSeconds = *policy.Spec.OccurrenceWindowSeconds
		}
		for _, sel := range policy.Spec.EventSelectors {
			if sel.OccurrenceWindowSeconds != nil && *sel.OccurrenceWindowSeconds > maxSeconds {
				maxSeconds = *sel.OccurrenceWindowSeconds
			}
		}
	}

	// Add 10% slack so cleanup runs slightly after the last valid timestamp would
	// have aged out, avoiding races with concurrent writes.
	return time.Duration(maxSeconds) * time.Second * 11 / 10
}

// cleanupOccurrenceTracking removes expired entries to prevent unbounded growth.
// Called periodically from the policy reconcile loop.
func (r *RemediationPolicyReconciler) cleanupOccurrenceTracking(maxAge time.Duration) {
	r.occurrenceMu.Lock()
	defer r.occurrenceMu.Unlock()

	if r.occurrenceTracking == nil {
		return
	}

	cutoff := time.Now().Add(-maxAge)
	for key, times := range r.occurrenceTracking {
		filtered := times[:0:0]
		for _, t := range times {
			if t.After(cutoff) {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			delete(r.occurrenceTracking, key)
		} else {
			r.occurrenceTracking[key] = filtered
		}
	}
}

// shouldFilterByOccurrence checks whether the event should be suppressed because
// the matching selector requires more occurrences before remediating.
// Returns true if the event should be filtered (i.e., NOT processed).
func (r *RemediationPolicyReconciler) shouldFilterByOccurrence(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, selector dotaiv1alpha1.EventSelector, event *corev1.Event) (filter bool, count int, threshold int, key string) {
	threshold = r.getEffectiveMinOccurrences(selector, policy)
	if threshold <= 1 {
		return false, 0, threshold, ""
	}

	window := r.getEffectiveOccurrenceWindow(selector, policy)
	key = r.getOccurrenceKey(ctx, policy, event)
	allowed, c := r.recordOccurrenceAndCheckThreshold(key, threshold, window)

	if !allowed {
		logger := logf.FromContext(ctx)
		logger.V(1).Info("Event filtered by occurrence threshold",
			"key", key,
			"count", c,
			"required", threshold,
			"windowSeconds", int(window.Seconds()),
		)
		return true, c, threshold, key
	}
	return false, c, threshold, key
}
