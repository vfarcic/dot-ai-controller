# PRD: Event Filtering Improvements for RemediationPolicy

**GitHub Issue**: #26
**Status**: Complete
**Priority**: High
**Created**: 2025-12-07

## Problem Statement

The RemediationPolicy controller has significant limitations that affect usability:

**Notification storms on restart**: When the controller restarts, it reprocesses all existing events in the cluster because the in-memory `processedEvents` map is lost. This causes a flood of notifications for historical events that may no longer be relevant.

**Notifications for deleted resources**: After a resource is deleted, the controller continues to send notifications for that resource. This happens because:
1. Kubernetes events persist after the involved resource is deleted (until TTL-based garbage collection, ~1 hour)
2. The object cooldown expires after 5 minutes
3. Events get re-processed because their `resourceVersion` changes (count/timestamp updates)
4. The controller never validates that the involved object still exists

## Solution Overview

### Automatic Startup Time Filtering

The controller automatically ignores events that occurred before it started:

- Record controller startup time on initialization
- Filter events where `lastTimestamp < startupTime`
- No configuration needed - automatic behavior
- Handles both new events and recurring events (checks `lastTimestamp`, not `creationTimestamp`)
- Falls back to `eventTime` for series-based events when `lastTimestamp` is not set

### Skip Events for Deleted Resources

The controller validates that the involved object still exists before processing:

- Check if the resource referenced by `event.InvolvedObject` exists in the cluster
- Use unstructured client to support any resource type (leverages existing `*/*` RBAC permissions)
- Skip events with `NotFound` response (resource deleted)
- Continue processing on transient API errors (to avoid missing events)

## User Stories

1. **As a cluster operator**, I want the controller to only process events that occur after it starts, so I don't get flooded with notifications for old events after a restart.

2. **As a cluster operator**, I want the controller to stop sending notifications for resources I've already deleted, so I don't receive repeated alerts for issues I've already addressed.

## Technical Design

### Controller Changes

Update `RemediationPolicyReconciler` in `internal/controller/remediationpolicy_controller.go`:

1. Add `startupTime time.Time` field to reconciler struct
2. Initialize `startupTime` in `SetupWithManager`
3. Add startup time check in `reconcileEvent` before any processing

### Implementation

```go
// In RemediationPolicyReconciler struct
startupTime time.Time

// In SetupWithManager
r.startupTime = time.Now()

// In reconcileEvent - filter historical events
if !r.startupTime.IsZero() {
    eventTime := event.LastTimestamp.Time
    // For events without lastTimestamp, fall back to eventTime (series-based events)
    if eventTime.IsZero() && !event.EventTime.IsZero() {
        eventTime = event.EventTime.Time
    }
    if !eventTime.IsZero() && eventTime.Before(r.startupTime) {
        logger.V(1).Info("Ignoring historical event (occurred before controller startup)")
        return ctrl.Result{}, nil
    }
}
```

### Deleted Resource Check

Add `involvedObjectExists` function using unstructured client:

```go
// involvedObjectExists checks if the object referenced by the event still exists.
// Returns true if object exists or on API errors (to avoid missing events).
// Returns false only on NotFound (confirming deletion).
func (r *RemediationPolicyReconciler) involvedObjectExists(ctx context.Context, event *corev1.Event) bool {
    involvedObj := event.InvolvedObject

    gv, err := schema.ParseGroupVersion(involvedObj.APIVersion)
    if err != nil {
        return true // Assume exists on parse error
    }

    obj := &unstructured.Unstructured{}
    obj.SetGroupVersionKind(schema.GroupVersionKind{
        Group:   gv.Group,
        Version: gv.Version,
        Kind:    involvedObj.Kind,
    })

    objKey := client.ObjectKey{
        Namespace: involvedObj.Namespace,
        Name:      involvedObj.Name,
    }

    if err := r.Get(ctx, objKey, obj); err != nil {
        if apierrors.IsNotFound(err) {
            return false // Resource deleted
        }
        return true // Assume exists on other errors
    }
    return true
}

// In reconcileEvent - after startup time check
if !r.involvedObjectExists(ctx, event) {
    return ctrl.Result{}, nil
}
```

## Success Criteria

1. **Startup filtering works**: After controller restart, no events with `lastTimestamp` before startup are processed
2. **Deleted resource filtering works**: Events for resources that no longer exist are skipped
3. **Existing behavior preserved**: Positive matching logic unchanged; existing configurations work without modification
4. **Tests pass**: Unit tests cover startup time filtering and deleted resource check functionality

## Milestones

- [x] **Milestone 1: Startup time filtering**
  - Add `startupTime` field to reconciler
  - Initialize on controller startup
  - Filter events in `reconcileEvent`
  - Unit tests for startup filtering

- [x] **Milestone 2: Skip events for deleted resources**
  - Add `involvedObjectExists` function using unstructured client
  - Parse `APIVersion` to construct proper GVK for any resource type
  - Call check in `reconcileEvent` after startup time filter
  - Return false only on `NotFound` (confirmed deletion)
  - Unit tests pass

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Startup time check uses wrong timestamp field | Events incorrectly filtered | Use `lastTimestamp` which reflects most recent occurrence; fall back to `eventTime` for series-based events |
| API errors when checking resource existence | Events incorrectly skipped | Only skip on `NotFound` error; assume exists on transient API errors |
| Performance impact from extra API call | Slower event processing | Leverages informer cache; check happens early to avoid wasted work on deleted resources |

## Dependencies

- None - this is a self-contained enhancement to the existing controller

## Out of Scope

- Hierarchical exclusion filters (deferred - can be added in future PRD if requested)
- Regex-based exclusion patterns
- Per-policy startup time configuration (automatic is simpler and covers the use case)

## Decision Log

| Date | Decision | Rationale | Impact |
|------|----------|-----------|--------|
| 2025-12-08 | Remove exclusion filters from scope | Focus on solving the immediate restart notification storm problem; exclusions add complexity and can be addressed in a separate PRD if users request it | Reduced scope to single feature; faster delivery |
| 2025-12-08 | Add deleted resource filtering to this PRD | Closely related to event filtering improvements; both address unwanted notifications; leverages existing `*/*` RBAC permissions | Expanded scope; comprehensive event filtering solution |

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-07 | PRD created |
| 2025-12-08 | Milestone 1 complete - Startup time filtering implemented with unit tests |
| 2025-12-08 | Scope reduced - Exclusion filters deferred to future PRD |
| 2025-12-08 | PRD complete |
| 2025-12-08 | PRD expanded - Added Milestone 2 to address notifications for deleted resources |
| 2025-12-08 | Milestone 2 complete - Added `involvedObjectExists` check using unstructured client; all tests pass |
