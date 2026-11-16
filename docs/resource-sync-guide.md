# Resource Sync Guide

This guide covers the ResourceSyncConfig CRD for enabling resource visibility and semantic search in your Kubernetes cluster.

## Overview

The ResourceSyncConfig enables:
- **Resource Discovery**: Automatically discovers all resource types in your cluster
- **Change Tracking**: Watches for resource changes (create, update, delete)
- **Semantic Search**: Syncs resource metadata to MCP for natural language queries

## Stack Installation

If you installed via the [DevOps AI Toolkit Stack](https://devopstoolkit.ai/docs/stack), ResourceSyncConfig is already configured. You can verify with:

```bash
kubectl get resourcesyncconfig -n dot-ai
```

Continue below only if you need to customize the configuration or installed the controller individually.

## Prerequisites

- Controller installed (see [Setup Guide](setup-guide.md))
- [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) installed and running

## Quick Start

1. Create a secret with your MCP API key:

```bash
kubectl create secret generic dot-ai-secrets \
  --namespace dot-ai \
  --from-literal=auth-token=your-auth-token-here
```

2. Create a ResourceSyncConfig to start syncing resources:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: default-sync
  namespace: dot-ai
spec:
  mcpEndpoint: http://dot-ai.dot-ai.svc.cluster.local:3456/api/v1/resources/sync
  mcpAuthSecretRef:
    name: dot-ai-secrets
    key: auth-token
  debounceWindowSeconds: 10
  resyncIntervalMinutes: 60
```

3. Apply it:

```bash
kubectl apply -f resourcesyncconfig.yaml
```

## How It Works

1. **Discovery**: Controller discovers all resource types via the Kubernetes Discovery API
2. **Informers**: Dynamic informers are created for each resource type
3. **Change Detection**: Informer event handlers detect create/update/delete events
4. **Debouncing**: Changes are batched in a time window to reduce API calls
5. **Sync to MCP**: Batched changes are sent to MCP via HTTP
6. **Periodic Resync**: Full state is sent periodically to catch any missed events

### What Gets Synced

For each resource, the following metadata is synced to MCP:
- Kind, APIVersion, Name, Namespace
- Labels and select annotations (description-related)
- Creation and update timestamps

This metadata enables semantic search to discover resources (e.g., "find all databases", "list deployments in production").

### What's NOT Synced

The following are **not** synced to reduce traffic and storage:
- **Resource status** - fetched on-demand from Kubernetes API when needed
- **Resource spec** - fetched on-demand from Kubernetes API when needed
- High-volume resources: Events, Leases, EndpointSlices
- Large annotations like `kubectl.kubernetes.io/last-applied-configuration`

This design means resource discovery happens via semantic search in Qdrant, while current state (status/spec) is always fetched fresh from the Kubernetes API.

## Configuration

### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mcpEndpoint` | string | Yes | - | Full URL of the MCP resource sync endpoint |
| `mcpAuthSecretRef` | SecretReference | Yes | - | Secret containing API key for MCP authentication |
| `debounceWindowSeconds` | int | No | 10 | Time window to batch changes before syncing |
| `resyncIntervalMinutes` | int | No | 60 | Full resync interval (catches missed events) |
| `includeResources` | []string | No | all | `resource.group` patterns of resource types to watch |
| `excludeResources` | []string | No | none | `resource.group` patterns of resource types to skip, applied after `includeResources` |
| `watchNamespaces` | []string | No | all | Watch namespaced resources only in these namespaces |
| `watchNamespaceLabelSelector` | string | No | - | Watch namespaced resources only in namespaces matching this label selector; takes precedence over `watchNamespaces` |

### Limiting What Gets Watched

By default the controller creates an informer for every resource type in the cluster and lists objects across all namespaces. On large clusters (hundreds of CRDs, many namespaces) this can use a lot of memory. Use the filtering fields to reduce it. All of them are optional, and leaving them unset keeps the default behavior.

**Resource types** (`includeResources`, `excludeResources`) use `resource.group` patterns:

| Pattern | Matches |
|---------|---------|
| `deployments.apps` | Deployments only |
| `pods.` | Core (no group) Pods; note the trailing dot |
| `*.monitoring.coreos.com` | Every resource type in the `monitoring.coreos.com` group |
| `certificates.*` | `certificates` in any group |
| `*` | Every resource type |

When `includeResources` is set, only matching types are watched. `excludeResources` then removes types from that set. Events, Leases and EndpointSlices are always skipped, and including them has no effect.

**Namespaces** (`watchNamespaces`, `watchNamespaceLabelSelector`) limit where instances of namespaced types are watched. Cluster-scoped types, such as Nodes, Namespaces or cluster-scoped CRDs, are always watched cluster-wide.

- `watchNamespaces` is a fixed list. Duplicate entries are ignored, and empty names are rejected.
- `watchNamespaceLabelSelector` accepts equality-based (`team=payments`) and set-based (`team in (payments,checkout)`) selectors. The matching namespaces are resolved when the watcher starts. A namespace that gets the label later is not picked up until the controller restarts or the `ResourceSyncConfig` changes.
- An invalid selector, or one that matches no namespace, is reported as an error and retried. The controller never falls back to watching all namespaces.

Resources that fall outside the filters are removed from the search index at the next full resync.

If you install with the [dot-ai-stack](https://github.com/vfarcic/dot-ai-stack) Helm chart, set these fields with the `resourceSync.includeResources`, `resourceSync.excludeResources`, `resourceSync.watchNamespaces` and `resourceSync.watchNamespaceLabelSelector` values.

## Status

Check the status to verify the sync is working:

```bash
kubectl get resourcesyncconfig default-sync -o yaml
```

### Status Fields

| Field | Description |
|-------|-------------|
| `active` | Whether the watcher is running |
| `watchedResourceTypes` | Number of resource types being watched |
| `totalResourcesSynced` | Total resources synced to MCP |
| `lastResyncTime` | Time of last full resync |
| `syncErrors` | Count of sync errors |
| `conditions` | Standard Kubernetes conditions |

### Conditions

| Type | Description |
|------|-------------|
| `Ready` | True when watcher is active and syncing |

## Semantic Search

Once resources are synced, you can search using natural language through MCP:

```
"which databases are we running?"
"list deployments in production namespace"
"find all services related to payments"
```

MCP uses semantic embeddings to understand intent, so you don't need to know exact resource kinds or field names.

### Query Flow

For questions about resource state (e.g., "what's the status of my databases"):
1. **Discovery**: Semantic search finds relevant resources in Qdrant
2. **Fetch**: Current status is fetched from Kubernetes API
3. **Response**: AI synthesizes the answer with fresh data

This ensures status information is always current, not potentially stale from a sync lag.

## Example: Full Configuration

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: production-sync
  namespace: dot-ai
spec:
  # MCP endpoint for resource sync (full URL)
  mcpEndpoint: http://dot-ai.dot-ai.svc.cluster.local:3456/api/v1/resources/sync

  # Required: authentication for MCP
  mcpAuthSecretRef:
    name: dot-ai-secrets
    key: auth-token

  # Batch changes for 10 seconds before syncing
  debounceWindowSeconds: 10

  # Full resync every hour to catch any missed events
  resyncIntervalMinutes: 60

  # Optional: watch only these resource types (default: all)
  includeResources:
    - deployments.apps
    - services.
    - "*.crossplane.io"

  # Optional: skip these types, applied after includeResources
  excludeResources:
    - "*.internal.crossplane.io"

  # Optional: watch namespaced resources only in namespaces labeled team=payments
  # (takes precedence over watchNamespaces)
  watchNamespaceLabelSelector: team=payments
```

## Troubleshooting

### Watcher Not Starting

Check the Ready condition:

```bash
kubectl get resourcesyncconfig default-sync -o jsonpath='{.status.conditions}'
```

Common issues:
- Invalid `mcpEndpoint` URL
- MCP service not reachable
- Missing RBAC permissions
- `watchNamespaceLabelSelector` is invalid or matches no namespace, or `watchNamespaces` contains an empty name

### No Resources Being Synced

Check the status:

```bash
kubectl get resourcesyncconfig default-sync -o jsonpath='{.status.watchedResourceTypes}'
```

If zero, check controller logs:

```bash
kubectl logs -l app.kubernetes.io/name=dot-ai-controller -n dot-ai --tail=50
```

### Sync Errors

Check the `syncErrors` status field and controller logs for details:

```bash
kubectl get resourcesyncconfig default-sync -o jsonpath='{.status.syncErrors}'
```

## Cleanup

Delete the ResourceSyncConfig to stop syncing:

```bash
kubectl delete resourcesyncconfig default-sync
```

This stops the watchers but does not delete synced data from MCP/Qdrant.

## Next Steps

- Learn about [Capability Scanning](capability-scan-guide.md) for autonomous capability discovery
- Explore [Remediation Policies](remediation-guide.md) for event-driven remediation
- Check [Troubleshooting Guide](troubleshooting.md) for common issues
