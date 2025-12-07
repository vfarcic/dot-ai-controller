# PRD: Event Filtering Improvements for RemediationPolicy

**GitHub Issue**: #26
**Status**: Planning
**Priority**: High
**Created**: 2025-12-07

## Problem Statement

The RemediationPolicy controller has two significant limitations that affect usability:

1. **Notification storms on restart**: When the controller restarts, it reprocesses all existing events in the cluster because the in-memory `processedEvents` map is lost. This causes a flood of notifications for historical events that may no longer be relevant.

2. **No exclusion mechanism**: Users can only specify positive matching criteria (which events TO process). There's no way to exclude specific event reasons (like `ContainerdStart`, `DockerStart`) or object kinds (like `Node`). This forces users to either:
   - Use verbose configurations listing every reason/kind they DO want
   - Accept noise from unwanted events

## Solution Overview

### 1. Automatic Startup Time Filtering

The controller will automatically ignore events that occurred before it started:

- Record controller startup time on initialization
- Filter events where `lastTimestamp < startupTime`
- No configuration needed - automatic behavior
- Handles both new events and recurring events (checks `lastTimestamp`, not `creationTimestamp`)

### 2. Hierarchical Exclusion Filters

Add exclusion fields at two levels for flexibility:

**Spec-level (global)** - applies to all selectors:
```yaml
spec:
  excludeReasons:
    - ContainerdStart
    - DockerStart
  excludeInvolvedObjectKinds:
    - Node
```

**Selector-level** - applies only to that selector, adds to global exclusions:
```yaml
spec:
  excludeReasons:
    - ContainerdStart  # Global
  eventSelectors:
    - type: Warning
      namespace: default
      excludeReasons:
        - BackOff  # Additional exclusion for this selector only
```

## User Stories

1. **As a cluster operator**, I want the controller to only process events that occur after it starts, so I don't get flooded with notifications for old events after a restart.

2. **As a platform engineer**, I want to globally exclude noisy event reasons like `ContainerdStart` from all my policies, so I don't have to repeat exclusions in every selector.

3. **As a developer**, I want to exclude specific event reasons for certain namespaces while keeping them for others, so I have fine-grained control over what gets processed.

## Technical Design

### API Changes

Update `RemediationPolicySpec` in `api/v1alpha1/remediationpolicy_types.go`:

```go
type RemediationPolicySpec struct {
    // ... existing fields ...

    // ExcludeReasons is a list of event reasons to exclude globally
    // Events with these reasons will not be processed by any selector
    // +optional
    ExcludeReasons []string `json:"excludeReasons,omitempty"`

    // ExcludeInvolvedObjectKinds is a list of involved object kinds to exclude globally
    // Events involving these object kinds will not be processed by any selector
    // +optional
    ExcludeInvolvedObjectKinds []string `json:"excludeInvolvedObjectKinds,omitempty"`
}

type EventSelector struct {
    // ... existing fields ...

    // ExcludeReasons is a list of event reasons to exclude for this selector
    // Adds to (does not replace) spec-level excludeReasons
    // +optional
    ExcludeReasons []string `json:"excludeReasons,omitempty"`

    // ExcludeInvolvedObjectKinds is a list of involved object kinds to exclude for this selector
    // Adds to (does not replace) spec-level excludeInvolvedObjectKinds
    // +optional
    ExcludeInvolvedObjectKinds []string `json:"excludeInvolvedObjectKinds,omitempty"`
}
```

### Controller Changes

Update `RemediationPolicyReconciler` in `internal/controller/remediationpolicy_controller.go`:

1. Add `startupTime time.Time` field to reconciler struct
2. Initialize `startupTime` in `SetupWithManager` or constructor
3. Add startup time check in `reconcileEvent` before any processing
4. Update `matchesSelector` to check exclusions after positive matching

### Matching Logic

The updated matching logic in `matchesSelector`:

```go
func (r *RemediationPolicyReconciler) matchesSelector(event *corev1.Event, selector EventSelector, policy *RemediationPolicy) bool {
    // 1. Check startup time first (most efficient filter)
    if event.LastTimestamp.Time.Before(r.startupTime) {
        return false
    }

    // 2. Existing positive matching logic
    if selector.Type != "" && event.Type != selector.Type {
        return false
    }
    // ... other positive checks ...

    // 3. Check global exclusions
    for _, reason := range policy.Spec.ExcludeReasons {
        if event.Reason == reason {
            return false
        }
    }
    for _, kind := range policy.Spec.ExcludeInvolvedObjectKinds {
        if event.InvolvedObject.Kind == kind {
            return false
        }
    }

    // 4. Check selector-level exclusions
    for _, reason := range selector.ExcludeReasons {
        if event.Reason == reason {
            return false
        }
    }
    for _, kind := range selector.ExcludeInvolvedObjectKinds {
        if event.InvolvedObject.Kind == kind {
            return false
        }
    }

    return true
}
```

## Success Criteria

1. **Startup filtering works**: After controller restart, no events with `lastTimestamp` before startup are processed
2. **Global exclusions work**: Events matching spec-level exclusions are ignored across all selectors
3. **Selector exclusions work**: Events matching selector-level exclusions are ignored for that selector only
4. **Exclusions combine**: Selector exclusions add to (not replace) global exclusions
5. **Existing behavior preserved**: Positive matching logic unchanged; existing configurations work without modification
6. **Tests pass**: Unit and integration tests cover all new functionality

## Milestones

- [ ] **Milestone 1: Startup time filtering**
  - Add `startupTime` field to reconciler
  - Initialize on controller startup
  - Filter events in `reconcileEvent`
  - Unit tests for startup filtering

- [ ] **Milestone 2: API changes for exclusions**
  - Update CRD types with exclusion fields
  - Run `make generate manifests`
  - Update sample manifests

- [ ] **Milestone 3: Exclusion logic implementation**
  - Update `matchesSelector` with exclusion checks
  - Ensure global + selector exclusions combine correctly
  - Unit tests for exclusion logic

- [ ] **Milestone 4: Integration testing**
  - Integration tests with envtest
  - Test various exclusion combinations
  - Test startup time filtering with real events

- [ ] **Milestone 5: Documentation and release**
  - Update user documentation
  - Add examples showing exclusion usage
  - Update CHANGELOG

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Startup time check uses wrong timestamp field | Events incorrectly filtered | Use `lastTimestamp` which reflects most recent occurrence |
| Breaking change for existing configs | User disruption | Exclusions are additive; existing configs work unchanged |
| Performance impact from additional checks | Slower event processing | Exclusion checks are O(n) with small n; negligible impact |

## Dependencies

- None - this is a self-contained enhancement to the existing controller

## Out of Scope

- Regex-based exclusion patterns (could be future enhancement)
- Exclusion by event message content (already has `message` regex in positive matching)
- Per-policy startup time configuration (automatic is simpler and covers the use case)

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-07 | PRD created |

