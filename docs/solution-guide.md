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

## Current Status (Milestone 1)

**✅ Implemented:**
- Solution CRD with complete schema (spec + status)
- Basic controller that watches Solution CRs
- Status management (state, observedGeneration, conditions)
- Resource count tracking
- Integration tests

**⏳ Coming in Milestone 2:**
- Actual resource discovery and verification
- ownerReferences management for garbage collection
- Health checking of child resources
- Ready/Failed resource counts

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

### Step 2: Create a Test Solution

In another terminal, create a simple Solution CR:

```bash
kubectl apply -f config/samples/solution_simple.yaml
```

### Step 3: Observe Controller Behavior

Watch the controller logs from Step 1. You should see:

```
INFO    Reconciling Solution    {"solution": "default/sample-solution", "intent": "Deploy a sample web application...", "resources": 5}
INFO    Initializing status for new Solution
INFO    ✅ Solution status initialized  {"state": "deployed", "totalResources": 5}
INFO    ✅ Solution reconciled successfully  {"state": "deployed", "totalResources": 5}
```

### Step 4: Inspect Solution Status

```bash
# List all Solutions
kubectl get solutions

# Get detailed status
kubectl describe solution sample-solution

# Get full YAML with status
kubectl get solution sample-solution -o yaml
```

Expected output:

```yaml
status:
  state: deployed
  observedGeneration: 1
  resources:
    total: 5
    ready: 0    # Will be populated in Milestone 2
    failed: 0   # Will be populated in Milestone 2
  conditions:
  - type: Ready
    status: "True"
    reason: SolutionCreated
    message: "Solution tracking 5 resources"
    lastTransitionTime: "2025-11-22T..."
```

### Step 5: Test Status Updates

Update the Solution to trigger reconciliation:

```bash
# Update intent field
kubectl patch solution sample-solution --type merge -p '{"spec":{"intent":"Updated intent"}}'

# Watch observedGeneration increment
kubectl get solution sample-solution -o jsonpath='{.status.observedGeneration}'
```

The controller will reconcile and increment `observedGeneration` from 1 to 2.

### Step 6: Cleanup

```bash
kubectl delete solution sample-solution
```

## Example Solutions

### Simple Example

The `config/samples/solution_simple.yaml` file contains a basic example:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: sample-solution
  namespace: default
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
      namespace: default
    - apiVersion: v1
      kind: Service
      name: web-app-service
      namespace: default
```

### Custom Example

Create your own Solution:

```bash
kubectl apply -f - <<EOF
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: my-solution
  namespace: default
spec:
  intent: "Your deployment intent here"

  context:
    createdBy: "manual"
    rationale: "Testing the Solution controller"

  resources:
    - apiVersion: v1
      kind: ConfigMap
      name: my-config
      namespace: default
    - apiVersion: v1
      kind: Secret
      name: my-secret
      namespace: default
EOF
```

## Current Limitations (Milestone 1)

### No Resource Validation

The controller **does not verify** that resources listed in `spec.resources` actually exist in the cluster. It simply:
- Counts resources from the list
- Sets `status.resources.total`
- Sets `status.state = "deployed"`

**Example:** You can create a Solution referencing non-existent resources, and the controller will accept it:

```yaml
spec:
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: does-not-exist  # This resource doesn't exist!
      namespace: default
```

The controller will still show `state: deployed` and `total: 1`.

**This is intentional for Milestone 1** - resource discovery and validation will be added in Milestone 2.

### No ownerReferences Management

The controller does not yet add `ownerReferences` to child resources. This means:
- Deleting a Solution CR **will not** delete its child resources
- No Kubernetes garbage collection integration
- Child resources must be deleted manually

**Coming in Milestone 2**: The controller will dynamically add ownerReferences to enable automatic cleanup.

### No Health Checking

The `status.resources.ready` and `status.resources.failed` fields are always `0`. The controller doesn't check if resources are actually healthy or even exist.

**Coming in Milestone 2**: The controller will verify resource health and update these counts.

## What Works Now

Despite the limitations, the current implementation provides:

✅ **Solution Tracking**: Create and manage Solution CRs
✅ **Status Management**: Automatic status initialization and updates
✅ **Generation Tracking**: `observedGeneration` tracks spec changes
✅ **Ready Conditions**: Standard Kubernetes condition reporting
✅ **Resource Counting**: Tracks total resource count from spec
✅ **Event Recording**: Kubernetes events for observability
✅ **Graceful Deletion**: Handles Solution CR deletion properly

## kubectl Shortcuts

The Solution CRD includes the shortname `sol` for convenience:

```bash
# Instead of
kubectl get solutions

# You can use
kubectl get sol
```

## Next Steps

### For Users

Wait for **Milestone 2** before using Solution CRDs in production. The current implementation is suitable for:
- Testing the controller
- Understanding the API schema
- Preparing for future integration with dot-ai recommend tool

### For Developers

To contribute to Solution controller development:

1. **Read the PRD**: See `prds/4-solution-crd-tracking.md` for full requirements
2. **Check Milestones**: Understand what's planned for future releases
3. **Run Tests**: `make test` to verify controller behavior
4. **Review Code**: Controller logic in `internal/controller/solution_controller.go`

## Roadmap

### Milestone 2: Resource Tracking & ownerReferences

- Discover child resources from `spec.resources`
- Add ownerReferences to child resources
- Health check child resources
- Update `ready` and `failed` counts
- Enable Kubernetes garbage collection

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
kubectl get solution <name> -o jsonpath='{.spec.resources}' | jq .
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
