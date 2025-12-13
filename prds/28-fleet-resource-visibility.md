# PRD #28: Fleet-Wide Resource Visibility and Status Tracking

**GitHub Issue**: [#28](https://github.com/vfarcic/dot-ai-controller/issues/28)
**Status**: Draft
**Priority**: High
**Created**: 2025-12-13

---

## Problem Statement

Users managing multiple Kubernetes clusters lack unified visibility into resources and their statuses across the fleet. Current tooling requires:

1. **Knowing which cluster to query** - Users must switch contexts or specify clusters manually
2. **Running multiple commands** - No single query can span clusters
3. **No semantic search** - Can't ask "show me all database-related resources" across the fleet
4. **Manual correlation** - Difficult to correlate resources with available capabilities (CRDs)

---

## Solution Overview

Extend the dot-ai-controller to provide fleet-wide resource visibility by:

1. **Watching all resources** in each cluster where the controller is deployed
2. **Syncing metadata and status** to a central Qdrant database (near-realtime)
3. **Enabling semantic search** through the MCP interface ("show all failing pods across clusters")
4. **Fetching details on-demand** via kubectl/client-go when users need more information

### Architecture

```
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│   Cluster A     │  │   Cluster B     │  │   Cluster C     │
│  ┌───────────┐  │  │  ┌───────────┐  │  │  ┌───────────┐  │
│  │Controller │  │  │  │Controller │  │  │  │Controller │  │
│  │(watching) │  │  │  │(watching) │  │  │  │(watching) │  │
│  └─────┬─────┘  │  │  └─────┬─────┘  │  │  └─────┬─────┘  │
└────────┼────────┘  └────────┼────────┘  └────────┼────────┘
         │                    │                    │
         └────────────────────┼────────────────────┘
                              ▼
                 ┌────────────────────────┐
                 │   Central Qdrant DB    │
                 │ (fleet-resources coll) │
                 └───────────┬────────────┘
                             │
                 ┌───────────▼────────────┐
                 │      MCP Server        │
                 │  (dot-ai project)      │
                 │                        │
                 │  - Query interface     │
                 │  - On-demand fetches   │
                 │  - Kubeconfigs in      │
                 │    K8s Secrets         │
                 └────────────────────────┘
```

### Data Model

Each resource is stored with:

```json
{
  "id": "cluster-a/default/apps/v1/Deployment/nginx",
  "cluster": "cluster-a",
  "clusterSecretRef": "fleet-cluster-a-kubeconfig",
  "namespace": "default",
  "name": "nginx",
  "kind": "Deployment",
  "apiVersion": "apps/v1",
  "apiGroup": "apps",
  "status": {
    "availableReplicas": 3,
    "readyReplicas": 3,
    "replicas": 3,
    "conditions": [
      {"type": "Available", "status": "True"},
      {"type": "Progressing", "status": "True"}
    ]
  },
  "labels": {"app": "nginx", "env": "prod"},
  "annotations": {"description": "Web server"},
  "createdAt": "2025-12-13T10:00:00Z",
  "updatedAt": "2025-12-13T10:05:00Z"
}
```

**Key principle**: Store metadata + user-relevant status only. Full resource details fetched on-demand.

---

## Existing Code Reusability Analysis

### What Already Exists (~70% of infrastructure)

| Component | Location | Reusability |
|-----------|----------|-------------|
| **Dynamic client pattern** | `solution_controller.go:296-327` | Direct reuse - handles arbitrary resource types via `unstructured.Unstructured` |
| **Status extraction** | `solution_controller.go:361-447` | Direct reuse - multi-strategy health checking (conditions, replica counts, fallbacks) |
| **Secret-based configuration** | `remediationpolicy_mcp.go:175-217` | Direct reuse - `SecretReference` pattern for API keys |
| **HTTP client patterns** | `cmd/main.go:193-201`, `remediationpolicy_mcp.go` | Direct reuse - timeout config, retry logic, JSON handling |
| **Rate limiting/cooldowns** | `remediationpolicy_ratelimit.go` | Adapt for batch debouncing |
| **Event deduplication** | `remediationpolicy_controller.go:129-162` | Adapt for resource change detection |
| **Exponential backoff with jitter** | `solution_controller.go:157-167` | Direct reuse for retry logic |
| **Status subresource management** | `remediationpolicy_controller.go:382-477` | Direct reuse - conflict detection, retry patterns |
| **Controller-runtime watches** | Both controllers | Extend pattern for dynamic resource types |

### Key Reusable Code Examples

**Dynamic Resource Fetching** (from `solution_controller.go`):
```go
obj := &unstructured.Unstructured{}
obj.SetGroupVersionKind(schema.GroupVersionKind{
    Group:   gv.Group,
    Version: gv.Version,
    Kind:    ref.Kind,
})
if err := r.Get(ctx, key, obj); err != nil { ... }
```

**Status Extraction** (from `solution_controller.go`):
```go
// Strategy 1: Check conditions
conditions, found, _ := unstructured.NestedSlice(resource.Object, "status", "conditions")
// Strategy 2: Check replica counts
readyReplicas, _, _ := unstructured.NestedInt64(resource.Object, "status", "readyReplicas")
// Strategy 3: Fallback - existence = ready
```

**Secret Resolution** (from `remediationpolicy_mcp.go`):
```go
type SecretReference struct {
    Name string `json:"name"`
    Key  string `json:"key"`
}
// Resolve from namespace, graceful fallback if not found
```

### What Needs to Be Built New (~30%)

| Component | Description | Complexity |
|-----------|-------------|------------|
| **Qdrant integration** | Client, connection management, upsert/delete operations | Medium |
| **Dynamic resource discovery** | Use discovery API to find all resource types in cluster | Medium |
| **Batch aggregation** | Collect status changes into time-windowed batches | Medium |
| **Change detection** | Determine if status actually changed (not just any update) | Low |
| **Embedding generation** | Generate vectors for semantic search | Medium |
| **Fleet sync controller** | New controller orchestrating the above | Medium |
| **Configuration CRD or flags** | Cluster ID, Qdrant endpoint, batch settings | Low |

---

## Scope

### In Scope

**Phase 1: Controller (dot-ai-controller)**
- New `FleetSync` controller leveraging existing patterns
- Dynamic resource discovery and watching
- Status extraction using existing `checkStatusConditions`/`checkReplicaCounts`
- Qdrant integration with batching
- Configuration via Helm values (similar to existing MCP endpoint config)

**Phase 2: MCP Integration (dot-ai) - Separate PRD**
- Query interface for fleet-wide searches
- Natural language query translation
- On-demand resource detail fetching
- Correlation with capabilities

### Out of Scope

- Kubernetes Events resource syncing (high volume, low signal)
- Full resource spec storage (only metadata + status)
- Cross-cluster relationship tracking beyond Solution CR
- Resource modification through fleet interface

---

## Technical Design

### Phase 1: Controller Implementation

#### 1. Dynamic Resource Discovery

```go
// Reuse pattern from discovery client
func (r *FleetSyncReconciler) discoverResources(ctx context.Context) ([]schema.GroupVersionResource, error) {
    resources, err := r.discoveryClient.ServerPreferredResources()
    if err != nil {
        return nil, err
    }

    var gvrs []schema.GroupVersionResource
    for _, resourceList := range resources {
        gv, _ := schema.ParseGroupVersion(resourceList.GroupVersion) // Existing pattern
        for _, resource := range resourceList.APIResources {
            if strings.Contains(resource.Name, "/") {
                continue // Skip subresources
            }
            gvrs = append(gvrs, schema.GroupVersionResource{
                Group:    gv.Group,
                Version:  gv.Version,
                Resource: resource.Name,
            })
        }
    }
    return gvrs, nil
}
```

#### 2. Status Extraction (Reuse Existing)

```go
// Directly reuse from solution_controller.go
func (r *FleetSyncReconciler) extractStatus(obj *unstructured.Unstructured) FleetResourceStatus {
    status := FleetResourceStatus{}

    // Reuse checkStatusConditions pattern
    conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
    if found {
        status.Conditions = r.parseConditions(conditions)
    }

    // Reuse checkReplicaCounts pattern
    for _, field := range []string{"replicas", "readyReplicas", "availableReplicas"} {
        if val, found, _ := unstructured.NestedInt64(obj.Object, "status", field); found {
            status.ReplicaCounts[field] = val
        }
    }

    // Reuse phase extraction
    if phase, found, _ := unstructured.NestedString(obj.Object, "status", "phase"); found {
        status.Phase = phase
    }

    return status
}
```

#### 3. Batch Processing (Adapt Rate Limiting Pattern)

```go
// Adapt from remediationpolicy_ratelimit.go patterns
type BatchProcessor struct {
    queue        chan FleetResourceEvent
    batchWindow  time.Duration  // e.g., 5 seconds
    maxBatchSize int            // e.g., 1000
    qdrantClient *qdrant.Client
}

func (b *BatchProcessor) Run(ctx context.Context) {
    ticker := time.NewTicker(b.batchWindow)
    defer ticker.Stop()

    var batch []FleetResourceEvent

    for {
        select {
        case event := <-b.queue:
            batch = append(batch, event)
            if len(batch) >= b.maxBatchSize {
                b.flush(ctx, batch)
                batch = nil
            }
        case <-ticker.C:
            if len(batch) > 0 {
                b.flush(ctx, batch)
                batch = nil
            }
        case <-ctx.Done():
            return
        }
    }
}
```

#### 4. Qdrant Integration (New)

```go
type QdrantClient struct {
    client     *qdrant.Client
    collection string
}

func (q *QdrantClient) UpsertResources(ctx context.Context, resources []FleetResource) error {
    points := make([]*qdrant.PointStruct, len(resources))
    for i, res := range resources {
        embedding := q.generateEmbedding(res) // Generate vector
        points[i] = &qdrant.PointStruct{
            Id:      qdrant.NewID(res.ID),
            Vectors: qdrant.NewVectors(embedding...),
            Payload: res.ToPayload(),
        }
    }
    _, err := q.client.Upsert(ctx, &qdrant.UpsertPoints{
        CollectionName: q.collection,
        Points:         points,
    })
    return err
}
```

#### 5. Configuration (Adapt Existing Pattern)

```yaml
# Helm values - similar to existing MCP endpoint config
fleetSync:
  enabled: true
  clusterID: "cluster-a"
  clusterSecretRef: "fleet-cluster-a-kubeconfig"
  qdrant:
    endpoint: "https://qdrant.example.com:6334"
    apiKeySecretRef:
      name: "qdrant-credentials"
      key: "api-key"
    collection: "fleet-resources"
  batch:
    windowSeconds: 5
    maxSize: 1000
```

---

## Performance Considerations

| Concern | Mitigation | Existing Pattern to Reuse |
|---------|------------|---------------------------|
| API server load | Shared informer factory | Controller-runtime handles this |
| Memory footprint | Configurable resync period | Existing controller patterns |
| Qdrant writes | Batching with time window | Adapt rate limiting patterns |
| Duplicate processing | Change detection | Adapt event deduplication |
| Retry storms | Exponential backoff with jitter | `solution_controller.go:157-167` |

---

## Milestones

### Phase 1: Controller (dot-ai-controller)

- [ ] **M1: Resource discovery and watching infrastructure**
  - Implement dynamic resource discovery using discovery API
  - Create informers for all discovered resource types
  - Reuse existing event handler patterns from RemediationPolicy
  - Add change detection (status-only changes)

- [ ] **M2: Qdrant client integration**
  - Add Qdrant Go client dependency
  - Implement connection management with retry logic (reuse backoff patterns)
  - Implement upsert/delete operations with batching
  - Add embedding generation for semantic search

- [ ] **M3: Batch processing and sync logic**
  - Implement batch queue (adapt rate limiting patterns)
  - Status extraction reusing `checkStatusConditions`/`checkReplicaCounts`
  - Initial full scan on controller startup
  - Handle API server disconnects gracefully

- [ ] **M4: Configuration and Helm chart updates**
  - Add fleetSync configuration to Helm values
  - Secret reference pattern for Qdrant API key (reuse SecretReference)
  - Cluster identifier configuration
  - Feature flag to enable/disable

- [ ] **M5: Testing and documentation**
  - Unit tests (reuse existing test patterns)
  - Integration tests with envtest
  - E2E tests with Kind cluster and Qdrant
  - Update Helm chart documentation

### Phase 2: MCP Integration (dot-ai) - Tracked in Separate PRD

- [ ] **M6: Fleet query tool in MCP**
- [ ] **M7: On-demand resource details**
- [ ] **M8: Capabilities correlation**

---

## Dependencies

### Phase 1 (Controller)
- `github.com/qdrant/go-client` - Qdrant Go client
- Existing controller-runtime setup
- Existing dynamic client patterns

### Phase 2 (MCP)
- Phase 1 complete (data available in Qdrant)
- Existing Qdrant integration in dot-ai

---

## Risks and Mitigations

| Risk | Mitigation | Existing Pattern |
|------|------------|------------------|
| API server overload | Shared informers, configurable resync | Controller-runtime standard |
| Qdrant write bottleneck | Batching, async writes | Adapt rate limiting |
| Memory exhaustion | Resource limits, selective caching | Existing controller limits |
| Stale data | Periodic resync, change detection | Adapt event deduplication |
| Credential security | Secret references, not inline | Existing SecretReference pattern |

---

## Open Questions

1. **Embedding model**: Same as capabilities collection? Or separate?
2. **Resource type filtering**: Should we allow excluding certain types (e.g., Secrets)?
3. **Namespace filtering**: Watch all namespaces or configurable subset?
4. **Solution CR integration**: Should we automatically create Solution CRs for discovered resource groups?

---

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-13 | PRD created, reusability analysis completed |

---

## References

- [dot-ai-controller Repository](https://github.com/vfarcic/dot-ai-controller)
- [dot-ai Repository](https://github.com/vfarcic/dot-ai)
- [Existing Solution Controller](../internal/controller/solution_controller.go) - Status extraction patterns
- [Existing RemediationPolicy Controller](../internal/controller/remediationpolicy_controller.go) - Event watching patterns
- [Qdrant Go Client](https://github.com/qdrant/go-client)
