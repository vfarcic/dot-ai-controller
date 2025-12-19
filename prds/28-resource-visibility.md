# PRD #28: Resource Visibility and Status Tracking

**GitHub Issue**: [#28](https://github.com/vfarcic/dot-ai-controller/issues/28)
**Status**: In Progress
**Priority**: High
**Created**: 2025-12-13

---

## Problem Statement

Users lack efficient visibility into resources and their statuses within a Kubernetes cluster. Current tooling requires:

1. **Manual kubectl commands** - Users must run multiple commands to understand resource state
2. **No semantic search** - Can't ask "show me all database-related resources" or "what's failing?"
3. **Manual correlation** - Difficult to correlate resources with available capabilities (CRDs)
4. **No unified view** - Resource status scattered across different commands and outputs

> **Scope Note**: This PRD focuses on single-cluster resource visibility. Fleet-wide (multi-cluster) visibility is deferred to a future PRD after validating the single-cluster approach and determining the optimal architecture (central MCP vs distributed).

---

## Solution Overview

Extend the dot-ai-controller to provide resource visibility within a cluster by:

1. **Watching all resources** in the cluster where the controller is deployed
2. **Syncing metadata and status** to Qdrant (near-realtime)
3. **Enabling semantic search** through the MCP interface ("show all failing pods", "what databases are running?")
4. **Fetching details on-demand** via kubectl/client-go when users need more information

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Cluster                       │
│                                                              │
│  ┌─────────────────┐       ┌─────────────────────────────┐  │
│  │  dot-ai-controller │     │        MCP Server           │  │
│  │                   │     │      (dot-ai project)       │  │
│  │  - Watches all    │     │                             │  │
│  │    resources      │ HTTP│  - Receives resource data   │  │
│  │  - Detects changes│────▶│  - Generates embeddings     │  │
│  │  - Sends to MCP   │     │  - Writes to Qdrant         │  │
│  │                   │     │  - Query interface          │  │
│  └─────────────────┘       │  - On-demand fetches        │  │
│                             └─────────────┬───────────────┘  │
│                                           │                  │
│                             ┌─────────────▼───────────────┐  │
│                             │         Qdrant              │  │
│                             │   (resources collection)    │  │
│                             └─────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**Key design principles**:
- **Controller is "dumb"** - watches resources and sends data to MCP via HTTP
- **MCP is "smart"** - handles embeddings, diffing, and Qdrant operations
- **Qdrant is a search index, not source of truth** - Kubernetes etcd remains authoritative
- **Single-cluster scope** - All components run in the same cluster; fleet expansion deferred

### Qdrant as Search Index

Qdrant enables fast discovery and search, but is NOT the complete data store:

| Layer | Role | Data Stored |
|-------|------|-------------|
| **Kubernetes etcd** | Source of truth | Complete resources, full CRD schemas |
| **Qdrant** | Search index | Metadata + status (enough to find and filter) |
| **On-demand API calls** | Full details | Fetched from Kubernetes when user needs more |

**Query flow example:**
```
User: "show me failing databases"
  → Qdrant search: finds matching resources (IDs, basic status)
  → Return summary to user

User: "tell me more about prod-db"
  → MCP calls Kubernetes API: kubectl get/describe equivalent
  → Return full resource details
```

This keeps Qdrant lean, sync fast, and Kubernetes as the authoritative source.

### Why Semantic Search over Query Languages

Our goal is **intent-based queries** - users express what they want in natural language, not query syntax. We evaluated multiple approaches:

**Option A: Relational DB + AI-generated SQL**
```
User: "show me failing databases"

AI must generate:
SELECT * FROM resources
WHERE (
  kind IN ('PostgreSQL', 'Cluster', 'RDSInstance', 'AzureDatabase', ...)
  OR labels->>'app' LIKE '%db%'
)
AND (
  status->'conditions' @> '[{"type":"Ready","status":"False"}]'
  OR status->>'phase' = 'Failed'
)
```

**Option B: NoSQL (MongoDB) + AI-generated queries**
```
User: "show me failing databases"

AI must generate:
db.resources.find({
  $and: [
    { $or: [
      { kind: { $in: ['PostgreSQL', 'Cluster', 'RDSInstance', ...] } },
      { "labels.app": { $regex: /db/i } }
    ]},
    { $or: [
      { "status.conditions": { $elemMatch: { type: "Ready", status: "False" } } },
      { "status.phase": "Failed" }
    ]}
  ]
})
```

NoSQL solves schema flexibility (good for varied Kubernetes resources), but **not semantic understanding**. AI still must enumerate "database" kinds and know exact field paths.

**Problems with AI-generated queries (SQL or NoSQL):**
- AI must know every "database" kind (CNPG, RDS, Azure, etc.) - misses new ones
- Must understand nested JSON status structure per resource type
- Must know exact field paths and query operators
- Wrong query = silent wrong results or cryptic errors
- State-of-the-art text-to-query is ~85% accurate on clean schemas; Kubernetes JSON is messier
- Schema changes break prompts

**Option C: Vector DB + Semantic Search (chosen)**
```
User: "show me failing databases"

AI: calls search-resources("failing databases")
Qdrant: returns resources whose embeddings are semantically similar
```

**Why semantic search wins for our use case:**

| Aspect | SQL | NoSQL (MongoDB) | Semantic Search |
|--------|-----|-----------------|-----------------|
| Schema flexibility | Rigid | ✅ Flexible | ✅ Flexible |
| Schema knowledge by AI | Required | Required | Not needed |
| "Database" understanding | Must enumerate kinds | Must enumerate kinds | Semantic similarity |
| Nested JSON status | Complex query | Complex query | Embedded in vectors |
| New resource types | Must update prompts | Must update prompts | Automatic |
| Failure mode | Wrong data / SQL error | Wrong data / query error | Less relevant results |
| Query complexity | AI generates SQL | AI generates MongoDB query | AI passes natural text |

**Key insight**: "Database" is a semantic concept, not a schema value. A StatefulSet running PostgreSQL, a CNPG Cluster, and an RDS Instance are all "databases" - but only embeddings understand this relationship without explicit enumeration.

**Hybrid approach**: Semantic search finds resources; Kubernetes API provides details.
```
User: "show me failing databases"
  → Qdrant: semantic search finds matching resources
  → Return: summary list with basic status

User: "tell me more about prod-db"
  → Kubernetes API: kubectl get/describe equivalent
  → Return: full spec, events, conditions, etc.
```

This gives us the best of both worlds: fuzzy intent-based discovery (Qdrant) + precise structured data when needed (Kubernetes API). We don't need SQL for structured queries because we go directly to the source of truth.

### Dual-Collection Semantic Search

Both **capabilities** and **resources** are independent collections with their own embeddings. The LLM decides which to query based on user intent.

```
┌─────────────────────────────────────────────────────────────┐
│                       MCP Server                             │
│                                                              │
│  ┌────────────────────────┐    ┌──────────────────────────┐ │
│  │ search-capabilities    │    │ search-resources         │ │
│  │ query-capabilities     │    │ query-resources          │ │
│  └───────────┬────────────┘    └─────────────┬────────────┘ │
└──────────────┼───────────────────────────────┼──────────────┘
               │                               │
               ▼                               ▼
   ┌───────────────────────┐     ┌───────────────────────────┐
   │    Capabilities       │     │    Resources              │
   │                       │     │                           │
   │  What CAN be deployed │     │  What IS running          │
   │  Rich semantic metadata│     │  Factual data + status   │
   │  (DB, database, postgres,│   │  (kind, namespace, health,│
   │   postgresql, sql...)  │     │   labels...)             │
   └───────────────────────┘     └───────────────────────────┘
```

#### Collection Purposes

| Collection | Primary Purpose | Embeddings Based On |
|------------|-----------------|---------------------|
| **Capabilities** | Catalog of what CAN be done (CRDs, resource types) | Rich semantic metadata: "DB, database, postgres, postgresql, sql, relational, storage..." |
| **Resources** | Catalog of what IS running (instances + status) | Factual data: kind, name, namespace, health, labels |

#### Query Routing (LLM Decides)

| User Intent | Collections Used | Rationale |
|-------------|------------------|-----------|
| "what database options do I have?" | capabilities | Asking about available types |
| "show me all failing resources" | resources | Asking about running instances |
| "can I deploy Redis here?" | capabilities | Asking about available types |
| "list all postgres instances" | **both** | Capabilities knows what "postgres" means (StatefulSet, CNPG, RDS, etc.); resources has actual instances |
| "why is my-db failing?" | resources | Asking about specific instance |

#### Example: "List all postgres instances"

PostgreSQL can be implemented as StatefulSet, CloudNativePG, AWS RDS, Azure Database, etc. The query uses both collections:

```
User: "list all postgres instances"
              │
              ├─────────────────────────────────────┐
              ▼                                     ▼
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  Capabilities                │    │  Resources                   │
│  "postgres" →                │    │  "postgres" →                │
│  - StatefulSet (labeled)     │    │  - my-postgres (CNPG)        │
│  - Cluster.cnpg.io           │    │  - prod-db (RDS)             │
│  - RDSInstance.aws...        │    │  - analytics-pg (StatefulSet)│
│  - AzurePostgres...          │    │                              │
│                              │    │  (actual running instances)  │
│  (what COULD be postgres)    │    │                              │
└──────────────────────────────┘    └──────────────────────────────┘
              │                                     │
              └─────────────┬───────────────────────┘
                            ▼
                   LLM combines results
```

**Why both?**
- A StatefulSet running postgres has kind=StatefulSet, not "postgres" - only labels/image reveal it
- Capabilities knows "postgres includes these GVKs"
- Resources has the actual instances with their status
- Together: complete picture of what "postgres" means in this cluster

### Data Model

Each resource is stored with:

```json
{
  "id": "default/apps/v1/Deployment/nginx",
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
      {
        "type": "Available",
        "status": "True",
        "lastTransitionTime": "2025-12-18T10:00:00Z",
        "reason": "MinimumReplicasAvailable",
        "message": "Deployment has minimum availability."
      },
      {
        "type": "Progressing",
        "status": "True",
        "lastTransitionTime": "2025-12-18T09:55:00Z",
        "reason": "NewReplicaSetAvailable",
        "message": "ReplicaSet \"nginx-abc123\" has successfully progressed."
      }
    ],
    "observedGeneration": 5
  },
  "labels": {"app": "nginx", "env": "prod"},
  "annotations": {"description": "Web server"},
  "createdAt": "2025-12-13T10:00:00Z",
  "updatedAt": "2025-12-13T10:05:00Z"
}
```

**Key principles**:
- **Complete status stored** - entire `status` object from the resource, including timestamps
- **Always overwrite** - no history accumulation, Qdrant stores current state (same as Kubernetes)
- **Timestamps preserved** - useful for queries like "resources that changed in the last hour"
- **Full resource spec NOT stored** - only metadata + status (enough to find and filter)
- **On-demand details** - full resource spec fetched from Kubernetes API when user needs more

### Controller Data Flow

The controller uses informer watch events (not Kubernetes Event resources) to detect changes:

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Controller Startup                           │
├─────────────────────────────────────────────────────────────────────┤
│  1. Discover all resource types via Discovery API                    │
│  2. Create dynamic informer for each GVR                            │
│  3. List & send all existing resources to MCP (initial sync)        │
│  4. Start watching with event handlers                               │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      Informer Event Handlers                         │
├─────────────────────────────────────────────────────────────────────┤
│  OnAdd:    Extract relevant fields → queue upsert                   │
│  OnUpdate: Compare old/new relevant fields → queue upsert if changed│
│  OnDelete: Queue delete                                              │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Debounce Buffer (10 seconds)                      │
├─────────────────────────────────────────────────────────────────────┤
│  - Collect changes per resource ID                                   │
│  - Multiple updates → keep only last state                          │
│  - Delete always preserved (even if created in same window)         │
│  - On window close: batch all changes                               │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         Send to MCP                                  │
├─────────────────────────────────────────────────────────────────────┤
│  POST /resources/sync                                                │
│  {                                                                   │
│    "upserts": [{id, kind, namespace, name, status, labels, ...}],   │
│    "deletes": ["id1", "id2", ...]                                   │
│  }                                                                   │
│  MCP: generates embeddings, upserts/deletes in Qdrant               │
│  MCP: ignores "not found" errors on delete (idempotent)             │
└─────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    Periodic Resync (every hour)                      │
├─────────────────────────────────────────────────────────────────────┤
│  Controller: Lists all resources, sends full state to MCP           │
│  MCP: Fetches existing records from Qdrant                          │
│  MCP: Compares and applies diff:                                    │
│    - In incoming, not in Qdrant → Insert (new resource)             │
│    - In both, different → Update (changed resource)                 │
│    - In Qdrant, not in incoming → Delete (resource removed)         │
│  Result: Eventual consistency - drift self-corrects                 │
└─────────────────────────────────────────────────────────────────────┘
```

#### Change Detection

**Simple approach**: Compare complete `status` object (including timestamps) and `labels`.

```go
func hasRelevantChanges(oldObj, newObj *unstructured.Unstructured) bool {
    // Compare labels
    if !reflect.DeepEqual(oldObj.GetLabels(), newObj.GetLabels()) {
        return true
    }
    // Compare complete status (including timestamps)
    oldStatus := oldObj.Object["status"]
    newStatus := newObj.Object["status"]
    return !reflect.DeepEqual(oldStatus, newStatus)
}
```

**Why include timestamps?**
- Kubernetes doesn't keep history - status is always current state
- We always overwrite in Qdrant - no accumulation
- Timestamps are useful for queries ("show resources changed in last hour")
- Simpler code - no stripping logic
- Debouncing handles any extra update volume

**Ignored fields** (not part of status):
- `resourceVersion` - changes on every update, not meaningful
- `managedFields` - internal bookkeeping

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
| **Dynamic resource discovery** | Use discovery API to find all resource types in cluster | Medium |
| **Dynamic informer management** | Create/manage informers for each discovered GVR | Medium |
| **Change detection** | Compare old/new objects for relevant field changes | Low |
| **Debounce buffer** | Collect changes per resource ID, last-state-wins, 10s window | Medium |
| **MCP HTTP client** | Send batched upserts/deletes to MCP endpoint | Low |
| **Initial sync** | List all resources on startup, send to MCP | Low |
| **Periodic resync** | Send full state to MCP every hour | Low |
| **Resource sync controller** | New controller orchestrating the above | Medium |
| **Configuration** | MCP endpoint, batch settings | Low |

> **Note**: Controller does NOT handle embeddings or Qdrant directly. It sends resource data to MCP via HTTP. MCP handles embeddings, diffing, and Qdrant writes. This keeps the controller simple and consistent with how capabilities work.

---

## Scope

### In Scope

**Phase 1: Controller (dot-ai-controller)**
- New `ResourceSync` controller leveraging existing patterns
- Dynamic resource discovery via Discovery API
- Dynamic informer creation for each resource type
- Informer event handlers (OnAdd, OnUpdate, OnDelete)
- Change detection comparing complete status + labels
- Debounce buffer with 10-second window, last-state-wins
- HTTP client to send batched data to MCP
- Initial sync on startup (list all, send to MCP)
- Periodic resync every hour (full state to MCP, configurable)
- Configuration via Helm values (MCP endpoint, timings)

**Phase 2: MCP Integration (dot-ai) - Separate PRD**
- HTTP endpoint to receive resource data from controller
- Embedding generation for resources
- Qdrant upsert/delete operations
- Diff logic for resync: insert new, update changed, delete missing
- Idempotent delete handling (ignore not-found errors)
- Query interface for cluster-wide searches
- On-demand resource detail fetching

### Out of Scope

- Kubernetes Events resource syncing (high volume, low signal)
- Full resource spec storage (only metadata + status)
- Resource modification through the interface
- **Fleet-wide (multi-cluster) visibility** - Deferred to future PRD after:
  - Validating single-cluster value proposition
  - Determining optimal architecture (central MCP vs distributed)
  - Assessing impact on existing MCP tools (recommend, remediate, operate)

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

#### 2. Status Extraction (Simplified)

```go
// Extract complete status as-is - no selective field picking
func (r *ResourceSyncReconciler) extractResourceData(obj *unstructured.Unstructured) *ResourceData {
    return &ResourceData{
        ID:         buildResourceID(obj),
        Namespace:  obj.GetNamespace(),
        Name:       obj.GetName(),
        Kind:       obj.GetKind(),
        APIVersion: obj.GetAPIVersion(),
        Labels:     obj.GetLabels(),
        Status:     obj.Object["status"],  // Complete status, including timestamps
        CreatedAt:  obj.GetCreationTimestamp().Time,
    }
}
```

**Key insight**: Store the complete status object. No need to cherry-pick fields - works for any resource type (including custom resources with unique status structures).

#### 3. Change Detection (Simplified)

```go
// Compare old and new objects - simple deep equal on status and labels
func hasRelevantChanges(oldObj, newObj *unstructured.Unstructured) bool {
    // Compare labels
    if !reflect.DeepEqual(oldObj.GetLabels(), newObj.GetLabels()) {
        return true
    }

    // Compare complete status (including timestamps)
    oldStatus := oldObj.Object["status"]
    newStatus := newObj.Object["status"]
    return !reflect.DeepEqual(oldStatus, newStatus)
}
```

**Why this is better than selective field comparison**:
- Works for ANY resource type (Deployments, Pods, custom CRs)
- No need to know status structure upfront
- Simpler code, fewer edge cases
- Timestamps included = useful for time-based queries

#### 4. Debounce Buffer (Last-State-Wins)

```go
// Adapt from remediationpolicy_ratelimit.go patterns
type DebounceBuffer struct {
    changes     map[string]*ResourceChange  // keyed by resource ID
    mu          sync.Mutex
    window      time.Duration               // e.g., 10 seconds
    mcpClient   *MCPClient
}

type ResourceChange struct {
    Action Action         // Upsert or Delete
    Data   *ResourceData  // nil for deletes
}

func (b *DebounceBuffer) Record(id string, action Action, data *ResourceData) {
    b.mu.Lock()
    defer b.mu.Unlock()

    if action == Delete {
        // Delete always wins and is always preserved
        b.changes[id] = &ResourceChange{Action: Delete, Data: nil}
    } else if existing, ok := b.changes[id]; !ok || existing.Action != Delete {
        // Upsert: keep latest state (unless already marked for delete)
        b.changes[id] = &ResourceChange{Action: Upsert, Data: data}
    }
}

func (b *DebounceBuffer) Run(ctx context.Context) {
    ticker := time.NewTicker(b.window)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            b.flush(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (b *DebounceBuffer) flush(ctx context.Context) {
    b.mu.Lock()
    if len(b.changes) == 0 {
        b.mu.Unlock()
        return
    }

    // Collect changes
    var upserts []*ResourceData
    var deletes []string
    for id, change := range b.changes {
        if change.Action == Delete {
            deletes = append(deletes, id)
        } else {
            upserts = append(upserts, change.Data)
        }
    }
    b.changes = make(map[string]*ResourceChange)
    b.mu.Unlock()

    // Send to MCP
    b.mcpClient.SyncResources(ctx, upserts, deletes)
}
```

#### 5. MCP HTTP Client

```go
type MCPClient struct {
    endpoint   string
    httpClient *http.Client
}

type SyncRequest struct {
    Upserts  []*ResourceData `json:"upserts,omitempty"`
    Deletes  []string        `json:"deletes,omitempty"`
    IsResync bool            `json:"isResync,omitempty"`  // true for periodic full resync
}

func (c *MCPClient) SyncResources(ctx context.Context, upserts []*ResourceData, deletes []string) error {
    req := SyncRequest{
        Upserts: upserts,
        Deletes: deletes,
    }
    return c.post(ctx, "/resources/sync", req)
}

func (c *MCPClient) Resync(ctx context.Context, allResources []*ResourceData) error {
    req := SyncRequest{
        Upserts:  allResources,
        IsResync: true,  // MCP will diff against Qdrant and handle deletions
    }
    return c.post(ctx, "/resources/sync", req)
}
```

> **Design Decision**: Controller sends resource data to MCP via HTTP. MCP handles embeddings, diffing (for resync), and Qdrant writes. This keeps the controller simple and consistent with how capabilities work. MCP ignores "not found" errors on delete (idempotent).

#### 6. API Contract: Resource Sync Endpoint

**Endpoint**: `POST /api/v1/resources/sync`

**Request Body**:
```json
{
  "upserts": [
    {
      "id": "default:apps/v1:Deployment:nginx",
      "namespace": "default",
      "name": "nginx",
      "kind": "Deployment",
      "apiVersion": "apps/v1",
      "labels": {"app": "nginx", "env": "prod"},
      "annotations": {"description": "Web server"},
      "status": {
        "availableReplicas": 3,
        "readyReplicas": 3,
        "conditions": [
          {
            "type": "Available",
            "status": "True",
            "lastTransitionTime": "2025-12-18T10:00:00Z",
            "reason": "MinimumReplicasAvailable",
            "message": "Deployment has minimum availability."
          }
        ]
      },
      "createdAt": "2025-12-13T10:00:00Z",
      "updatedAt": "2025-12-18T14:30:00Z"
    }
  ],
  "deletes": ["default:apps/v1:Deployment:old-nginx"],
  "isResync": false
}
```

**Response Body** (follows MCP's `RestApiResponse` pattern):

Success:
```json
{
  "success": true,
  "data": {
    "upserted": 5,
    "deleted": 2
  },
  "meta": {
    "timestamp": "2025-12-18T14:30:00Z",
    "requestId": "abc-123",
    "version": "1.0.0"
  }
}
```

Error (partial or complete failure):
```json
{
  "success": false,
  "error": {
    "code": "SYNC_FAILED",
    "message": "Failed to process some resources",
    "details": {
      "upserted": 4,
      "deleted": 2,
      "failures": [
        {"id": "default:apps/v1:Deployment:nginx", "error": "embedding failed"}
      ]
    }
  },
  "meta": {
    "timestamp": "2025-12-18T14:30:00Z",
    "requestId": "abc-123",
    "version": "1.0.0"
  }
}
```

**Key Design Decisions**:
- **ID format**: `namespace:apiVersion:kind:name` - colons avoid URL path conflicts
- **Response wrapper**: Follows MCP's standard `RestApiResponse` pattern for consistency
- **Partial failures**: Reported in `error.details` with `success: false`
- **Idempotent deletes**: MCP ignores "not found" errors silently

#### 7. Configuration (Adapt Existing Pattern)

```yaml
# Helm values - similar to existing MCP endpoint config
resourceSync:
  enabled: true
  mcp:
    endpoint: "https://mcp.example.com"
    apiKeySecretRef:
      name: "mcp-credentials"
      key: "api-key"
  debounce:
    windowSeconds: 10          # Collect changes for 10 seconds before sending
  resync:
    intervalMinutes: 60        # Full resync every hour (conservative default, adjustable)
  discovery:
    intervalMinutes: 5         # Re-discover resource types every 5 minutes (for new CRDs)
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

- [x] **M1: Resource discovery and dynamic informers**
  - Implement dynamic resource discovery using discovery API
  - Create dynamic informers for each discovered GVR
  - ~~Periodic re-discovery for new CRDs~~ → **Improved**: CRD watching via informer for immediate detection
  - Handle CRD removal gracefully
  - **Added**: `ResourceSyncConfig` CRD for CR-based configuration (mcpEndpoint, debounceWindowSeconds, resyncIntervalMinutes)

- [x] **M2: Event handlers and change detection**
  - Implement OnAdd, OnUpdate, OnDelete handlers
  - Change detection comparing complete status + labels (simple deep equal)
  - Extract complete resource data (kind, namespace, name, labels, full status)
  - Status includes all fields and timestamps (no stripping)
  - **Added**: `ResourceData` and `ResourceChange` types for structured data passing
  - **Added**: Buffered change queue (10,000 capacity) to pass changes to debounce buffer (M3)
  - **Added**: Resource ID format: `namespace:apiVersion:kind:name` (e.g., `default:apps/v1:Deployment:nginx`)
  - **Added**: Cluster-scoped resources use `_cluster` as namespace prefix

- [x] **M3: Debounce buffer and MCP client**
  - Implement debounce buffer with 10-second window
  - Last-state-wins per resource ID
  - Deletes always preserved
  - HTTP client to send batched data to MCP
  - Retry logic with exponential backoff
  - **Added**: `resourcesync_mcp.go` - MCP client with `SyncRequest`/`SyncResponse` types, retry with exponential backoff
  - **Added**: `resourcesync_debounce.go` - Debounce buffer with configurable window, metrics tracking
  - **Added**: Safe channel closure handling to prevent panics during shutdown

- [x] **M4: Initial sync and periodic resync**
  - Initial sync on startup (list all resources, send to MCP)
  - Periodic resync every hour (default, configurable)
  - Controller sends full state; MCP diffs against Qdrant
  - MCP: insert new, update changed, delete missing (catches missed deletes)
  - **Added**: `listAllResources()` iterates all informer caches and extracts ResourceData
  - **Added**: `performResync()` sends all resources to MCP with `isResync: true`
  - **Added**: `performInitialSync()` runs after cache sync completes, updates status
  - **Added**: `periodicResyncLoop()` goroutine with configurable interval from `ResyncIntervalMinutes`
  - **Added**: Status updates for `LastResyncTime`, `TotalResourcesSynced`, `SyncErrors`

- [x] **M5: Configuration and Helm chart**
  - Added `ResourceSyncConfig` CRD to Helm chart templates
  - Updated `manager-role.yaml` with resourcesyncconfigs RBAC (resources, status, finalizers)
  - Configuration via CRD (same pattern as RemediationPolicy) - no Helm values needed
  - Users create `ResourceSyncConfig` CR after install to enable feature

- [ ] **M6: Testing and documentation**
  - Unit tests for change detection, debounce buffer
  - Integration tests with envtest
  - E2E tests with Kind cluster and mock MCP
  - Update Helm chart documentation

### Phase 2: MCP Integration (dot-ai) - Tracked in Separate PRD

- [ ] **M7: Resource sync endpoint**
  - HTTP endpoint to receive resource data from controller
  - Embedding generation for resources
  - Qdrant upsert/delete operations
  - Diff logic for resync: insert new, update changed, delete missing
  - Idempotent delete handling (ignore not-found errors)

- [ ] **M8: Query tools**
  - `search-resources` semantic search tool
  - `query-resources` structured query tool
  - On-demand resource detail fetching (call Kubernetes API for full spec/describe)

### Future: Fleet Expansion (Separate PRD)

- [ ] **M9: Multi-cluster architecture design**
  - Evaluate central MCP vs MCP-per-cluster
  - Evaluate central Qdrant vs Qdrant-per-cluster with federation
  - Assess impact on existing MCP tools (recommend, remediate, operate)
  - Design credential management for remote clusters
  - Design cross-cluster query routing

---

## Dependencies

### Phase 1 (Controller)
- Existing controller-runtime setup
- `k8s.io/client-go/dynamic` - Dynamic client for arbitrary resources
- `k8s.io/client-go/discovery` - Discovery API for resource types
- `k8s.io/client-go/dynamic/dynamicinformer` - Dynamic informer factory
- Existing HTTP client patterns from RemediationPolicy
- MCP endpoint available (Phase 2 dependency)

### Phase 2 (MCP)
- Phase 1 complete (controllers sending data)
- Existing Qdrant integration in dot-ai
- Existing embedding generation in dot-ai
- MCP tools: `search-resources`, `query-resources` (parallel to existing capabilities tools)

---

## Risks and Mitigations

| Risk | Mitigation | Existing Pattern |
|------|------------|------------------|
| API server overload | Shared informers, configurable resync | Controller-runtime standard |
| MCP overload | Debouncing, batching, change detection filtering | Adapt rate limiting |
| Memory exhaustion | Resource limits, selective caching | Existing controller limits |
| Stale data | Periodic resync (eventual consistency) | Adapt event deduplication |
| Credential security | Secret references, not inline | Existing SecretReference pattern |
| MCP unavailable | Retry with backoff, queue pending changes | Existing HTTP client patterns |
| High change volume | Aggressive filtering (relevant fields only), debouncing | New pattern |
| CRD churn | Periodic re-discovery, graceful informer management | New pattern |

---

## Open Questions

1. **Resource type filtering**: Should we allow excluding certain types (e.g., Secrets)?
2. **Namespace filtering**: Watch all namespaces or configurable subset?
3. **Solution CR integration**: Should we automatically create Solution CRs for discovered resource groups?

---

## Decision Log

| Decision | Rationale |
|----------|-----------|
| **Semantic search over query languages** - Use Qdrant vector search instead of relational DB (PostgreSQL) or document DB (MongoDB) with AI-generated queries. | "Database" is a semantic concept - embeddings understand it without enumeration. Text-to-query is ~85% accurate on clean schemas; Kubernetes JSON is messier. Wrong queries fail silently; semantic search degrades gracefully. New resource types work automatically. |
| **Dual-collection semantic search** - Capabilities (what CAN be deployed) and Resources (what IS running) are independent collections with embeddings. LLM decides which to query. | Enables direct semantic search for status queries. Capabilities provides semantic knowledge for resource types. LLM handles query routing without hardcoded logic. |
| **Controller sends HTTP to MCP** - Controller watches and sends data; MCP handles embeddings, Qdrant writes, and diffing. | Consistent with capabilities pattern. Keeps controller simple. Single place for embedding/Qdrant logic. |
| **Qdrant is search index, not source of truth** - Kubernetes etcd remains authoritative. Full details fetched on-demand from Kubernetes API. | Keeps Qdrant lean and sync fast. Avoids data duplication. Kubernetes is always authoritative. |
| **Single-cluster scope first** - Defer fleet-wide visibility to a future PRD. | MCP currently assumes single cluster. Fleet expansion requires broader architectural changes. Validate single-cluster value first. |
| **Informer watch events** - Use OnAdd/OnUpdate/OnDelete handlers with dynamic informers per resource type. | Standard Kubernetes pattern. Efficient change detection. Handles all resource types dynamically. |
| **Complete status storage** - Store entire status object including timestamps. Simple deep-equal for change detection. | Works for ANY resource type without knowing status structure. Simpler code. Timestamps useful for time-based queries. |
| **Debounce buffer (10s, last-state-wins)** - Collect changes per resource ID. Deletes always preserved. | Reduces HTTP call volume. Handles rapid event bursts. Simple implementation. |
| **Periodic resync (1 hour default)** - Controller sends full state; MCP diffs against Qdrant. | Safety net for eventual consistency. Diff approach minimizes embedding regeneration cost. Catches missed deletes. |
| **Idempotent deletes** - Always send deletes to MCP. MCP ignores "not found" errors. | Simpler logic. MCP handles idempotency. |
| **CR-based configuration** - New `ResourceSyncConfig` CRD (cluster-scoped) enables/configures resource syncing. Controller is dormant until CR exists. | Same pattern as RemediationPolicy. Dynamic enablement without restart. GitOps-friendly. Multiple configs supported. |
| **CRD watching instead of polling** - Watch CRDs directly via informer for immediate detection of new/removed custom resources. | Immediate response (seconds vs 5 minutes). More efficient than polling. Simpler code (no periodic goroutine). |
| **Shared SecretReference type** - Moved `SecretReference` to `common_types.go` for reuse across CRDs. | Avoids duplication. Consistent pattern across RemediationPolicy and ResourceSyncConfig. |
| **API contract for resource sync** - `POST /api/v1/resources/sync` with `RestApiResponse` wrapper pattern. ID format `namespace:apiVersion:kind:name`. | Consistent with MCP's existing API patterns. Colons in ID avoid URL path conflicts. Partial failures reported in `error.details`. |

---

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-13 | PRD created |
| 2025-12-18 | Design finalized: single-cluster scope, semantic search architecture, controller-MCP-Qdrant flow |
| 2025-12-18 | Implementation started - created feature branch `feature/prd-28-resource-visibility` |
| 2025-12-18 | **M1 Complete**: Resource discovery and dynamic informers. Created `ResourceSyncConfig` CRD, `resourcesync_controller.go`, shared `common_types.go`. Improved design: CRD watching via informer instead of polling for immediate detection of new CRDs. |
| 2025-12-18 | **M2 Complete**: Event handlers and change detection. Implemented `makeOnAdd`, `makeOnUpdate`, `makeOnDelete` handlers with `hasRelevantChanges()` for filtering. Added `ResourceData`/`ResourceChange` types, buffered change queue (10K), and resource ID format `namespace:apiVersion:kind:name`. Comprehensive unit tests added (75.8% coverage). |
| 2025-12-18 | **API Contract Defined**: Documented `POST /api/v1/resources/sync` endpoint contract aligned with MCP's `RestApiResponse` pattern. Ready for M3 implementation (controller side) and M7 implementation (MCP side). |
| 2025-12-18 | **M3 Complete**: Debounce buffer and MCP client. Created `resourcesync_mcp.go` (MCP client with retry logic, request/response types) and `resourcesync_debounce.go` (debounce buffer with configurable window, last-state-wins, metrics). Integrated with controller startup. Safe channel closure handling. Unit tests added (77.5% coverage). |
| 2025-12-18 | **M4 Complete**: Initial sync and periodic resync. Added `listAllResources()` to iterate informer caches, `performResync()` to send full state to MCP, `performInitialSync()` for startup sync after cache sync, and `periodicResyncLoop()` goroutine for configurable periodic resyncs. Status updates include `LastResyncTime`, `TotalResourcesSynced`, `SyncErrors`. Unit tests added with mock informers (74.8% coverage). |
| 2025-12-19 | **M5 Complete**: Configuration and Helm chart. Added `ResourceSyncConfig` CRD to `charts/dot-ai-controller/templates/`. Updated `manager-role.yaml` with resourcesyncconfigs RBAC. Follows same pattern as RemediationPolicy - CRD-based config, users create CR after install. All tests pass (74.8% coverage). |

---

## References

- [dot-ai-controller Repository](https://github.com/vfarcic/dot-ai-controller)
- [dot-ai Repository](https://github.com/vfarcic/dot-ai)
- [Existing Solution Controller](../internal/controller/solution_controller.go) - Status extraction patterns
- [Existing RemediationPolicy Controller](../internal/controller/remediationpolicy_controller.go) - Event watching patterns
- [Qdrant Go Client](https://github.com/qdrant/go-client)
