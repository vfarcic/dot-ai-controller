# Knowledge Source Guide

This guide covers the GitKnowledgeSource CRD for automatically syncing documentation from Git repositories into the DevOps AI Toolkit knowledge base.

## Overview

The GitKnowledgeSource enables:
- **Document Ingestion**: Automatically syncs markdown and other files to the knowledge base
- **Change Detection**: Only processes files changed since the last sync
- **Scheduled Sync**: Periodically re-syncs to capture updates
- **Automatic Cleanup**: Removes documents from knowledge base when the resource is deleted

Once documents are synced, they become searchable through the DevOps AI Toolkit's semantic search capabilities.

## Stack Installation

If you installed via the [DevOps AI Toolkit Stack](https://devopstoolkit.ai/docs/stack), you can create GitKnowledgeSource resources immediately. Verify the CRD is available:

```bash
kubectl get crds gitknowledgesources.dot-ai.devopstoolkit.live
```

Continue below to configure a GitKnowledgeSource for your documentation.

## Prerequisites

- Controller installed (see [Setup Guide](setup-guide.md))
- [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) installed and running

## Quick Start

1. Ensure the MCP authentication secret exists:

```bash
kubectl get secret dot-ai-secrets -n dot-ai
```

If not, create it:

```bash
kubectl create secret generic dot-ai-secrets \
  --namespace dot-ai \
  --from-literal=auth-token=your-auth-token-here
```

2. Create a GitKnowledgeSource to sync documentation from a Git repository:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: my-docs
  namespace: dot-ai
spec:
  repository:
    url: https://github.com/your-org/your-repo.git
    branch: main
  paths:
    - "docs/**/*.md"
    - "README.md"
  mcpServer:
    url: http://dot-ai.dot-ai.svc:3456
    authSecretRef:
      name: dot-ai-secrets
      key: auth-token
```

3. Apply it:

```bash
kubectl apply -f gitknowledgesource.yaml
```

4. Check the sync status:

```bash
kubectl get gitknowledgesource my-docs -n dot-ai
```

Expected output:
```
NAME      ACTIVE   DOCUMENTS   LAST SYNC              NEXT SYNC
my-docs   true     9           2026-02-05T16:40:14Z   2026-02-06T16:40:14Z
```

## How It Works

### Sync Process

1. **Clone**: Controller performs a shallow clone of the repository
2. **Pattern Match**: Finds files matching `paths` patterns, excluding `exclude` patterns
3. **Change Detection**: Compares current commit with `lastSyncedCommit` to find changed files
4. **Ingest**: Sends changed documents to MCP knowledge base with `sourceIdentifier`
5. **Cleanup**: Deletes the local clone (no persistent storage required)
6. **Schedule**: Queues next sync based on `schedule` field

### First Sync vs Incremental Sync

- **First sync**: Processes all matching files (full sync)
- **Subsequent syncs**: Only processes files changed since `lastSyncedCommit`
- **Spec changes**: Modifying `paths` or other spec fields triggers a full sync

### What Gets Synced

Each document is ingested to MCP with:
- **Content**: The file contents
- **URI**: `https://github.com/{org}/{repo}/blob/{branch}/{path}`
- **Source Identifier**: `{namespace}/{name}` for bulk operations
- **Custom Metadata**: Values from `spec.metadata` field

### Cleanup on Deletion

When a GitKnowledgeSource is deleted:
1. Controller detects deletion via finalizer
2. Checks `deletionPolicy` (`Delete` or `Retain`)
3. If `Delete`: Calls MCP to remove all documents with matching `sourceIdentifier`
4. Removes finalizer, allowing CR deletion to complete

## Configuration

### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `repository.url` | string | Yes | - | Git repository URL (HTTPS only) |
| `repository.branch` | string | No | `main` | Branch to sync |
| `repository.depth` | int | No | `1` | Shallow clone depth |
| `repository.secretRef` | SecretReference | No | - | Secret with token for private repos |
| `paths` | []string | Yes | - | Glob patterns for files to sync (e.g., `docs/**/*.md`) |
| `exclude` | []string | No | - | Glob patterns to exclude |
| `schedule` | string | No | `@every 24h` | Sync schedule (cron or interval) |
| `mcpServer.url` | string | Yes | - | MCP server endpoint URL |
| `mcpServer.authSecretRef` | SecretReference | Yes | - | Secret with MCP auth token |
| `metadata` | map[string]string | No | - | Custom metadata attached to all documents |
| `maxFileSizeBytes` | int | No | - | Skip files larger than this size |
| `deletionPolicy` | string | No | `Delete` | `Delete` or `Retain` documents on CR deletion |

### Repository Authentication

For private repositories, create a secret with a personal access token:

```bash
kubectl create secret generic github-token \
  --namespace dot-ai \
  --from-literal=token=ghp_xxxxxxxxxxxx
```

Reference it in the GitKnowledgeSource:

```yaml
spec:
  repository:
    url: https://github.com/your-org/private-repo.git
    secretRef:
      name: github-token
      key: token
```

### Path Patterns

The `paths` field uses glob patterns to match files:

| Pattern | Matches |
|---------|---------|
| `docs/**/*.md` | All markdown files under `docs/` recursively |
| `README.md` | Only the root README |
| `**/*.md` | All markdown files in the repository |
| `docs/*.md` | Markdown files directly in `docs/` (not subdirectories) |

Use `exclude` to skip specific paths:

```yaml
spec:
  paths:
    - "docs/**/*.md"
  exclude:
    - "docs/internal/**"
    - "docs/drafts/**"
```

### Schedule Configuration

The `schedule` field accepts cron expressions or interval syntax:

| Format | Example | Description |
|--------|---------|-------------|
| Interval | `@every 24h` | Sync every 24 hours (default) |
| Interval | `@every 6h` | Sync every 6 hours |
| Interval | `@every 30m` | Sync every 30 minutes |
| Cron | `0 3 * * *` | Daily at 3:00 AM |
| Cron | `0 */6 * * *` | Every 6 hours |

The default `@every 24h` means each GitKnowledgeSource syncs 24 hours after its last sync, naturally staggering syncs based on creation time.

**Invalid schedules**: If you specify an invalid schedule expression, the controller will sync once, then set a `ScheduleError` condition and stop scheduling. Fix the schedule to resume.

### File Size Limits

Use `maxFileSizeBytes` to skip large files:

```yaml
spec:
  maxFileSizeBytes: 1048576  # 1MB limit
```

Skipped files appear in the status:

```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o jsonpath='{.status.skippedFiles}' | jq
```

### Deletion Policy

The `deletionPolicy` controls what happens when the GitKnowledgeSource is deleted:

| Value | Behavior |
|-------|----------|
| `Delete` (default) | Remove all synced documents from MCP knowledge base |
| `Retain` | Keep documents in MCP (useful for migrations) |

```yaml
spec:
  deletionPolicy: Retain  # Keep docs when CR is deleted
```

## Status

Check the status to monitor sync progress:

```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o yaml
```

### Status Fields

| Field | Description |
|-------|-------------|
| `active` | Whether the source is actively syncing |
| `documentCount` | Total documents synced to MCP |
| `lastSyncTime` | Timestamp of last successful sync |
| `lastSyncedCommit` | Git commit SHA of last sync |
| `nextScheduledSync` | When the next sync will occur |
| `skippedDocuments` | Count of files skipped (e.g., size limit) |
| `skippedFiles` | Details of skipped files with reasons |
| `syncErrors` | Count of sync errors |
| `lastError` | Most recent error message |
| `observedGeneration` | Last processed spec generation |
| `conditions` | Standard Kubernetes conditions |

### Conditions

| Type | Description |
|------|-------------|
| `Ready` | True when source is active and configured correctly |
| `Synced` | True when last sync completed successfully |
| `Scheduled` | True when next sync is scheduled |

### Example Status

```yaml
status:
  active: true
  documentCount: 9
  lastSyncTime: "2026-02-05T16:40:14Z"
  lastSyncedCommit: "c32655af7f70361835a533e57533caaf4e8b750a"
  nextScheduledSync: "2026-02-06T16:40:14Z"
  conditions:
  - type: Ready
    status: "True"
    reason: Active
    message: "GitKnowledgeSource is active and syncing"
  - type: Synced
    status: "True"
    reason: SyncComplete
    message: "Successfully synced 9 documents"
  - type: Scheduled
    status: "True"
    reason: Scheduled
    message: "Next sync scheduled for 2026-02-06T16:40:14Z"
```

## Troubleshooting

### Sync Not Starting

Check the Ready condition:

```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o jsonpath='{.status.conditions}' | jq
```

Common issues:
- **CloneError**: Invalid repository URL or authentication failure
- **MCP unreachable**: Check MCP server URL and network connectivity
- **Missing secret**: Verify auth secret exists and has correct keys

### Clone Errors

If you see "read-only file system" errors:
- Ensure the controller deployment has a writable `/tmp` volume mount

If you see authentication errors for private repos:
- Verify the secret exists: `kubectl get secret <name> -n dot-ai`
- Check the token has read access to the repository
- Ensure `secretRef.key` matches the key in the secret

### Documents Not Appearing in Search

1. Check sync completed successfully:
```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o jsonpath='{.status.documentCount}'
```

2. Verify MCP is running:
```bash
kubectl get pods -n dot-ai -l app=dot-ai
```

3. Check for sync errors:
```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o jsonpath='{.status.lastError}'
```

### Schedule Not Working

Check the Scheduled condition:

```bash
kubectl get gitknowledgesource my-docs -n dot-ai -o jsonpath='{.status.conditions}' | jq '.[] | select(.type=="Scheduled")'
```

If `ScheduleError`, the schedule expression is invalid. Fix the `spec.schedule` field.

## Git Provider Compatibility

GitKnowledgeSource uses standard Git HTTPS protocol and should work with any Git provider:
- GitHub
- GitLab
- Bitbucket
- Gitea
- Self-hosted Git servers

Testing has been performed primarily with GitHub. If you encounter issues with other providers, please [report them on GitHub](https://github.com/vfarcic/dot-ai-controller/issues).

## Next Steps

- Learn about [Resource Sync](resource-sync-guide.md) for cluster resource visibility
- Explore [Remediation Policies](remediation-guide.md) for event-driven remediation
- Check [Troubleshooting Guide](troubleshooting.md) for common issues
