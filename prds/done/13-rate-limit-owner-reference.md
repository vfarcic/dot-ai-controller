# PRD: Rate Limiting Owner Reference for Jobs/CronJobs

**Issue**: [#13](https://github.com/vfarcic/dot-ai-controller/issues/13)
**Status**: Completed
**Priority**: Medium
**Created**: 2025-11-30

## Problem Statement

Currently, rate limiting in the RemediationPolicy controller uses the pod name as part of the rate limit key. The key format is:

```
policy-namespace/policy-name/object-namespace/object-name/event-reason
```

This causes issues with CronJob pods, where each run creates a new pod with a unique name (e.g., `my-cronjob-28373940-xyz`). Even with rate limiting configured, every CronJob execution that fails triggers a new alert because the pod name is different.

### Impact
- **Alert Spam**: Users receive repeated alerts for the same recurring CronJob failure
- **Resource Waste**: Each unique pod name creates new rate limit tracking entries
- **Poor User Experience**: Rate limiting doesn't work as expected for Job/CronJob workloads

## Solution Overview

For pods owned by a Job or CronJob, resolve the owner chain and use the ultimate owner (CronJob or Job) name instead of the pod name in the rate limit key.

### Example Transformation

**Before** (current behavior):
```
default/my-policy/default/my-cronjob-28373940-xyz/BackOff
default/my-policy/default/my-cronjob-28373941-abc/BackOff  # Different key!
```

**After** (proposed behavior):
```
default/my-policy/default/cronjob:my-cronjob/BackOff
default/my-policy/default/cronjob:my-cronjob/BackOff  # Same key!
```

## Technical Design

### Owner Reference Resolution

When generating a rate limit key for a Pod event:

1. **Check Pod Owner References**: If the pod has an ownerReference pointing to a Job, continue resolution
2. **Check Job Owner References**: If the Job has an ownerReference pointing to a CronJob, use the CronJob name
3. **Fallback**: If no Job/CronJob ownership, or for non-Pod resources, use the original object name

### Key Format Change

The rate limit key format will be enhanced:

```
policy-ns/policy-name/object-ns/[kind:]object-name/event-reason
```

- For Pods owned by CronJobs: `cronjob:cronjob-name`
- For Pods owned by Jobs (not CronJob): `job:job-name`
- For Pods not owned by Jobs: `pod-name` (unchanged)
- For non-Pod resources: `object-name` (unchanged)

### API Changes

No API changes required. This is an internal behavioral improvement to the rate limiting logic.

### Implementation Location

- **File**: `internal/controller/remediationpolicy_controller.go`
- **Method to modify**: `getRateLimitKey()`
- **New helper method**: `resolveOwnerForRateLimiting()`

## Success Criteria

1. **Functional**: CronJob pods from the same CronJob share a single rate limit key
2. **Functional**: Job pods from the same Job share a single rate limit key
3. **Backward Compatible**: Non-Job/CronJob pods continue to use pod name
4. **Tested**: Unit tests cover all ownership scenarios
5. **Documented**: Code comments explain the owner resolution logic

## Milestones

### Milestone 1: Owner Reference Resolution
- [x] Implement `resolveOwnerForRateLimiting()` helper function
- [x] Handle Pod -> Job -> CronJob ownership chain
- [x] Handle Pod -> Job ownership (no CronJob parent)
- [x] Handle edge cases (no owner, non-Pod resources)

### Milestone 2: Rate Limit Key Integration
- [x] Update `getRateLimitKey()` to use owner resolution
- [x] Include kind prefix in key for clarity (e.g., `cronjob:name`)
- [x] Ensure backward compatibility for non-Job/CronJob workloads

### Milestone 3: Testing & Validation
- [x] Unit tests for owner resolution with various ownership scenarios
- [x] Unit tests for rate limiting with CronJob pods
- [x] Integration tests verifying rate limiting behavior
- [x] Manual testing with actual CronJob workloads

### Milestone 4: Documentation & Release
- [x] Code comments explaining the owner resolution logic
- [x] Update any relevant user documentation (N/A - internal behavior change, no user-facing doc updates needed)
- [ ] PR review and merge

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| API calls to fetch Job/CronJob objects could add latency | Medium | Use informer cache instead of direct API calls |
| Owner reference chain could be broken or invalid | Low | Fallback to original pod name if resolution fails |
| Memory leak from orphaned rate limit entries | Low | Existing cleanup logic handles this |

## Dependencies

- Access to Kubernetes API for fetching Job and CronJob objects
- Informer cache should include Jobs and CronJobs for efficient lookups

## Out of Scope

- Rate limiting based on labels or annotations
- User-configurable owner resolution rules
- Support for other controller types (Deployment, ReplicaSet, etc.)

## Progress Log

| Date | Update |
|------|--------|
| 2025-11-30 | PRD created based on issue #13 |
| 2025-12-01 | Implementation completed: `resolveOwnerForRateLimiting()` and `getRateLimitKey()` updated with owner resolution. Unit tests added covering all scenarios. All 114 tests passing. |

---

*This PRD tracks the implementation of owner reference-based rate limiting for Jobs and CronJobs.*
