# PRD: Graceful Shutdown Handling

**Issue**: [#19](https://github.com/vfarcic/dot-ai-controller/issues/19)
**Status**: In Progress
**Priority**: Medium
**Created**: 2025-12-01

## Problem Statement

The RemediationPolicy and Solution controllers lack proper graceful shutdown handling. When receiving SIGTERM (e.g., during rolling updates, node drains, or pod evictions), in-flight operations may be interrupted abruptly without proper cleanup.

### Current Behavior

When SIGTERM is received:
- Controller exits immediately
- In-flight MCP remediation calls are terminated mid-request
- Events being processed are lost
- No indication to Kubernetes that the pod is shutting down (readiness probe unchanged)

### Impact

- **Lost work**: In-flight remediation operations are abandoned
- **Inconsistent state**: Partial operations may leave resources in unexpected states
- **Poor observability**: No clear indication that shutdown is occurring
- **Potential duplicate processing**: Events may be reprocessed by replacement pod

## Solution Overview

Implement graceful shutdown handling that:

1. **Marks pod as not ready** immediately on SIGTERM (stops new work routing)
2. **Drains in-flight operations** with configurable timeout
3. **Exits cleanly** after drain completes or timeout expires

### Shutdown Sequence

```
SIGTERM received
      │
      ▼
┌─────────────────────────────────┐
│ Mark readiness probe NOT READY  │  ◄── Kubernetes stops routing
└─────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────┐
│ Signal controllers to stop      │  ◄── No new reconciles accepted
│ accepting new work              │
└─────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────┐
│ Wait for in-flight operations   │  ◄── MCP calls, reconciles
│ to complete                     │
└─────────────────────────────────┘
      │
      ├── All complete ──────────► Clean exit (code 0)
      │
      ▼
┌─────────────────────────────────┐
│ Timeout reached                 │  ◄── Force exit
│ Log warning about abandoned     │
│ operations                      │
└─────────────────────────────────┘
      │
      ▼
   Exit (code 0)
```

## Technical Design

### Configuration Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--remediation-shutdown-timeout` | `20m` | Max time to wait for RemediationPolicy in-flight operations |
| `--solution-shutdown-timeout` | `2m` | Max time to wait for Solution in-flight operations |

RemediationPolicy has a longer default because MCP remediation operations can take significant time.

### Controller Manager Integration

controller-runtime's manager already supports graceful shutdown via `GracefulShutdownTimeout`. We need to:

1. Configure appropriate timeouts per controller type
2. Ensure HTTP clients respect context cancellation
3. Update health/readiness endpoints during shutdown

### Readiness Probe Updates

```go
// Health check handler that respects shutdown state
type shutdownAwareHealthz struct {
    shuttingDown *atomic.Bool
}

func (h *shutdownAwareHealthz) Check(_ *http.Request) error {
    if h.shuttingDown.Load() {
        return errors.New("shutting down")
    }
    return nil
}
```

### In-Flight Operation Tracking

For RemediationPolicy controller:
- Track active MCP HTTP requests
- Pass context to HTTP client calls (already done)
- Context cancellation will propagate to in-flight requests

For Solution controller:
- Standard reconcile loop respects context cancellation
- No special tracking needed

### Files to Modify

| File | Changes |
|------|---------|
| `cmd/main.go` | Add shutdown timeout flags, configure manager graceful shutdown |
| `internal/controller/remediationpolicy_controller.go` | Ensure HTTP calls respect context |
| Health endpoint setup | Add shutdown-aware readiness check |

## Success Criteria

1. **Readiness**: Pod marked not-ready immediately on SIGTERM
2. **Drain**: In-flight MCP calls complete before exit (within timeout)
3. **Configurable**: Shutdown timeouts configurable via flags
4. **Observable**: Logs indicate shutdown progress and any abandoned operations
5. **Clean exit**: Exit code 0 on successful drain

## Milestones

### Milestone 1: Shutdown-Aware Readiness Probe
- [x] Create shutdown state tracker (atomic bool)
- [x] Implement shutdown-aware health check handler
- [x] Wire up SIGTERM handler to set shutdown state
- [x] Verify readiness probe returns unhealthy during shutdown

### Milestone 2: Configurable Graceful Shutdown
- [ ] Add `--remediation-shutdown-timeout` flag (default 20m)
- [ ] Add `--solution-shutdown-timeout` flag (default 2m)
- [ ] Configure controller-runtime manager with appropriate timeout
- [ ] Add shutdown timeout to Helm chart values

### Milestone 3: Context Propagation Verification
- [ ] Verify RemediationPolicy HTTP client respects context cancellation
- [ ] Verify Solution controller reconciles respect context
- [ ] Add logging for shutdown progress

### Milestone 4: Testing and Documentation
- [x] Unit tests for shutdown-aware health check
- [ ] Integration test for graceful shutdown behavior
- [ ] Update documentation with shutdown configuration options

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Long-running MCP operations exceed timeout | Medium | 20m default is generous; operations log warning if abandoned |
| HTTP client doesn't respect context cancellation | Medium | Verify and fix context propagation in HTTP calls |
| Kubernetes terminates before drain completes | Medium | Set pod `terminationGracePeriodSeconds` >= shutdown timeout |

## Dependencies

- controller-runtime graceful shutdown support (already available)
- No external dependencies

## Out of Scope

- Persisting cooldown state on shutdown (separate concern, see issue #16 discussion)
- Leader election handling (single replica assumed)
- Webhook server shutdown (not currently used)

## Kubernetes Configuration

For graceful shutdown to work properly, the pod's `terminationGracePeriodSeconds` must be >= the configured shutdown timeout:

```yaml
# In Helm values or deployment manifest
spec:
  terminationGracePeriodSeconds: 1260  # 21 minutes (20m timeout + buffer)
```

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-01 | PRD created based on issue #16 discussion about rate limit persistence |
| 2026-01-23 | Milestone 1 complete: Added `internal/shutdown` package with `Tracker`, `HealthChecker`, and `SetupSignalHandler`. Updated `cmd/main.go` to use shutdown-aware readiness probe. Unit tests passing. |

---

*This PRD tracks the implementation of graceful shutdown handling for both controllers.*
