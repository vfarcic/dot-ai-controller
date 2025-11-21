# PRD: Solution CRD for Deployment Tracking

**Created**: 2025-11-21
**Status**: Draft
**Owner**: TBD
**Last Updated**: 2025-11-21
**Issue**: [#4](https://github.com/vfarcic/dot-ai-controller/issues/4)
**Priority**: High

## Executive Summary

Implement a Solution Custom Resource Definition (CRD) in `dot-ai-controller` to track deployed Kubernetes resources, store solution metadata, and provide context for future AI recommendations. This CRD acts as a parent resource that groups all Kubernetes resources composing a logical solution, preserving information not available in individual resources.

**⚠️ IMPORTANT**: dot-ai PRD #228 (Deployment Documentation & Example-Based Learning) depends on this PRD and cannot begin until the Solution CRD and controller are implemented.

## Problem Statement

### Current Challenges

When the DevOps AI Toolkit `recommend` tool deploys resources to a cluster, there is no persistent record of:

1. **Resource Grouping**: Which Kubernetes resources (Deployment, Service, PVC, etc.) compose a logical solution
2. **Original Intent**: User's intent that led to the deployment ("deploy PostgreSQL for analytics")
3. **Solution Metadata**: Information not available in individual resources:
   - Deployment rationale and decision-making context
   - Configuration trade-offs and choices
   - Documentation links (from dot-ai PRD #228)
   - Patterns and policies applied
4. **Parent-Child Relationships**: No explicit link between solution concept and its constituent resources
5. **Solution-Level Operations**: Cannot operate on a solution as a unit (query, update, delete related resources together)

### User Impact

- **Lost Context**: AI cannot learn from past deployments because there's no record of "why" solutions were deployed
- **Fragmented Resources**: Resources are scattered across the cluster with no logical grouping
- **Manual Tracking**: Users must manually remember which resources belong to which solution
- **Poor AI Recommendations**: Future recommendations lack context from similar past deployments
- **No Documentation Link**: Cannot associate deployment documentation with deployed resources

### Why This Matters for AI

The Solution CRD is **primarily for AI/MCP benefit**:
- Provides context for future `recommend` requests
- Enables learning from past deployment decisions
- Stores metadata that improves recommendation accuracy
- Links documentation to deployed resources for few-shot learning (dot-ai PRD #228)

## Solution Overview

### Core Concept

Create a Solution CRD that acts as a **parent resource** for all Kubernetes resources composing a logical solution, using Kubernetes' native `ownerReferences` pattern.

### Key Design Principles

1. **Kubernetes-Native**: Use `ownerReferences` for parent-child relationships
2. **Metadata Store**: Capture information NOT in individual resources
3. **Thin Controller**: Controller coordinates operations, delegates intelligence to MCP
4. **AI-Focused**: Primary benefit is providing context for AI/MCP tools
5. **Opt-In**: Only tracks new `recommend`-deployed solutions (no retrofitting existing apps)

### Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                 │
│                                                     │
│  ┌──────────────────────┐                          │
│  │  Solution CR         │  (Parent Resource)       │
│  │  ─────────────       │                          │
│  │  metadata:           │                          │
│  │    intent: "..."     │                          │
│  │    docURL: "..."     │                          │
│  │    rationale: "..."  │                          │
│  └──────────────────────┘                          │
│           ▲                                         │
│           │ ownerReferences                         │
│           │                                         │
│  ┌────────┴──────────┬──────────────┬─────────┐   │
│  │                   │              │         │   │
│  ▼                   ▼              ▼         ▼   │
│  Deployment      Service         PVC      ConfigMap│
│  (child)         (child)       (child)   (child)  │
│                                                     │
│  ┌──────────────────────────────────────┐          │
│  │  Solution Controller                 │          │
│  │  ───────────────────                 │          │
│  │  1. Watch Solution CRs               │          │
│  │  2. Reconcile state                  │          │
│  │  3. Call MCP for updates             │          │
│  │  4. Handle drift detection           │          │
│  └──────────────────────────────────────┘          │
│                                                     │
└─────────────────────────────────────────────────────┘
```

### Solution CRD Schema

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: payment-api-production
  namespace: production
spec:
  # Original user intent
  intent: "Deploy Go microservice with PostgreSQL database and Redis cache"

  # Solution metadata (not in individual resources)
  metadata:
    createdBy: "dot-ai-recommend-tool"
    rationale: "High availability deployment with persistent storage for PostgreSQL"
    patterns:
      - "High Availability Pattern"
      - "Stateful Storage Pattern"
    policies:
      - "production-security-policy"

  # Resource references (Option A from discussion)
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: payment-api
      namespace: production
    - apiVersion: apps/v1
      kind: StatefulSet
      name: postgresql
      namespace: production
    - apiVersion: v1
      kind: Service
      name: payment-api-svc
      namespace: production

  # Documentation link (populated by dot-ai PRD #228)
  documentationURL: "https://github.com/myorg/deployments/blob/main/docs/payment-api.md"

status:
  # Reconciliation state
  state: deployed  # pending, deployed, degraded, failed
  observedGeneration: 1

  # Resource health summary
  resources:
    total: 3
    ready: 3
    failed: 0

  # Conditions
  conditions:
    - type: Ready
      status: "True"
      lastTransitionTime: "2025-11-21T10:00:00Z"
      reason: AllResourcesReady
      message: "All resources are healthy"
```

## Implementation Requirements

### Resource Association Pattern

**Combine Option A and C from discussion:**

1. **Solution CR stores resource references** (Option A):
   ```yaml
   spec:
     resources:
       - apiVersion: apps/v1
         kind: Deployment
         name: payment-api
   ```

2. **Child resources have ownerReferences** (Option C):
   ```yaml
   # In Deployment manifest
   metadata:
     ownerReferences:
       - apiVersion: dot-ai.devopstoolkit.live/v1alpha1
         kind: Solution
         name: payment-api-production
         uid: <solution-uid>
         controller: true
         blockOwnerDeletion: true
   ```

**Benefits:**
- Solution CR provides quick lookup of all resources
- ownerReferences enable Kubernetes garbage collection
- Controller can discover resources via both methods

### Controller Responsibilities

The Solution Controller is a **thin coordinator** that:

1. **Watches Solution CRs**: Detects create/update/delete events
2. **Reconciles State**: Updates `status` based on child resource health
3. **Detects Drift**: Identifies when child resources are manually modified
4. **Calls MCP**: Delegates intelligence to MCP tools (future enhancement)
5. **Updates Status**: Keeps status.conditions and status.resources current

**NOT Responsible For:**
- Complex business logic (that's in MCP)
- Deployment orchestration (that's kubectl/Helm/Kustomize)
- Policy enforcement (future: different controller)

### Workflow Integration

**When recommend tool deploys a solution:**

1. **Generate manifests** (existing functionality)
2. **Create Solution CR** with metadata and resource references
3. **Add ownerReferences** to all generated manifests
4. **Apply Solution CR** first (parent must exist for ownerReferences)
5. **Apply child resources** with ownerReferences pointing to Solution CR
6. **Controller reconciles** and updates Solution status

### Open Questions & Design Notes

From our discussion, these are **noted for future discussion** when implementation begins:

#### Question 3: User Manually Edits Resources
**Scenario**: User manually modifies a Deployment after it's been deployed

**Options:**
- Solution CR status reflects drift (shows resource modified)
- Controller updates Solution CR to match current state
- Controller only tracks existence, not detailed state

**Decision**: TBD during implementation - likely just track drift in status

#### Question 3: User Deletes Resources Manually
**Scenario**: User runs `kubectl delete deployment payment-api`

**Options:**
- Solution CR status shows resource missing
- Controller recreates the resource (like Deployment controller does for pods)
- Solution CR is marked as degraded but takes no action

**Decision**: TBD during implementation - likely mark degraded, don't auto-recreate

#### Question 4: Existing Helm/Kustomize Applications
**Scope**: This PRD focuses on **new recommend-deployed solutions only**

Future consideration: Allow retrofitting existing apps by creating Solution CRs manually

#### Question 5: Documentation Link Timing
**Scenario**: When is documentationURL populated?

**Approach** (for dot-ai PRD #228):
1. Solution CR created without documentationURL initially
2. User generates documentation (dot-ai PRD #228)
3. User commits docs to Git
4. User updates Solution CR with documentationURL
5. dot-ai PRD #228 controller watches for URL, fetches and indexes docs

**Decision**: Field exists in CRD, population is dot-ai PRD #228's responsibility

#### Issue 1: Resource Drift Detection
**Challenge**: User manually modifies child resources, Solution CR metadata becomes stale

**Approach**:
- Controller watches child resources via ownerReferences
- Status reflects current child resource state
- Spec (metadata) remains unchanged (represents original intent)
- Status.conditions shows "Drifted" condition if changes detected

**Decision**: Note in PRD, design during implementation

#### Issue 2: Multi-Tool Environments
**Reality**: Users mix `dot-ai`, Helm, Kustomize, kubectl

**Scope**: Solution CRDs only track `dot-ai`-deployed solutions

**Trade-off**: Cluster becomes partially tracked, but that's acceptable
- Solution CRDs are opt-in metadata layer
- Don't interfere with other tools
- Provide value where they exist, gracefully absent otherwise

## Milestones

### Milestone 1: CRD Definition & Basic Controller ⬜
**Goal**: Solution CRD exists, basic controller reconciles state

**Success Criteria:**
- [x] Solution CRD defined with complete schema (spec + status)
- [ ] Controller scaffold created using Kubebuilder
- [ ] Controller watches Solution CRs and logs events
- [ ] Basic reconciliation updates status.state
- [ ] Integration test: Create Solution CR, controller updates status

**Deliverables:**
- [x] `api/v1alpha1/solution_types.go`
- [ ] `internal/controller/solution_controller.go`
- [x] Unit tests for CRD validation
- [ ] Integration test for basic reconciliation

**Estimated Duration**: 1-2 weeks

### Milestone 2: Resource Tracking & ownerReferences ⬜
**Goal**: Controller tracks child resources and maintains status

**Success Criteria:**
- Controller discovers child resources via ownerReferences
- Status.resources reflects actual resource count and health
- Status.conditions shows Ready when all resources exist
- Garbage collection works (deleting Solution CR deletes children)
- Integration test: Deploy solution with children, verify tracking

**Deliverables:**
- Resource discovery logic in controller
- Status update logic for resource health
- Health check aggregation across children
- E2E test with real Kubernetes resources

**Estimated Duration**: 1-2 weeks

### Milestone 3: Drift Detection & Status Management ⬜
**Goal**: Controller detects and reports manual resource modifications

**Success Criteria:**
- Controller detects when child resources are modified
- Status.conditions includes "Drifted" condition
- Status updated when resources are deleted manually
- Status shows which specific resources have issues
- Integration test: Modify child resource, verify drift detection

**Deliverables:**
- Resource state comparison logic
- Drift detection and reporting
- Status condition management
- Tests for drift scenarios

**Estimated Duration**: 1 week

### Milestone 4: MCP Integration (Basic) ⬜
**Goal**: Controller can call MCP tools for solution operations

**Success Criteria:**
- Controller has HTTP client for MCP communication
- Can notify MCP when Solution CR is created/updated/deleted
- Retry logic for failed MCP calls
- Integration test: Create Solution CR, verify MCP notification

**Deliverables:**
- MCP client library in controller
- Retry and error handling
- Tests with mock MCP server

**Estimated Duration**: 1 week

### Milestone 5: recommend Tool Integration ⬜
**Goal**: dot-ai recommend tool creates Solution CRs when deploying

**Success Criteria:**
- recommend tool generates Solution CR manifests
- ownerReferences added to all generated child resources
- Solution CR applied before children
- Complete workflow test: recommend → deploy → Solution CR created
- Documentation updated in dot-ai repo

**Deliverables:**
- Changes to dot-ai recommend tool
- Solution CR generation logic
- ownerReference injection
- E2E test in dot-ai repo

**Estimated Duration**: 1-2 weeks

**NOTE**: This milestone requires changes in dot-ai repo, not dot-ai-controller

### Milestone 6: Documentation & Production Readiness ⬜
**Goal**: Controller is production-ready and documented

**Success Criteria:**
- Complete README in dot-ai-controller
- Architecture diagrams showing Solution CR workflow
- Helm chart updated with Solution CRD
- RBAC properly configured
- Troubleshooting guide
- Performance testing documentation

**Deliverables:**
- Comprehensive documentation
- Helm chart templates for Solution CRD
- RBAC configuration
- Performance benchmarks
- Troubleshooting runbook

**Estimated Duration**: 1 week

## Dependencies

### Dependent PRDs (Blocked by This PRD)

- **dot-ai PRD #228**: Deployment Documentation & Example-Based Learning
  - Requires Solution CRD for tracking documentation references
  - Needs documentationURL field in Solution spec
  - Cannot begin implementation until Solution CRD and controller are complete

### Integration Points

- **dot-ai recommend tool**: Must be enhanced to create Solution CRs (Milestone 5)
- **MCP server**: Controller may call MCP endpoints for solution operations (Milestone 4)
- **Kubernetes API**: Controller watches and manages Solution CRs and child resources

### External Dependencies

- Kubernetes cluster (minimum version: 1.20+)
- Kubebuilder for CRD scaffolding
- Controller Runtime for controller logic

## Success Criteria

- [x] **CRD Exists**: Solution CRD deployed and functional in cluster
- [ ] **Controller Works**: Controller reconciles Solution CRs and updates status
- [ ] **Resource Tracking**: Child resources correctly linked via ownerReferences
- [ ] **Garbage Collection**: Deleting Solution CR deletes all child resources
- [ ] **Drift Detection**: Controller detects manual resource modifications
- [ ] **recommend Integration**: recommend tool creates Solution CRs automatically
- [ ] **Documentation Complete**: Comprehensive docs for users and developers
- [ ] **Production Ready**: Helm chart, RBAC, observability in place
- [ ] **Unblocks PRD #228**: dot-ai PRD #228 can begin implementation

## Risks & Mitigation

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| ownerReferences break with manual edits | Medium | Low | Document best practices, status shows drift |
| Users bypass Solution CR creation | Medium | Medium | Make recommend tool automatically create CRs |
| Solution CR schema changes required | Medium | Medium | Use CRD versioning (v1alpha1 allows breaking changes) |
| Controller adds operational overhead | Low | Low | Keep controller thin, minimal resource usage |
| Drift detection too noisy | Medium | Low | Add configuration for drift detection sensitivity |

## Open Questions

1. **Drift Reconciliation**: Should controller auto-correct drift or just report it?
   - **Current thinking**: Just report, don't auto-correct (too risky)

2. **Status Update Frequency**: How often should controller reconcile status?
   - **Current thinking**: Event-driven + periodic (every 5 minutes)

3. **MCP Integration Depth**: What solution operations should trigger MCP calls?
   - **Current thinking**: Notify on create/delete, not every status update

4. **Namespace Scoping**: Should Solution CRs be namespace-scoped or cluster-scoped?
   - **Decision**: Namespace-scoped (matches resources they track)

5. **Multi-Namespace Solutions**: What if a solution spans multiple namespaces?
   - **Current thinking**: Not supported in v1alpha1, consider in v1beta1

## Future Enhancements

**Phase 2 - Advanced Features**:
1. **Solution Updates**: Support for updating deployed solutions via Solution CR changes
2. **Rollback**: Track solution versions and support rollback
3. **Health Checks**: Advanced health checking beyond basic resource existence
4. **Metrics**: Export metrics about solution health and lifecycle

**Phase 3 - Intelligence**:
1. **Auto-Healing**: Controller automatically fixes degraded solutions
2. **Cost Tracking**: Integrate with cloud cost APIs to track solution costs
3. **Dependency Graph**: Visualize relationships between solutions
4. **Template System**: Create solution templates for common patterns

## References

- [Kubernetes ownerReferences](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
- [Kubebuilder Book](https://book.kubebuilder.io/)
- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [dot-ai recommend tool](https://github.com/vfarcic/dot-ai)
- [dot-ai PRD #228: Deployment Documentation](https://github.com/vfarcic/dot-ai/issues/228)

## Work Log

### 2025-11-21: PRD Creation
**Duration**: 2 hours
**Status**: Draft

**Completed Work**:
- Created PRD based on architecture discussion
- Defined Solution CRD schema with spec and status
- Documented ownerReferences pattern (Option A + C)
- Established 6 major milestones for implementation
- Documented dependency with dot-ai PRD #228
- Captured open questions and design notes for future discussion
- Clarified that controller is thin coordinator, MCP has intelligence

**Key Decisions**:
- Add Solution CRD to existing dot-ai-controller repo (not separate repo)
- Use both resource references (spec) and ownerReferences (child resources)
- Keep controller thin - delegates intelligence to MCP
- Primary benefit is for AI/MCP, not direct user operations
- Only track new recommend-deployed solutions (no retrofitting)
- documentationURL field exists, populated by dot-ai PRD #228

**Next Steps**:
- Review and approve PRD
- Begin Milestone 1: CRD Definition & Basic Controller
- Set up development environment for controller work

### 2025-11-21: Milestone 1 Progress - Solution CRD Definition
**Duration**: ~1 hour
**Status**: In Progress

**Completed Work**:
- Created Solution CRD API types in `api/v1alpha1/solution_types.go`
- Defined complete SolutionSpec with intent, context, resources, documentationURL fields
- Defined SolutionStatus with state, observedGeneration, resources summary, conditions
- Used `context` field name instead of `metadata` to avoid confusion with top-level metadata
- Generated CRD manifests and deepcopy methods via `make manifests generate`
- Created comprehensive unit tests in `api/v1alpha1/solution_types_test.go`
- Validated build with `make build` and `make test` - all tests passing
- Generated CRD manifest: `config/crd/bases/dot-ai.devopstoolkit.live_solutions.yaml`

**Key Decisions**:
- Renamed `spec.metadata` to `spec.context` to avoid confusion with top-level metadata field
- Used standard Kubernetes patterns: metav1.Condition for status, ResourceReference structure
- Added Kubebuilder validation markers for field validation
- Configured kubectl custom columns for useful `kubectl get solution` output

**PRD Updates**:
- Marked CRD definition items complete in Milestone 1 (2 of 5 success criteria)
- Marked CRD-related deliverables complete (2 of 4 deliverables)
- Marked "CRD Exists" complete in overall Success Criteria

**Next Steps**:
- Create Solution controller using Kubebuilder
- Implement basic reconciliation logic
- Add integration tests for controller behavior
