# PRD: CronJob Name Parsing Fallback for Deleted Pods

**Issue**: [#17](https://github.com/vfarcic/dot-ai-controller/issues/17)
**Status**: Completed
**Priority**: Medium
**Created**: 2025-12-01

## Problem Statement

The owner reference resolution for Job/CronJob rate limiting (implemented in #14) doesn't work when the pod is already deleted by the time the event is processed. This is a common scenario with CronJob pods, which are short-lived and often cleaned up quickly after completion.

### Current Behavior

When a pod event is processed but the pod no longer exists:

```
DEBUG Failed to fetch Pod for owner resolution, using pod name
{"pod": "my-backup-job-29409620-abc12", "error": "Pod not found"}
```

The controller falls back to using the unique pod name (`my-backup-job-29409620-abc12`), which defeats the purpose of grouping CronJob pods together for rate limiting.

### Impact

- **Rate Limiting Bypass**: Each CronJob execution creates a separate rate limit bucket
- **Alert Spam**: Users receive repeated alerts for the same recurring CronJob failure
- **Inconsistent Behavior**: Rate limiting works for long-lived pods but not short-lived ones

## Solution Overview

When the pod is not found, parse the CronJob name from the pod name pattern as a fallback. CronJob pods follow a predictable naming convention:

```
{cronjob-name}-{job-timestamp}-{random-suffix}
```

### Example Transformations

| Pod Name | Parsed CronJob Name |
|----------|---------------------|
| `my-backup-job-29409620-abc12` | `my-backup-job` |
| `database-cleanup-29409621-xyz99` | `database-cleanup` |
| `simple-task-28373940-a1b2c` | `simple-task` |

### Resolution Flow

```
Pod event received
       │
       ▼
┌─────────────────────────────┐
│ Try to fetch Pod            │  ◄── Existing logic (PR #14)
└─────────────────────────────┘
       │
       ├── Pod exists ──────────► Resolve ownerReferences chain
       │                          Return actual CronJob/Job name
       │
       ▼
┌─────────────────────────────┐
│ Pod not found               │  ◄── NEW fallback
│ Parse name pattern          │
└─────────────────────────────┘
       │
       ├── Matches CronJob ─────► Return ("cronjob", parsed-name)
       │   pattern
       │
       ▼
┌─────────────────────────────┐
│ Doesn't match pattern       │  ◄── Existing fallback
│ Return original pod name    │
└─────────────────────────────┘
```

## Technical Design

### Parsing Logic

CronJob pod names follow the pattern: `{cronjob-name}-{timestamp}-{suffix}`

1. Split pod name by `-`
2. Validate minimum segments (at least 3: name + timestamp + suffix)
3. Validate second-to-last segment is numeric (timestamp)
4. Validate last segment is alphanumeric suffix (typically 5 chars)
5. Join all segments except last two to get CronJob name

### Helper Function

```go
// parseCronJobNameFromPodName attempts to extract a CronJob name from a pod name
// that follows the pattern: {cronjob-name}-{timestamp}-{suffix}
// Returns the parsed name and true if successful, empty string and false otherwise.
func parseCronJobNameFromPodName(podName string) (string, bool) {
    // Implementation details in code
}
```

### Edge Cases

| Scenario | Pod Name | Result |
|----------|----------|--------|
| Valid CronJob pattern | `my-backup-29409620-abc12` | `my-backup` |
| CronJob with hyphens | `my-backup-job-29409620-abc12` | `my-backup-job` |
| Too few segments | `simple-abc12` | Falls back to original |
| Non-numeric timestamp | `my-job-notanumber-abc12` | Falls back to original |
| Standalone Job pod | `my-job-xyz12` | Falls back to original |

### API Changes

None. This is an internal behavioral improvement.

### Files to Modify

- `internal/controller/remediationpolicy_controller.go`: Add parsing function and update fallback logic

## Success Criteria

1. **Functional**: Deleted CronJob pods are correctly grouped by parsed CronJob name
2. **Accurate**: Parsing correctly handles CronJob names containing hyphens
3. **Safe**: Invalid patterns fall back to original pod name (no regressions)
4. **Tested**: Unit tests cover all parsing scenarios and edge cases
5. **Logged**: Debug logs indicate when name parsing is used vs owner resolution

## Milestones

### Milestone 1: Implement Name Parsing Logic
- [x] Create `parseCronJobNameFromPodName()` helper function
- [x] Validate timestamp segment is numeric
- [x] Handle CronJob names containing hyphens
- [x] Add unit tests for parsing function

### Milestone 2: Integrate with Owner Resolution
- [x] Update `resolveOwnerForRateLimiting()` to use parsing fallback
- [x] Return `("cronjob", parsed-name)` when pattern matches
- [x] Add debug logging for parsing decisions
- [x] Preserve existing behavior for non-matching patterns

### Milestone 3: Testing and Validation
- [x] Unit tests for deleted pod scenarios with CronJob patterns
- [x] Unit tests for edge cases (short names, non-CronJob patterns)
- [x] Integration tests verifying rate limiting with deleted pods
- [x] All existing tests continue to pass

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| False positives (non-CronJob pods matching pattern) | Low | Strict validation of timestamp segment; fallback is safe |
| CronJob names with unusual characters | Low | Pattern requires standard naming; falls back safely |
| Performance impact from string parsing | Very Low | Simple string operations; only runs when pod not found |

## Dependencies

- Builds on PR #14 (owner reference resolution)
- No external dependencies

## Out of Scope

- Parsing Job names from pod names (Jobs don't have predictable timestamp patterns)
- User-configurable parsing patterns
- Caching parsed names

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-01 | PRD created based on issue #15 analysis |
| 2025-12-01 | Implementation completed: `parseCronJobNameFromPodName()` helper function added, `resolveOwnerForRateLimiting()` updated with parsing fallback. 19 new unit tests added (12 for parsing function, 7 for deleted pod scenarios). All 133 tests passing. |

---

*This PRD tracks the implementation of CronJob name parsing fallback for deleted pods.*
