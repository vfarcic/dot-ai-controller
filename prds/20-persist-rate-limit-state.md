# PRD: Persist Rate Limit State Across Pod Restarts

**Issue**: [#20](https://github.com/vfarcic/dot-ai-controller/issues/20)
**Status**: In Progress
**Priority**: High
**Created**: 2025-12-03

## Problem Statement

The RemediationPolicy controller stores rate limiting and cooldown state in memory only. When the controller pod restarts (rolling updates, node drains, Karpenter consolidation), all suppression state is lost.

### Current Behavior

The controller maintains two in-memory maps:
- `rateLimitTracking`: Tracks processing times per policy+object+reason (1-minute sliding window)
- `cooldownTracking`: Tracks cooldown periods per policy+object+reason (configurable, up to 24h+)

When the pod restarts:
- All cooldown state is lost immediately
- Events that were previously suppressed trigger new analysis
- Users receive duplicate notifications for the same issues
- LLM costs increase due to re-analyzing identical problems

### Impact

From user feedback (issue #16):
- **Notification noise**: Same issue triggers alerts after restart, especially problematic when tied to paging/ticketing systems
- **Increased LLM costs**: Re-analyzing the same CrashLoopBackOff or other persistent issues
- **Feature unreliability**: 24h cooldowns become meaningless if any restart invalidates them
- **Alert storms**: In environments with frequent pod restarts (Karpenter consolidation), this is a regular occurrence

### User Environments

The assumption that "controllers shouldn't restart frequently" doesn't hold in modern cloud-native setups:
- Karpenter with consolidation regularly moves pods between nodes
- Spot instance interruptions cause unplanned restarts
- Pods are intentionally treated as ephemeral

## Solution Overview

Persist cooldown state to a ConfigMap with periodic batch sync, restoring state on startup.

### Design Principles

1. **Pragmatic persistence**: Focus on long-duration cooldowns (>1 hour) where state loss matters most
2. **Minimal API server load**: Batch writes with configurable sync interval (default 60s)
3. **Simple garbage collection**: Prune expired entries on write
4. **Graceful degradation**: If persistence fails, fall back to in-memory (existing behavior)

### State Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                     Controller Startup                          │
├─────────────────────────────────────────────────────────────────┤
│  1. Load cooldown state from ConfigMap                          │
│  2. Prune expired entries                                       │
│  3. Initialize in-memory maps with restored state               │
│  4. Start periodic sync goroutine                               │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Normal Operation                            │
├─────────────────────────────────────────────────────────────────┤
│  • Rate limiting uses in-memory maps (fast)                     │
│  • Cooldown entries marked dirty when updated                   │
│  • Periodic sync writes dirty entries to ConfigMap              │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Controller Shutdown                         │
├─────────────────────────────────────────────────────────────────┤
│  1. Final sync of all dirty entries to ConfigMap                │
│  2. Graceful shutdown completes                                 │
└─────────────────────────────────────────────────────────────────┘
```

## Technical Design

### ConfigMap Structure

**Design Decision**: Per-CR ConfigMaps instead of a single global ConfigMap. Each RemediationPolicy gets its own ConfigMap with ownerReference for automatic cleanup.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: <policy-name>-cooldown-state    # Same name as CR + suffix
  namespace: <policy-namespace>          # Same namespace as CR
  labels:
    app.kubernetes.io/component: cooldown-state
    app.kubernetes.io/managed-by: dot-ai-controller
  ownerReferences:
    - apiVersion: dot-ai.devopstoolkit.live/v1alpha1
      kind: RemediationPolicy
      name: <policy-name>
      uid: <policy-uid>
data:
  # JSON-encoded cooldown entries (simpler keys - no policy prefix needed)
  cooldowns: |
    {
      "namespace/object/reason1": "2025-12-04T10:30:00Z",
      "namespace/object/reason2": "2025-12-04T14:00:00Z"
    }
  # Metadata
  lastSync: "2025-12-03T10:00:00Z"
  version: "1"
```

**Benefits of per-CR ConfigMaps**:
- Automatic cleanup via ownerReferences when policy is deleted
- Natural isolation between policies
- Simpler key format (no policy prefix needed)
- Bounded size per policy

### Configuration Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cooldown-persistence-enabled` | `true` | Enable cooldown state persistence |
| `--cooldown-sync-interval` | `60s` | How often to sync to ConfigMap |
| `--cooldown-min-persist-duration` | `1h` | Only persist cooldowns longer than this |

*Note: ConfigMap name is derived from policy name (`<policy-name>-cooldown-state`) - no flag needed.*

### What Gets Persisted

| State | Persist? | Rationale |
|-------|----------|-----------|
| `cooldownTracking` (duration >= 1h) | Yes | Long-duration cooldowns are the primary use case |
| `cooldownTracking` (duration < 1h) | No | Short cooldowns have minimal impact if lost |
| `rateLimitTracking` | No | 1-minute window; stale after restart anyway |

### Controller Changes

```go
// CooldownPersistence handles ConfigMap-based state persistence
type CooldownPersistence struct {
    client        client.Client
    namespace     string
    configMapName string
    syncInterval  time.Duration
    minDuration   time.Duration

    mu            sync.RWMutex
    dirtyEntries  map[string]bool
}

// Load restores cooldown state from ConfigMap
func (p *CooldownPersistence) Load(ctx context.Context) (map[string]time.Time, error)

// MarkDirty flags an entry for persistence
func (p *CooldownPersistence) MarkDirty(key string)

// Sync writes dirty entries to ConfigMap
func (p *CooldownPersistence) Sync(ctx context.Context, cooldowns map[string]time.Time) error

// StartPeriodicSync starts background sync goroutine
func (p *CooldownPersistence) StartPeriodicSync(ctx context.Context, getCooldowns func() map[string]time.Time)
```

### RBAC Requirements

```yaml
# Add to controller ClusterRole
- apiGroups: [""]
  resources: ["configmaps"]
  verbs: ["get", "create", "update", "patch"]
  # Optionally scope to specific ConfigMap name
```

### Files to Modify

| File | Changes |
|------|---------|
| `cmd/main.go` | Add persistence flags, initialize CooldownPersistence |
| `internal/controller/remediationpolicy_controller.go` | Integrate persistence, mark dirty on cooldown update |
| `internal/controller/persistence.go` (new) | CooldownPersistence implementation |
| `config/rbac/role.yaml` | Add ConfigMap permissions |
| `charts/dot-ai-controller/templates/rbac.yaml` | Add ConfigMap permissions |

## Success Criteria

1. **State survives restarts**: Cooldown state persisted and restored correctly
2. **Minimal API load**: ConfigMap updated at most once per sync interval
3. **Graceful degradation**: Controller works normally if ConfigMap operations fail
4. **Configurable**: All persistence settings configurable via flags
5. **Observable**: Logs indicate sync operations and any failures
6. **Size bounded**: Expired entries pruned; ConfigMap stays well under 1MB limit

## Milestones

### Milestone 1: Core Persistence Infrastructure
- [x] Create CooldownPersistence struct with Load/Sync methods
- [x] Implement ConfigMap read/write with proper error handling
- [x] Add JSON serialization for cooldown entries
- [x] Implement entry pruning for expired cooldowns

### Milestone 2: Controller Integration
- [x] Add persistence flags to cmd/main.go
- [x] Initialize CooldownPersistence on startup
- [x] Load existing state on controller start
- [x] Mark entries dirty when cooldown is set
- [x] Start periodic sync goroutine

### Milestone 3: Minimal Shutdown Sync
- [x] Perform final sync when context is cancelled (controller-runtime handles SIGTERM)
- [x] Log sync completion or failures

*Note: This is minimal shutdown handling for persistence only. Extensive graceful shutdown (draining in-flight MCP operations, shutdown-aware readiness probes, configurable timeouts) remains in PRD #19.*

### Milestone 4: RBAC and Helm Chart Updates
- [x] Update ClusterRole with ConfigMap permissions
- [ ] Add persistence configuration to Helm values
- [ ] Update Helm templates for new flags

### Milestone 5: Testing and Documentation
- [ ] Unit tests for CooldownPersistence
- [ ] Integration tests for state restoration
- [ ] E2E test for persistence across restart
- [ ] Update documentation with persistence configuration

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| ConfigMap size exceeds 1MB | Medium | Prune expired entries; only persist long cooldowns |
| Sync fails repeatedly | Low | Fall back to in-memory; log warnings |
| Race condition on startup | Medium | Load state before starting reconcilers |
| ConfigMap corruption | Low | Version field for future migrations; log and recreate if invalid |
| Multiple replicas conflict | High | Out of scope - single replica assumed (see Out of Scope) |

## Dependencies

- Kubernetes ConfigMap API
- controller-runtime's context cancellation on SIGTERM (built-in)

*Note: PRD #19 (Graceful Shutdown) is complementary but not a blocker. This PRD handles minimal shutdown sync; PRD #19 handles draining in-flight MCP operations.*

## Out of Scope

- **Multi-replica support**: This design assumes single replica; leader election and distributed state would require different approach
- **Rate limit tracking persistence**: The 1-minute sliding window is not worth persisting
- **External storage**: Redis, etcd, or database backends
- **Encryption**: ConfigMap data is not encrypted (no secrets involved)

## Alternatives Considered

### Alternative 1: RemediationPolicy Status
Store cooldown state in each policy's status subresource.

**Pros**: State co-located with policy; automatic cleanup on deletion
**Cons**: Frequent writes per policy; status size growth; complex aggregation

**Decision**: Rejected - too many writes, complex to query across policies

### Alternative 2: Lease Objects
Use Kubernetes Lease objects with built-in TTL.

**Pros**: Built-in expiration; native Kubernetes pattern
**Cons**: One object per cooldown entry; not a natural fit for this use case

**Decision**: Rejected - would create many objects; not designed for this pattern

### Alternative 3: No Persistence
Accept state loss on restart.

**Pros**: Simple; no additional complexity
**Cons**: Long-duration cooldowns become unreliable; user feedback indicates this is a real problem

**Decision**: Rejected based on user feedback in issue #16

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-03 | PRD created based on @barth12 feedback in issue #16 |
| 2025-12-15 | Milestone 1 & 2 complete. Design changed to per-CR ConfigMaps (ownerReferences for auto-cleanup). Created `persistence.go`, integrated with controller, added flags. RBAC updated. |
| 2025-12-15 | Milestone 3 complete. Added shutdown sync: `Stop()` method now uses stored `getCooldowns` callback, creates fresh context with 30s timeout, called from `main.go` after manager exits. |

---

*This PRD tracks the implementation of rate limit state persistence for the RemediationPolicy controller.*
