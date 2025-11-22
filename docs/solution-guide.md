# Solution CRD Guide

## Overview

The **Solution CRD** is a Kubernetes Custom Resource Definition that tracks deployed solutions and their constituent resources. It acts as a parent resource that groups all Kubernetes resources (Deployments, Services, ConfigMaps, etc.) that compose a logical solution, preserving metadata and context not available in individual resources.

### Purpose

When the DevOps AI Toolkit `recommend` tool deploys resources to a cluster, the Solution CRD provides:

1. **Resource Grouping**: Links related Kubernetes resources into a logical solution
2. **Intent Preservation**: Stores the original user intent that led to the deployment
3. **Metadata Storage**: Captures information not available in individual resources:
   - Deployment rationale and decision-making context
   - Configuration trade-offs and choices
   - Documentation links
   - Patterns and policies applied
4. **AI Context**: Provides context for future AI recommendations and learning

### Key Design Principles

- **Kubernetes-Native**: Uses standard Kubernetes patterns and conventions
- **Metadata Store**: Captures information NOT in individual resources
- **Thin Controller**: Controller coordinates operations, delegates intelligence to MCP
- **AI-Focused**: Primary benefit is providing context for AI/MCP tools

## Current Status (Milestone 2)

**✅ Implemented:**
- Solution CRD with complete schema (spec + status)
- Controller that watches Solution CRs and child resources
- Resource discovery from `spec.resources` list
- Dynamic ownerReference management for garbage collection
- Health checking of child resources (conditions, replica counts)
- Status management with actual health tracking (ready/failed counts)
- Status updates based on resource state (deployed/degraded/pending)
- Wildcard RBAC for tracking any resource type
- Integration tests (65 tests passing)

**⏳ Coming in Milestone 3:**
- Drift detection when resources are modified
- Status reporting for resource state changes

## Solution CRD Schema

### Spec Fields

```yaml
spec:
  # Original user intent that led to this deployment
  intent: string

  # Solution metadata (information not in individual resources)
  context:
    createdBy: string
    rationale: string
    patterns: []string
    policies: []string

  # List of Kubernetes resources that compose this solution
  resources:
    - apiVersion: string
      kind: string
      name: string
      namespace: string  # optional for cluster-scoped resources

  # Documentation URL (optional)
  documentationURL: string
```

### Status Fields

```yaml
status:
  # Overall state of the solution
  state: string  # pending, deployed, degraded, failed

  # Generation tracking
  observedGeneration: int64

  # Resource summary
  resources:
    total: int     # Total number of resources
    ready: int     # Number of ready resources (Milestone 2)
    failed: int    # Number of failed resources (Milestone 2)

  # Standard Kubernetes conditions
  conditions:
    - type: Ready
      status: "True"
      reason: string
      message: string
```

## Installation

### Prerequisites

- Kubernetes cluster v1.20+
- kubectl configured for cluster access
- Controller running (see main README for installation)

### Install the Solution CRD

```bash
# Install CRD into cluster
make install

# Verify CRD is installed
kubectl get crds solutions.dot-ai.devopstoolkit.live
```

## Testing the Solution Controller

### Step 1: Run the Controller Locally

For testing and development, run the controller locally:

```bash
# Run controller locally (connects to cluster via kubeconfig)
make run
```

Leave this running in one terminal. You'll see controller logs in real-time.

### Step 2: Deploy Sample Resources and Solution

In another terminal, deploy the Solution samples:

```bash
# Deploy Solution samples (namespace + resources + Solution CR)
kubectl apply -k config/samples/solution/

# This creates:
# - dot-ai namespace
# - web-app Deployment + Service
# - postgresql StatefulSet + Service + PVC
# - sample-solution CR (tracks all the above resources)
```

**Note**: Resources are created before the Solution, but order doesn't matter. The controller handles both cases gracefully.

### Step 3: Observe Controller Behavior

Watch the controller logs from Step 1. You should see:

```
INFO    Reconciling Solution    {"solution": "dot-ai/sample-solution", "intent": "Deploy a sample...", "resources": 5}
INFO    Initializing status for new Solution
INFO    ✅ Solution status initialized  {"state": "deployed", "totalResources": 5}
INFO    Reconciling Solution    {"solution": "dot-ai/sample-solution", "resources": 5, "observedGeneration": 1}
INFO    Ensuring resource ownership
DEBUG   ✅ ownerReference ensured  {"kind": "Deployment", "name": "web-app"}
DEBUG   ✅ ownerReference ensured  {"kind": "Service", "name": "web-app-service"}
DEBUG   ✅ ownerReference ensured  {"kind": "StatefulSet", "name": "postgresql"}
DEBUG   Resource is ready  {"kind": "Deployment", "name": "web-app", "reason": "Available"}
DEBUG   Resource is ready  {"kind": "Service", "name": "web-app-service", "reason": "ResourceExists"}
INFO    ✅ Solution reconciled successfully  {"state": "deployed", "totalResources": 5}
```

**Key observations:**
- ownerReferences are dynamically added to all child resources
- Health checking reports ready/failed status
- Status tracks actual resource health

### Step 4: Inspect Solution Status

```bash
# List all Solutions in dot-ai namespace
kubectl get solutions -n dot-ai

# Get detailed status
kubectl describe solution sample-solution -n dot-ai

# Get full YAML with status
kubectl get solution sample-solution -n dot-ai -o yaml
```

Expected output:

```yaml
status:
  state: deployed
  observedGeneration: 1
  resources:
    total: 5
    ready: 5      # All resources are healthy (Milestone 2)
    failed: 0
  conditions:
  - type: Ready
    status: "True"
    reason: AllResourcesReady
    message: "All 5 resources are ready"
    lastTransitionTime: "2025-11-22T..."
```

### Step 5: Verify ownerReferences Were Added

Check that ownerReferences were dynamically added to child resources:

```bash
# Check web-app Deployment
kubectl get deployment web-app -n dot-ai -o jsonpath='{.metadata.ownerReferences}' | jq

# Expected: ownerReference pointing to sample-solution
# with controller=true and blockOwnerDeletion=true
```

All child resources should now have ownerReferences pointing to the Solution CR.

### Step 6: Test Health Detection

Break a Deployment to see the health status change:

```bash
# Use a non-existent image to make deployment unhealthy
kubectl set image deployment/web-app -n dot-ai nginx=nginx:nonexistent
```

Wait ~1 minute for reconciliation. Check status:

```bash
kubectl get solution sample-solution -n dot-ai -o yaml
```

Expected status shows degraded state:

```yaml
status:
  state: degraded
  resources:
    total: 5
    ready: 4      # 4 resources still healthy
    failed: 1     # web-app Deployment is not Available
  conditions:
  - type: Ready
    status: "False"
    reason: ResourcesNotReady
    message: "Ready: 4/5, Failed: 1"
```

Fix it:

```bash
kubectl set image deployment/web-app -n dot-ai nginx=nginx:latest
```

Wait for pods to become ready. Status will return to `state: deployed` with `ready: 5`.

### Step 7: Test Garbage Collection

Delete the Solution and verify children are automatically deleted:

```bash
# Delete Solution
kubectl delete solution sample-solution -n dot-ai

# Verify child resources are garbage collected
kubectl get deployment web-app -n dot-ai
# Error from server (NotFound): deployments.apps "web-app" not found

kubectl get service web-app-service -n dot-ai
# Error from server (NotFound): services "web-app-service" not found
```

**Expected**: All child resources are automatically deleted by Kubernetes GC because ownerReferences were set with `blockOwnerDeletion: true`.

### Step 8: Cleanup

```bash
# Delete remaining resources and namespace
kubectl delete -k config/samples/solution/
```

## Example Solutions

### Simple Example

The `config/samples/solution/solution_simple.yaml` file contains a basic example:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: sample-solution
  namespace: dot-ai
spec:
  intent: "Deploy a sample web application with database backend"

  context:
    createdBy: "dot-ai-recommend-tool"
    rationale: "User requested a scalable web application"
    patterns:
      - "Microservices Pattern"
    policies:
      - "production-security-policy"

  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: web-app
    - apiVersion: v1
      kind: Service
      name: web-app-service
```

### Custom Example

Create your own Solution:

```bash
kubectl apply -f - <<EOF
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: my-solution
  namespace: dot-ai
spec:
  intent: "Your deployment intent here"

  context:
    createdBy: "manual"
    rationale: "Testing the Solution controller"

  resources:
    - apiVersion: v1
      kind: ConfigMap
      name: my-config
    - apiVersion: v1
      kind: Secret
      name: my-secret
EOF
```

## Current Limitations (Milestone 2)

### Single Namespace Only

Solutions can only track resources in the same namespace as the Solution CR itself. Multi-namespace solutions are not supported in v1alpha1.

**Example - NOT supported:**
```yaml
metadata:
  name: my-solution
  namespace: dot-ai
spec:
  resources:
    - kind: Service
      name: database
      namespace: production  # ❌ Different namespace not supported
```

**Workaround**: Create separate Solutions in each namespace.

### No Drift Reconciliation

The controller detects when resources become unhealthy and updates status accordingly, but it does not automatically fix drift or recreate missing resources.

**Example**: If you manually delete a child Deployment, the Solution status will show `failed: 1`, but the controller won't recreate it.

**Coming in Milestone 3**: Enhanced drift detection and reporting.

### No MCP Integration

The controller doesn't yet call MCP tools for solution-level operations.

**Coming in Milestone 4**: MCP integration for intelligent solution operations.

## What Works Now

The current Milestone 2 implementation provides:

✅ **Solution Tracking**: Create and manage Solution CRs
✅ **Resource Discovery**: Automatically discovers resources from `spec.resources`
✅ **ownerReference Management**: Dynamically adds ownerReferences to child resources
✅ **Garbage Collection**: Kubernetes automatically deletes children when Solution is deleted
✅ **Health Checking**: Monitors resource health using conditions and replica counts
✅ **Status Management**: Real-time status updates with ready/failed counts
✅ **State Tracking**: Solution state reflects actual health (deployed/degraded/pending)
✅ **Generation Tracking**: `observedGeneration` tracks spec changes
✅ **Event Recording**: Kubernetes events for observability
✅ **Wildcard RBAC**: Works with any resource type (built-in and custom CRDs)

## kubectl Shortcuts

The Solution CRD includes the shortname `sol` for convenience:

```bash
# Instead of
kubectl get solutions -n dot-ai

# You can use
kubectl get sol -n dot-ai
```

## Next Steps

### For Users

**Milestone 2 is complete!** The Solution controller is now ready for testing and early adoption. Current capabilities:

- Full resource tracking and health monitoring
- Automatic garbage collection
- Works with any Kubernetes resource type
- Production-ready status management

**Not yet ready for**: Production use. Wait for Milestone 6 (Documentation & Production Readiness) for:
- Complete user documentation
- Helm chart integration
- Performance benchmarks
- Troubleshooting guides

### For Developers

To contribute to Solution controller development:

1. **Read the PRD**: See `prds/4-solution-crd-tracking.md` for full requirements
2. **Test Milestone 2**: Follow this guide to verify functionality
3. **Check Next Milestones**: Milestone 3 (Drift Detection) is next
4. **Run Tests**: `make test` - all 65 tests should pass
5. **Review Code**: Controller logic in `internal/controller/solution_controller.go`

## Roadmap

### ✅ Milestone 2: Resource Tracking & ownerReferences (COMPLETE)

- ✅ Discover child resources from `spec.resources`
- ✅ Add ownerReferences to child resources dynamically
- ✅ Health check child resources (conditions + replica counts)
- ✅ Update `ready` and `failed` counts based on actual health
- ✅ Enable Kubernetes garbage collection
- ✅ Wildcard RBAC for any resource type
- ✅ 65 integration tests passing

### Milestone 3: Drift Detection & Status Management

- Detect when child resources are modified manually
- Report drift in status conditions
- Track resource state changes

### Milestone 4: MCP Integration

- Call MCP tools for solution operations
- Notify MCP when Solutions are created/updated
- Support solution-level AI operations

### Milestone 5: dot-ai recommend Tool Integration

- Generate Solution CRs when deploying with recommend tool
- Automatically populate `spec.resources` list
- Link documentation URLs

## Troubleshooting

### Solution CR Not Being Reconciled

**Problem**: Created a Solution CR but status is not updating.

**Check:**
```bash
# Is the controller running?
kubectl get pods -n dot-ai -l app.kubernetes.io/name=dot-ai-controller

# Check controller logs
kubectl logs -n dot-ai -l app.kubernetes.io/name=dot-ai-controller --tail=50

# Is the CRD installed?
kubectl get crds solutions.dot-ai.devopstoolkit.live
```

### Status Shows Wrong Resource Count

**Problem**: `status.resources.total` doesn't match expected count.

**Cause**: The controller counts resources from `spec.resources` list. Check your Solution spec:

```bash
# Show the resources list
kubectl get solution <name> -n dot-ai -o jsonpath='{.spec.resources}' | jq .
```

### Controller Logs Show Conflicts

**Problem**: Controller logs show "resource conflict" errors.

**Cause**: Multiple concurrent updates to the Solution status. This is normal and handled automatically with exponential backoff retries.

**Action**: No action needed - the controller will retry and eventually succeed.

## API Reference

For complete API documentation, see the generated CRD:

```bash
kubectl get crd solutions.dot-ai.devopstoolkit.live -o yaml
```

Or view the Go types in `api/v1alpha1/solution_types.go`.

## Related Documentation

- [Main README](../README.md) - Controller installation and RemediationPolicy guide
- [PRD #4](../prds/4-solution-crd-tracking.md) - Complete Solution CRD requirements and design
- [CLAUDE.md](../CLAUDE.md) - Development guide for contributors
