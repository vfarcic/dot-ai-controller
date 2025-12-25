# PRD: Autonomous Capability Scanning Controller

**Issue**: #34
**Status**: Complete
**Priority**: High
**Created**: 2025-12-24
**Last Updated**: 2025-12-25
**Completed**: 2025-12-25

## Related Context

This PRD implements **Phase 2** of [vfarcic/dot-ai PRD #216](https://github.com/vfarcic/dot-ai/blob/main/prds/216-controller-based-autonomous-capability-scanning.md). Phase 1 (fire-and-forget scan API) has been completed in the main dot-ai repository.

## Problem Statement

Capability scanning in DevOps AI Toolkit currently requires manual initiation via MCP server calls. This creates several operational challenges:

1. **Manual Overhead**: Users must explicitly trigger capability scans when cluster resources change
2. **Stale Data**: Capability data can become outdated when CRDs or resource definitions are added/removed
3. **Discovery Lag**: New operators or custom resources aren't automatically discovered
4. **Operational Burden**: DevOps teams must remember to trigger scans after cluster changes

This reactive scanning model doesn't align with Kubernetes' event-driven architecture and creates unnecessary friction in maintaining accurate capability data.

## Solution Overview

Deploy a Kubernetes controller that autonomously manages capability scanning by:

1. **Startup Reconciliation**: On startup, compare cluster CRDs with MCP capabilities and sync differences (full scan only if MCP is empty; otherwise targeted scan for missing CRDs and cleanup of orphaned capabilities)
2. **Event-Driven Updates**: Watch Kubernetes API for CRD and resource definition changes (create/update/delete events)
3. **MCP Coordination**: Send HTTP requests to MCP server's `manageOrgData` tool to scan or remove capabilities
4. **Resilient Operation**: Retry failed operations and expose metrics for observability

**Key Design Principle**: The controller is a coordinator, not a scanner. All actual scanning logic remains in the MCP server - the controller simply triggers it at the right times.

## Architecture

```
┌─────────────────────────────────────┐
│  Kubernetes Cluster                 │
│                                     │
│  ┌──────────────────────┐          │
│  │  dot-ai-controller   │          │
│  │                      │          │
│  │  1. Watch API Events │          │
│  │  2. HTTP Client      │──────┐   │
│  │  3. Retry Logic      │      │   │
│  └──────────────────────┘      │   │
│           │                    │   │
│           │ watches            │   │
│           ↓                    │   │
│  ┌──────────────────────┐     │   │
│  │  Kubernetes API      │     │   │
│  │  - CRDs              │     │   │
│  │  - Built-in Resources│     │   │
│  └──────────────────────┘     │   │
│                                │   │
│  ┌──────────────────────┐     │   │
│  │  dot-ai MCP Server   │◄────┘   │
│  │  HTTP Endpoints      │         │
│  │  /api/v1/tools/      │         │
│  │    manageOrgData     │         │
│  └──────────────────────┘         │
└─────────────────────────────────────┘
```

## User Experience

### Installation Flow

1. **Deploy MCP Server** (existing):
   ```bash
   helm install dot-ai oci://ghcr.io/vfarcic/dot-ai/helm/dot-ai
   ```

2. **Enable Controller** (new, optional):
   ```bash
   helm install dot-ai oci://ghcr.io/vfarcic/dot-ai/helm/dot-ai \
     --set controller.enabled=true \
     --set controller.mcp.endpoint=http://dot-ai-mcp:8080
   ```

3. **Configure Scanning** (via Custom Resource):
   ```yaml
   apiVersion: dot-ai.io/v1alpha1
   kind: CapabilityScanConfig
   metadata:
     name: default-scan-config
   spec:
     mcp:
       endpoint: http://dot-ai-mcp:8080
       collection: capabilities  # Qdrant collection name

     # Resource filters
     includeResources:
       - "deployments.apps"
       - "*.crossplane.io"
       - "applications.argoproj.io"

     excludeResources:
       - "events.v1"
       - "*.internal.example.com"

     # Retry configuration
     retry:
       maxAttempts: 3
       backoffSeconds: 5
   ```

### Operational Workflow

**Day 1 - Initial Setup**:
1. User deploys Helm chart with controller enabled
2. Controller starts and checks MCP server for existing capabilities
3. If no capabilities exist, controller initiates full scan via MCP
4. Controller enters watch mode for future changes

**Day 2+ - Autonomous Operation**:
1. DevOps team installs Crossplane provider (e.g., `provider-aws`)
2. New CRDs are created (e.g., `RDSInstance.database.aws.crossplane.io`)
3. Controller detects CRD creation event
4. Controller sends fire-and-forget HTTP POST to MCP server
5. MCP server scans the new resource asynchronously and stores capability data
6. Users get recommendations for AWS RDS in their next deployment

**Resource Removal**:
1. Team uninstalls operator, CRD is deleted
2. Controller detects deletion event
3. Controller sends HTTP POST to MCP server to delete capability
4. MCP server removes capability data from database

## MCP Server API Reference

The MCP server exposes the following endpoints that the controller will use. These APIs were implemented in Phase 1 (PRD #216 in dot-ai repo).

### Check for Existing Capabilities

```json
POST /api/v1/tools/manageOrgData
{
  "dataType": "capabilities",
  "operation": "list",
  "limit": 10,
  "collection": "capabilities"
}

// Response:
{
  "success": true,
  "data": {
    "result": {
      "success": true,
      "capabilities": [...],
      "totalCount": 150
    }
  }
}
```

### Fire-and-Forget: Full Cluster Scan

Controller calls this on startup if no capabilities exist:

```json
POST /api/v1/tools/manageOrgData
{
  "dataType": "capabilities",
  "operation": "scan",
  "mode": "full",
  "collection": "capabilities"
}

// Response (immediate):
{
  "success": true,
  "data": {
    "result": {
      "success": true,
      "status": "started",
      "message": "Full capability scan initiated in background"
    }
  }
}
```

### Fire-and-Forget: Targeted Resource Scan

Controller calls this when CRD create/update events are detected:

```json
POST /api/v1/tools/manageOrgData
{
  "dataType": "capabilities",
  "operation": "scan",
  "resourceList": "RDSInstance.database.aws.crossplane.io,Bucket.s3.aws.crossplane.io",
  "collection": "capabilities"
}

// Response (immediate):
{
  "success": true,
  "data": {
    "result": {
      "success": true,
      "status": "started",
      "message": "Scan initiated for 2 resources"
    }
  }
}
```

**resourceList Format**:
- Comma-separated string of resources
- Grouped resources: `Kind.group` (e.g., `Deployment.apps`, `RDSInstance.database.aws.crossplane.io`)
- Core resources: `Kind` (e.g., `Service`, `ConfigMap`, `Pod`)

### Delete Capability

Controller calls this when CRD deletion events are detected:

```json
POST /api/v1/tools/manageOrgData
{
  "dataType": "capabilities",
  "operation": "delete",
  "id": "RDSInstance.database.aws.crossplane.io",
  "collection": "capabilities"
}

// Response:
{
  "success": true,
  "data": {
    "result": {
      "success": true,
      "operation": "delete",
      "message": "Capability deleted successfully"
    }
  }
}
```

**Capability ID Format**: The ID is the resource identifier - `Kind` for core resources, `Kind.group` for grouped resources.

### Get Specific Capability

```json
POST /api/v1/tools/manageOrgData
{
  "dataType": "capabilities",
  "operation": "get",
  "id": "Service",
  "collection": "capabilities"
}
```

## Technical Design

### Technology Stack

- **Language**: Go (standard for Kubernetes controllers)
- **Framework**: Kubebuilder / Controller Runtime
- **Client**: kubernetes/client-go
- **HTTP Client**: Standard Go net/http with retry logic

### Core Components

1. **Startup Reconciler**:
   - Check MCP server health
   - Query existing capabilities via `list` operation
   - If empty, trigger full scan via `mode: "full"`
   - Mark initialization complete

2. **Event Watcher**:
   - Watch all API resource types (CRDs + built-in)
   - Filter events based on `CapabilityScanConfig` include/exclude rules
   - Queue events for processing (rate-limited work queue)

3. **HTTP Client Manager**:
   - HTTP client pool for MCP server communication
   - Exponential backoff retry logic (configurable max attempts)
   - Request timeout handling
   - Error categorization (retryable vs. permanent failures)

4. **Observability**:
   - Prometheus metrics (scans triggered, failures, queue depth)
   - Structured logging (event details, retry attempts, outcomes)
   - Health endpoints for liveness/readiness probes

### Resource Filtering Logic

**Include/Exclude Processing**:
1. If `includeResources` is specified, only watch those patterns
2. Apply `excludeResources` as blacklist after includes
3. Patterns support wildcards: `*.crossplane.io`, `deployments.*`, `*`

**Example Configuration**:
```yaml
includeResources:
  - "*.crossplane.io"      # All Crossplane resources
  - "applications.*"        # All application CRDs (any group)
  - "deployments.apps"      # Specific built-in resource

excludeResources:
  - "events.*"              # Ignore all events
  - "*.internal.example.com" # Ignore internal CRDs
```

**Default Behavior** (no filters specified):
- Watch all API resources
- Exclude known high-volume resources: `events`, `leases`, `endpoints`

### Error Handling & Retry

**Retry Strategy**:
```go
type RetryConfig struct {
    MaxAttempts     int           // Default: 3
    InitialBackoff  time.Duration // Default: 5s
    MaxBackoff      time.Duration // Default: 5m
    BackoffFactor   float64       // Default: 2.0 (exponential)
}

// Example retry sequence:
// Attempt 1: immediate
// Attempt 2: after 5s
// Attempt 3: after 10s
// Attempt 4: after 20s
// Give up after MaxAttempts
```

**Error Categories**:
- **Retryable**: Network errors, 500/503 from MCP, timeouts
- **Non-Retryable**: 400 Bad Request, 404 Not Found, invalid resource format
- **Permanent Failure**: After max retry attempts, log error and expose metric

**Recovery via Startup Reconciliation**: Failed events are automatically recovered on pod restart via diff-and-sync reconciliation (compares cluster CRDs with MCP capabilities and syncs differences).

## Milestones

### Milestone 1: Controller Foundation
**Deliverable**: Working controller that watches API resources and logs events

- [x] Implement Kubebuilder scaffold with CRD for `CapabilityScanConfig`
- [x] Build event watcher for all API resources (CRDs + built-in types)
- [x] Implement include/exclude filtering logic with wildcard support
- [x] Add structured logging following project patterns (metrics deferred - project has no custom Prometheus metrics yet)

**Success Criteria**:
- Controller deploys successfully in test cluster
- Logs show detection of CRD create/update/delete events
- Filtering rules correctly include/exclude resources based on config
- No crashes or memory leaks during 24-hour run

### Milestone 2: MCP Server Integration
**Deliverable**: Controller successfully communicates with MCP server

- [x] Implement HTTP client with retry logic (exponential backoff)
- [x] Build startup reconciler that checks for existing capabilities via `list` operation
- [x] Implement fire-and-forget scan triggering via `resourceList` parameter
- [x] Implement fire-and-forget full scan via `mode: "full"` on startup if empty
- [x] Implement delete operation for removed resources
- [x] Add integration tests with mock MCP server

**Success Criteria**:
- Controller successfully queries MCP for existing capabilities on startup
- Controller triggers fire-and-forget scans for new CRDs (no polling)
- Retry logic recovers from transient MCP server failures
- Delete operations successfully remove capabilities when CRDs are deleted

### Milestone 3: Resilience & Observability
**Deliverable**: Production-ready controller with full observability

- [x] Implement debounce buffer for batching CRD events (configurable window, reduces MCP calls)
- [~] Add Prometheus metrics (scans triggered, success/failure rates, queue depth) - deferred: project has no custom Prometheus metrics pattern yet
- [~] Implement health endpoints (liveness, readiness) with proper checks - not applicable: manager already provides health endpoints; adding MCP-specific checks would affect unrelated controllers
- [~] Add dead letter queue logging for permanent failures - not needed: startup reconciliation recovers failed operations on restart
- [~] Create runbook documentation for common failure scenarios - deferred: will be covered by user-facing troubleshooting in capability-scan-guide.md

**Success Criteria**:
- Controller handles MCP server downtime gracefully (queues events, retries)
- Failed events are logged with sufficient detail for debugging

### Milestone 4: Helm Chart & Release
**Deliverable**: Controller deployable via its own Helm chart (following existing pattern)

- [x] Add capability scanning controller to existing dot-ai-controller Helm chart - automated: CI copies CRDs from `config/crd/bases/` to Helm chart on merge
- [~] Implement chart values for capability scanning configuration (endpoint, filters, retry) - not needed: configuration done via CapabilityScanConfig CR, not Helm values
- [x] Add default `CapabilityScanConfig` CRD and resource to chart - automated: CI copies CRD on merge
- [x] Configure RBAC permissions for watching API resources - automated: kubebuilder markers generate RBAC, CI syncs to Helm chart
- [~] Test installation alongside existing controller features - deferred: will be validated post-release in real cluster alongside new MCP version

**Success Criteria**:
- Controller installed separately: `helm install dot-ai-controller oci://ghcr.io/vfarcic/dot-ai-controller/charts/dot-ai-controller`
- ~~Capability scanning configuration manageable through Helm values~~ Configuration via CapabilityScanConfig CR
- RBAC permissions correctly scoped (read API resources, no write permissions)
- Works alongside existing Solution CR controller functionality

### Milestone 5: Documentation & Testing
**Deliverable**: Complete documentation and end-to-end testing

- [x] Create user guide for CapabilityScan feature (docs/capability-scan-guide.md)
- [~] Add architecture diagrams showing controller/MCP interaction - deferred: architecture in PRD sufficient for now
- [x] Write end-to-end tests for CapabilityScanConfig (CRUD, status updates, CRD event detection)
- [~] Create troubleshooting guide with common issues and solutions - deferred: requires real-world usage to identify issues
- [~] Add performance testing documentation (resource usage, scaling limits) - deferred: requires real-world usage data

**Success Criteria**:
- New users can deploy and configure controller following user guide
- ~~E2E tests validate complete workflow from CRD creation to capability availability~~ Post-release validation
- ~~Troubleshooting guide addresses failure scenarios discovered during testing~~ Post-release iteration
- ~~Performance characteristics documented (events/sec, memory usage)~~ Post-release iteration

## Success Metrics

**Operational Metrics**:
- **Scan Latency**: Time from CRD creation to capability availability in recommendations
  - Target: < 60 seconds for single resource scan
- **Error Rate**: Percentage of scan operations that fail permanently
  - Target: < 1% failure rate under normal conditions
- **Resource Efficiency**: Controller memory and CPU usage
  - Target: < 50MB memory, < 0.1 CPU cores at steady state

**User Experience Metrics**:
- **Setup Time**: Time to deploy and configure controller
  - Target: < 5 minutes using Helm chart
- **Discovery Accuracy**: Percentage of new CRDs that are automatically scanned
  - Target: 100% for included resources
- **Staleness Elimination**: Reduce manual scan triggers to zero

## Risks & Mitigation

### Risk 1: Event Storm Handling
**Risk**: Large cluster with frequent CRD changes could overwhelm controller/MCP server

**Mitigation**:
- Implement rate-limited work queue (configurable events/sec)
- Add batch scanning option for initial full scan
- Document resource requirements for large clusters

### Risk 2: MCP Server Availability
**Risk**: Controller depends on MCP server; downtime blocks capability updates

**Mitigation**:
- Implement persistent work queue that survives restarts
- Retry with exponential backoff prevents thundering herd
- Expose metrics showing queue depth and failure rates
- Document recovery procedures in runbook

### Risk 3: Partial Scans After Failures
**Risk**: Initial scan partially completes, leaving some resources unscanned

**Mitigation**:
- Track initial scan state in controller status condition
- Resume incomplete initial scan on restart
- Expose metric showing scan completion percentage
- Log detailed progress during initial scan

### Risk 4: Version Compatibility
**Risk**: Controller version may become incompatible with MCP server API

**Mitigation**:
- Document compatible version matrix (controller <-> MCP server)
- Use semantic versioning with clear API compatibility rules
- Test controller against multiple MCP server versions in CI
- Include version compatibility check in controller startup

## Dependencies

### Prerequisites
- **Phase 1 Complete**: Fire-and-forget API in dot-ai repo (completed in PRD #216)
- **MCP Server**: dot-ai MCP server deployed and accessible
- **Qdrant**: Vector database for capability storage

### External Dependencies
- Kubernetes cluster with API access (minimum version: 1.20+)
- MCP server deployed and accessible via cluster DNS
- RBAC permissions for controller ServiceAccount

### Related PRDs
- **dot-ai PRD #216**: Parent PRD with Phase 1 API implementation
- **dot-ai PRD #155**: Parallel Capability Analysis (future: controller could leverage)
- **dot-ai PRD #180**: Dynamic Credential Management (may inform auth design)

## Future Enhancements

**Advanced Features**:
1. **Scheduled Scanning**: Periodic full scans to detect drift
2. **Parallel Processing**: Integrate with PRD #155 for faster initial scans
3. **Multi-Cluster**: Watch multiple clusters from single controller instance
4. **Selective Scanning**: Fine-grained control over which resource fields to scan
5. **Webhook Integration**: Use ValidatingWebhook to scan resources before creation

**Intelligence Features**:
1. **Priority Scanning**: Scan frequently-used resources first based on recommendation history
2. **Change Detection**: Only re-scan resources when their schema actually changes
3. **Capability Prediction**: Pre-scan resources likely to be needed based on cluster patterns

## Open Questions

1. **Authentication**: Should controller authenticate to MCP server, or rely on network policies?
   - Current decision: Start without auth (internal cluster traffic), add optional auth later

2. **Initial Scan Scope**: Should initial scan exclude built-in Kubernetes resources unlikely to be used in recommendations?
   - Proposed: Add smart defaults that exclude high-volume, low-value resources

3. **Multi-Tenancy**: Should single controller handle multiple Qdrant collections?
   - Future consideration: Allow multiple `CapabilityScanConfig` resources with different collections

## References

- [Kubernetes Controller Pattern](https://kubernetes.io/docs/concepts/architecture/controller/)
- [Kubebuilder Book](https://book.kubebuilder.io/)
- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [dot-ai PRD #216: Controller-Based Autonomous Capability Scanning](https://github.com/vfarcic/dot-ai/blob/main/prds/216-controller-based-autonomous-capability-scanning.md)

---

## Progress Log

### 2025-12-24: PRD Created
- Initial PRD created based on Phase 2 context from dot-ai PRD #216
- 5 milestones defined for controller implementation
- API documentation reflects completed Phase 1 implementation

### 2025-12-24: Milestone 1 & 2 Implementation
- Created `CapabilityScanConfig` CRD with minimal status fields (removed counters per review - belong in Prometheus)
- Implemented controller using `Watches()` pattern for CRD events (following ResourceSyncConfig pattern)
- Built MCP client with exponential backoff retry logic and jitter
- Implemented include/exclude filtering with wildcard support (e.g., `*.crossplane.io`)
- Added comprehensive unit tests for pattern matching and config change detection
- Added MCP client tests using httptest (real HTTP server with controlled responses)
- All tests passing (unit + integration)

### 2025-12-24: Logging Alignment & Test Performance Fix
- Aligned capabilityscan controller logging with project patterns (✅ success, ❌ errors, ⚠️ warnings)
- Updated `capabilityscan_controller.go` and `capabilityscan_mcp.go` with emoji log messages
- Fixed bug: `MaxRetries: 0` was being overridden to `3` due to `<= 0` check
- Changed `MaxRetries` to pointer type (`*int`) in both MCP clients to distinguish "not set" from "set to 0"
- Updated all test files to use `ptr.To(0)` syntax
- Test performance improved from ~67s to ~37.5s (~45% faster)
- Milestone 1 complete (metrics deferred as project has no custom Prometheus metrics)

### 2025-12-24: Milestone 3 - Debounce Buffer Implementation
- Discussed whether work queue was needed; concluded MCP is fire-and-forget so traditional queue adds no value
- Decided on debounce buffer approach: collect CRD events over configurable window, send as single batched request
- Added `DebounceWindowSeconds` field to `CapabilityScanConfigSpec` (default: 10 seconds)
- Created `capabilityscan_debounce.go` with simple buffer implementation:
  - Collects CRD create/update events for scanning
  - Collects CRD delete events for capability removal
  - Flushes periodically, batching scans into comma-separated `resourceList`
  - Sends deletes individually (MCP API limitation)
- Modified controller to queue events to buffer instead of spawning goroutines per event
- Added comprehensive unit tests in `capabilityscan_debounce_test.go`
- Removed unused metrics from buffer (YAGNI - can add with Prometheus later)
- All tests passing (268/268)
- Milestone 3 first task complete

### 2025-12-24: Design Decision - Prometheus Metrics Deferred
- **Decision**: Defer Prometheus metrics implementation for CapabilityScan controller
- **Rationale**: Project has no custom Prometheus metrics in any existing controller (Solution, RemediationPolicy, ResourceSync). Adding metrics would establish a new pattern that should be done consistently across all controllers, not just this one.
- **Impact**: Milestone 3 "Add Prometheus metrics" task marked as deferred `[~]`
- **Future**: When the project decides to add Prometheus metrics, it should be done as a cross-cutting concern for all controllers simultaneously

### 2025-12-24: Design Decision - Health Endpoints Not Applicable
- **Decision**: Mark health endpoints task as not applicable for CapabilityScan controller
- **Rationale**: The controller-runtime manager already provides `/healthz` and `/readyz` endpoints at the Pod level. All controllers (Solution, RemediationPolicy, ResourceSyncConfig, CapabilityScanConfig) run in a single manager/Pod. Adding MCP-specific health checks would cause the entire Pod to become unhealthy when MCP is down, even though unrelated controllers (like Solution) would still function correctly.
- **Impact**: Milestone 3 "Implement health endpoints" task marked as not applicable `[~]`
- **Architecture Note**: Health is managed at the manager level, not per-controller. MCP connectivity issues should be surfaced via logs and status conditions, not health endpoints.

### 2025-12-24: Startup Reconciliation Implementation
- **Problem Identified**: Original design only triggered full scan if MCP had zero capabilities. If pod restarted after capabilities existed, any CRDs created during downtime would be missed.
- **Solution**: Implemented diff-and-sync reconciliation on every startup:
  1. List all CRDs in cluster (with include/exclude filters applied)
  2. List all capability IDs from MCP
  3. If MCP is empty → trigger full scan (fresh install case)
  4. Otherwise → compute diff:
     - CRDs in cluster but not in MCP → targeted scan
     - Capabilities in MCP but not in cluster → delete orphaned
  5. If both match → no action needed (already in sync)
- **Implementation Details**:
  - Added `ListCapabilityIDs()` method to MCP client (returns actual IDs, not just count)
  - Added `listClusterCRDIDs()` method to list cluster CRDs with filtering
  - Renamed `performInitialScan` to `performStartupReconciliation` with new logic
  - Added `computeCapabilityDiff()` helper for set difference calculations
  - Removed `InitialScanComplete` status check - now always reconciles on startup
- **Benefits**:
  - Handles pod restarts gracefully (no missed CRDs)
  - Avoids expensive full scans when only delta sync is needed
  - Full scan still used for fresh installs (MCP empty)
- **Tests**: Added comprehensive unit tests for `computeCapabilityDiff` and `ListCapabilityIDs`
- All tests passing

### 2025-12-24: Design Decision - Dead Letter Queue Not Needed
- **Decision**: Mark dead letter queue logging task as not needed
- **Rationale**: Startup reconciliation already provides recovery for failed operations. On pod restart, the controller compares cluster CRDs with MCP capabilities and syncs any differences. This means:
  - If a scan failed → startup reconciliation detects missing capability and re-triggers scan
  - If a delete failed → startup reconciliation detects orphaned capability and re-triggers delete
- **Impact**: Milestone 3 "Add dead letter queue logging" task marked as not needed `[~]`
- **Architecture Note**: Recovery via restart is simpler and more reliable than persistent DLQ

### 2025-12-24: Design Decision - Helm Chart Automation via CI
- **Decision**: Milestone 4 Helm chart tasks are mostly automated via existing CI pipeline
- **Rationale**: The release workflow already:
  1. Runs `make generate manifests` to generate CRDs and RBAC
  2. Copies all CRDs from `config/crd/bases/*.yaml` to Helm chart templates
  3. Copies RBAC from `config/rbac/role.yaml` to Helm chart (with templating)
- **Impact**:
  - "Add CRD to Helm chart" → automated via CI
  - "Configure RBAC permissions" → automated via CI (kubebuilder markers + CI sync)
  - "Implement Helm values for config" → not needed (config via CapabilityScanConfig CR)
- **Remaining**: Only "Test installation alongside existing controller features" needs manual verification

### 2025-12-24: Design Decision - Post-Release Validation Strategy
- **Decision**: Defer installation testing and most Milestone 5 items to post-release validation
- **Rationale**:
  - Controller and MCP server need to be released together for meaningful testing
  - Real-world cluster testing provides better validation than Kind-based pre-release testing
  - Troubleshooting guide and performance docs require real-world usage data to be useful
- **Impact**:
  - Milestone 4: "Test installation alongside existing controller features" → deferred to post-release
  - Milestone 5: E2E tests, troubleshooting guide, performance docs → deferred to post-release
  - Milestone 5: User guide (capability-scan-guide.md) → required for release
- **Workflow**: Release both controller and MCP → Install in real cluster → Analyze outcomes → Fix issues → Update docs

### 2025-12-25: Documentation Complete
- **Created** `docs/capability-scan-guide.md` - comprehensive user guide for CapabilityScan feature
- **Updated** `docs/index.md` - added CapabilityScanConfig section, updated architecture diagram
- **Updated** `docs/setup-guide.md` - added CapabilityScanConfig to features and CRDs lists
- **Updated** `docs/troubleshooting.md` - added section #8 for CapabilityScanConfig issues
- **Updated** `docs/remediation-guide.md`, `docs/solution-guide.md`, `docs/resource-sync-guide.md` - added cross-reference links to capability-scan-guide
- **Updated** `README.md` - added CapabilityScanConfig to CRD list, fixed MCP documentation link
- **Made** `authSecretRef` required in both CapabilityScanConfig and RemediationPolicy CRDs
- **Fixed** resource filtering to use Discovery API (applies to ALL resources, not just CRDs)
- **Fixed** `cmd/main.go` to pass `RestConfig` to CapabilityScanReconciler
- All tests passing

### 2025-12-25: E2E Test Implementation & Performance Optimization
- **E2E Tests Complete**: Created comprehensive e2e tests for CapabilityScanConfig
  - CRUD operations (create, update, delete)
  - Status updates (active watcher, conditions)
  - CRD event detection with debouncing
  - Mock server for MCP API simulation
- **Test Performance Optimizations**:
  - Reduced RemediationPolicy event filtering sleep from 30s to 5s
  - Reduced CapabilityScanConfig debounce window from 5s to 2s in tests
  - Changed ResourceSyncConfig mock server setup from BeforeEach to BeforeAll
  - Overall test execution time reduced from 310s to 252s (~19% improvement)
- All 30 e2e tests passing
