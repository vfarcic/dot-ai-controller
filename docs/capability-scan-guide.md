# Capability Scan Guide

This guide covers the CapabilityScanConfig CRD for autonomous capability discovery and scanning in your Kubernetes cluster.

## Overview

The CapabilityScanConfig enables:
- **Autonomous Discovery**: Automatically detects CRD changes (create, update, delete)
- **Event-Driven Scanning**: Triggers capability scans when new CRDs are installed
- **Startup Reconciliation**: Syncs cluster state with MCP on controller restart
- **Debounced Batching**: Groups rapid CRD changes into efficient batch requests

This feature works with the [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) to keep your cluster's capability data up-to-date for AI-powered recommendations.

## Prerequisites

- Controller installed (see [Setup Guide](setup-guide.md))
- [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) installed and running

## Quick Start

1. Create a secret with your MCP API key (if authentication is required):

```bash
kubectl create secret generic mcp-credentials \
  --namespace dot-ai \
  --from-literal=api-key=your-api-key-here
```

2. Create a CapabilityScanConfig to start scanning:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: default-scan
  namespace: dot-ai
spec:
  mcp:
    endpoint: http://dot-ai.dot-ai.svc.cluster.local:3456/api/v1/tools/manageOrgData
    authSecretRef:
      name: mcp-credentials
      key: api-key
```

3. Apply it:

```bash
kubectl apply -f capabilityscanconfig.yaml
```

The controller will perform an initial scan of all cluster resources and then watch for CRD changes.

## Configuration

### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mcp.endpoint` | string | Yes | - | Full URL of the MCP manageOrgData endpoint |
| `mcp.collection` | string | No | capabilities | Qdrant collection name for storing capabilities |
| `mcp.authSecretRef` | SecretReference | Yes | - | Secret containing API key for MCP authentication |
| `includeResources` | []string | No | all | Patterns for resources to include in scanning |
| `excludeResources` | []string | No | - | Patterns for resources to exclude from scanning |
| `retry.maxAttempts` | int | No | 3 | Maximum retry attempts for MCP API calls |
| `retry.backoffSeconds` | int | No | 5 | Initial backoff duration in seconds |
| `retry.maxBackoffSeconds` | int | No | 300 | Maximum backoff duration in seconds |
| `debounceWindowSeconds` | int | No | 10 | Time window to batch CRD events before syncing |

### Resource Filtering

Use `includeResources` and `excludeResources` to control which resources are scanned. Filters apply to:
- **Initial scan**: All resources discovered via Discovery API (core + CRDs)
- **Event-driven scanning**: CRD create/update/delete events

**Pattern Format**:
- `Kind.group` for grouped resources (e.g., `Deployment.apps`, `RDSInstance.database.aws.crossplane.io`)
- `Kind` for core resources (e.g., `Service`, `ConfigMap`)
- Wildcards supported: `*.crossplane.io`, `*.apps`, `*`

**Example: Whitelist - Scan Only Crossplane Resources**:

```yaml
spec:
  includeResources:
    - "*.crossplane.io"
```

**Example: Blocklist - Scan Everything Except High-Volume Resources**:

```yaml
spec:
  excludeResources:
    - "Event"
    - "Lease.coordination.k8s.io"
    - "EndpointSlice.discovery.k8s.io"
```

**Example: Combined - Crossplane Resources Except Provider Configs**:

```yaml
spec:
  includeResources:
    - "*.crossplane.io"
  excludeResources:
    - "ProviderConfig.*"
```

**Processing Order**:
1. If `includeResources` is specified, only those patterns are scanned
2. `excludeResources` is applied as a blocklist after includes
3. If neither is specified, all resources are scanned

## Status

Check the status to verify scanning is working:

```bash
kubectl get capabilityscanconfig default-scan -o yaml
```

### Status Fields

| Field | Description |
|-------|-------------|
| `initialScanComplete` | Whether startup reconciliation has completed |
| `lastScanTime` | Timestamp of last successful scan trigger |
| `lastError` | Last error message if any |
| `conditions` | Standard Kubernetes conditions |

### Conditions

| Type | Description |
|------|-------------|
| `Ready` | True when controller is watching CRDs and connected to MCP |

## How It Works

### Startup Reconciliation

When the controller starts (or restarts), it performs a diff-and-sync:

1. **List Cluster Resources**: Uses Discovery API to get all resources (core + CRDs) matching include/exclude filters
2. **List MCP Capabilities**: Queries MCP for existing capability IDs
3. **Compute Diff**:
   - Resources in cluster but not in MCP → trigger targeted scan
   - Capabilities in MCP but not in cluster → delete orphaned

This ensures the controller recovers gracefully from restarts without missing any changes.

### Event-Driven Scanning

After startup, the controller watches for CRD events:

1. **CRD Created/Updated**: Queue for capability scan
2. **CRD Deleted**: Queue for capability deletion
3. **Debounce**: Wait for `debounceWindowSeconds` to collect more events
4. **Batch Request**: Send all queued scans in a single request

### Debouncing

When operators are installed, many CRDs may be created in rapid succession. Debouncing prevents overwhelming MCP with individual requests:

```
Time 0s:   CRD-A created → add to buffer
Time 1s:   CRD-B created → add to buffer
Time 2s:   CRD-C created → add to buffer
...
Time 10s:  Flush buffer → single request: "CRD-A,CRD-B,CRD-C"
```

Configure the window based on your needs:
- **Lower values (1-5s)**: Faster scanning, more HTTP requests
- **Higher values (30-60s)**: Fewer requests, delayed scanning

### Fire-and-Forget Model

The controller uses a fire-and-forget pattern:
- Scans are triggered asynchronously (controller doesn't wait for completion)
- MCP performs the actual capability analysis in the background
- Failed scans are automatically retried on next controller restart

## Example: Full Configuration

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: production-scan
  namespace: dot-ai
spec:
  # MCP configuration
  mcp:
    endpoint: http://dot-ai.dot-ai.svc.cluster.local:3456/api/v1/tools/manageOrgData
    collection: capabilities
    authSecretRef:
      name: mcp-credentials
      key: api-key

  # Only scan Crossplane and ArgoCD resources
  includeResources:
    - "*.crossplane.io"
    - "*.aws.crossplane.io"
    - "*.gcp.crossplane.io"
    - "*.azure.crossplane.io"
    - "applications.argoproj.io"
    - "applicationsets.argoproj.io"

  # Exclude internal resources
  excludeResources:
    - "*.internal.company.com"

  # Retry configuration for MCP API calls
  retry:
    maxAttempts: 5
    backoffSeconds: 10
    maxBackoffSeconds: 300

  # Batch CRD events for 15 seconds before sending
  debounceWindowSeconds: 15
```

## Use Cases

### Crossplane Provider Installation

When you install a Crossplane provider:

```bash
kubectl apply -f provider-aws.yaml
```

The controller:
1. Detects new CRDs (`RDSInstance.database.aws.crossplane.io`, `Bucket.s3.aws.crossplane.io`, etc.)
2. Waits for debounce window (batches all CRDs)
3. Sends single scan request to MCP
4. MCP analyzes and stores capabilities

MCP can now provide AI recommendations that include the newly available AWS resources.

### Operator Removal

When you remove an operator:

```bash
kubectl delete -f provider-aws.yaml
```

The controller:
1. Detects CRD deletions
2. Sends delete requests to MCP for each capability
3. MCP removes stale capability data

MCP recommendations no longer suggest the removed resources.

### Controller Restart Recovery

If the controller pod restarts:

1. Controller performs startup reconciliation
2. Compares cluster CRDs with MCP capabilities
3. Syncs any differences (missed events during downtime)
4. Resumes event watching

No manual intervention required.

## Troubleshooting

### Controller Not Starting

Check the Ready condition:

```bash
kubectl get capabilityscanconfig default-scan -o jsonpath='{.status.conditions}'
```

Common issues:
- Invalid `mcp.endpoint` URL
- MCP service not reachable
- Missing RBAC permissions

### Scans Not Triggering

1. Check if CRD matches include/exclude filters:

```bash
# View configured filters
kubectl get capabilityscanconfig default-scan -o jsonpath='{.spec.includeResources}'
kubectl get capabilityscanconfig default-scan -o jsonpath='{.spec.excludeResources}'
```

2. Check controller logs:

```bash
kubectl logs -l app.kubernetes.io/name=dot-ai-controller -n dot-ai --tail=50
```

Look for messages about CRD events and filtering decisions.

### MCP Connection Errors

Check `lastError` in status:

```bash
kubectl get capabilityscanconfig default-scan -o jsonpath='{.status.lastError}'
```

Common issues:
- MCP endpoint unreachable (check service/DNS)
- Authentication failure (check secret exists and has correct key)
- MCP server overloaded (check MCP logs)

### Initial Scan Not Completing

Check if initial scan is marked complete:

```bash
kubectl get capabilityscanconfig default-scan -o jsonpath='{.status.initialScanComplete}'
```

If false, check controller logs for errors during startup reconciliation.

### Debounce Window Too Long/Short

Adjust `debounceWindowSeconds` based on your operator installation patterns:

```yaml
spec:
  # For frequent small changes
  debounceWindowSeconds: 5

  # For large operator installations
  debounceWindowSeconds: 30
```

## Cleanup

Delete the CapabilityScanConfig to stop scanning:

```bash
kubectl delete capabilityscanconfig default-scan
```

This stops the CRD watcher but does not delete capability data from MCP. To remove capability data, use the MCP `manageOrgData` tool with `operation: deleteAll`. See the [Capability Management Guide](https://devopstoolkit.ai/docs/mcp/guides/mcp-capability-management-guide) for details.

## Next Steps

- Learn about [Resource Sync](resource-sync-guide.md) for semantic search of cluster resources
- Explore [Remediation Policies](remediation-guide.md) for event-driven remediation
- Check [Troubleshooting Guide](troubleshooting.md) for common issues
