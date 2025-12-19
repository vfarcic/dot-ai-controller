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

Create a ResourceSyncConfig to start syncing resources:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: default-sync
spec:
  mcpEndpoint: http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/resources/sync
  debounceWindowSeconds: 10
  resyncIntervalMinutes: 60
```

Apply it:

```bash
kubectl apply -f resourcesyncconfig.yaml
```

## Configuration

### Spec Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mcpEndpoint` | string | required | URL of the MCP resource sync endpoint |
| `debounceWindowSeconds` | int | 10 | Time window to batch changes before syncing |
| `resyncIntervalMinutes` | int | 60 | Full resync interval (catches missed events) |
| `mcpAuthSecretRef` | SecretReference | optional | Secret containing API key for MCP authentication |

### Authentication

If your MCP endpoint requires authentication, reference a secret:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: authenticated-sync
spec:
  mcpEndpoint: https://mcp.example.com/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-credentials
    key: api-key
```

Create the secret:

```bash
kubectl create secret generic mcp-credentials \
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

For each resource, the following is synced to MCP:
- Kind, APIVersion, Name, Namespace
- Labels and select annotations
- Complete status object (conditions, replica counts, etc.)
- Creation and update timestamps

### What's NOT Synced

- Full resource spec (fetched on-demand from Kubernetes when needed)
- High-volume resources: Events, Leases, EndpointSlices
- Large annotations like `kubectl.kubernetes.io/last-applied-configuration`

## Semantic Search

Once resources are synced, you can search using natural language through MCP:

```
"show me all failing pods"
"what databases are running?"
"list deployments in production namespace"
```

MCP uses semantic embeddings to understand intent, so you don't need to know exact resource kinds or field names.

## Example: Full Configuration

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: production-sync
spec:
  # MCP endpoint for resource sync
  mcpEndpoint: http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/resources/sync

  # Batch changes for 10 seconds before syncing
  debounceWindowSeconds: 10

  # Full resync every hour to catch any missed events
  resyncIntervalMinutes: 60

  # Optional: authentication for MCP
  # mcpAuthSecretRef:
  #   name: mcp-credentials
  #   key: api-key
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
