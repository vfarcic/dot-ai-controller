# Resource Sync Guide

This guide covers the ResourceSyncConfig CRD for enabling resource visibility and semantic search in your Kubernetes cluster.

## Overview

The ResourceSyncConfig enables:
- **Resource Discovery**: Automatically discovers all resource types in your cluster
- **Change Tracking**: Watches for resource changes (create, update, delete)
- **Semantic Search**: Syncs resource metadata to MCP for natural language queries

## Prerequisites

- Controller installed (see [Setup Guide](setup-guide.md))
- [DevOps AI Toolkit MCP](https://github.com/vfarcic/dot-ai) installed and running

## Quick Start

1. Create a secret with your MCP API key:

```bash
kubectl create secret generic mcp-credentials \
  --namespace dot-ai \
  --from-literal=api-key=your-api-key-here
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
    name: mcp-credentials
    key: api-key
  debounceWindowSeconds: 10
  resyncIntervalMinutes: 60
```

3. Apply it:

```bash
kubectl apply -f resourcesyncconfig.yaml
```

## Configuration

### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mcpEndpoint` | string | Yes | - | Full URL of the MCP resource sync endpoint |
| `mcpAuthSecretRef` | SecretReference | Yes | - | Secret containing API key for MCP authentication |
| `debounceWindowSeconds` | int | No | 10 | Time window to batch changes before syncing |
| `resyncIntervalMinutes` | int | No | 60 | Full resync interval (catches missed events) |

### Authentication

The `mcpAuthSecretRef` field is required and must reference a Kubernetes Secret in the same namespace as the ResourceSyncConfig:

```yaml
mcpAuthSecretRef:
  name: mcp-credentials  # Secret name
  key: api-key           # Key within the secret containing the token
```

Create the secret in the same namespace:

```bash
kubectl create secret generic mcp-credentials \
  --namespace dot-ai \
  --from-literal=api-key=your-api-key-here
```

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
    name: mcp-credentials
    key: api-key

  # Batch changes for 10 seconds before syncing
  debounceWindowSeconds: 10

  # Full resync every hour to catch any missed events
  resyncIntervalMinutes: 60
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
