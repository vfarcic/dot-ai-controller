# PRD: Persist Rate Limit State Across Pod Restarts

**Issue**: [#20](https://github.com/vfarcic/dot-ai-controller/issues/20)
**Status**: Not Started
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

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: dot-ai-controller-cooldown-state
  namespace: <controller-namespace>
  labels:
    app.kubernetes.io/component: cooldown-state
    app.kubernetes.io/managed-by: dot-ai-controller
data:
  # JSON-encoded cooldown entries
  cooldowns: |
    {
      "policy1/namespace/object/reason1": "2025-12-04T10:30:00Z",
      "policy2/namespace/object/reason2": "2025-12-04T14:00:00Z"
    }
  # Metadata
  lastSync: "2025-12-03T10:00:00Z"
  version: "1"
```

### Configuration Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--cooldown-persistence-enabled` | `true` | Enable cooldown state persistence |
| `--cooldown-sync-interval` | `60s` | How often to sync to ConfigMap |
| `--cooldown-configmap-name` | `dot-ai-controller-cooldown-state` | Name of the ConfigMap |
| `--cooldown-min-persist-duration` | `1h` | Only persist cooldowns longer than this |

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
- [ ] Create CooldownPersistence struct with Load/Sync methods
- [ ] Implement ConfigMap read/write with proper error handling
- [ ] Add JSON serialization for cooldown entries
- [ ] Implement entry pruning for expired cooldowns

### Milestone 2: Controller Integration
- [ ] Add persistence flags to cmd/main.go
- [ ] Initialize CooldownPersistence on startup
- [ ] Load existing state on controller start
- [ ] Mark entries dirty when cooldown is set
- [ ] Start periodic sync goroutine

### Milestone 3: Graceful Shutdown Integration
- [ ] Perform final sync on SIGTERM (integrate with PRD #19)
- [ ] Ensure sync completes before shutdown
- [ ] Log any entries that couldn't be persisted

### Milestone 4: RBAC and Helm Chart Updates
- [ ] Update ClusterRole with ConfigMap permissions
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

- PRD #19 (Graceful Shutdown) - for final sync on shutdown
- Kubernetes ConfigMap API
- No external dependencies

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

---

*This PRD tracks the implementation of rate limit state persistence for the RemediationPolicy controller.*
