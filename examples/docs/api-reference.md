# API Reference

Complete API reference for all Custom Resource Definitions provided by the DevOps AI Toolkit Controller.

## Solution

The Solution CRD groups related Kubernetes resources as a logical unit for tracking and lifecycle management.

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| resources | []ResourceRef | Yes | List of resources to track |
| healthChecks | []HealthCheck | No | Custom health check definitions |
| ownershipPolicy | string | No | How to handle ownership (Adopt, Reference) |

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| phase | string | Current phase (Pending, Ready, Failed) |
| resourceCount | int | Number of tracked resources |
| healthyCount | int | Number of healthy resources |
| conditions | []Condition | Detailed status conditions |

### Example

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: my-app
spec:
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: my-app
    - apiVersion: v1
      kind: Service
      name: my-app
```

## GitKnowledgeSource

The GitKnowledgeSource CRD syncs documents from Git repositories to the MCP knowledge base for semantic search.

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| repository.url | string | Yes | Git repository HTTPS URL |
| repository.branch | string | No | Branch to sync (default: main) |
| repository.depth | int | No | Shallow clone depth (default: 1) |
| repository.secretRef | SecretRef | No | Secret containing auth token |
| paths | []string | Yes | Glob patterns for files to include |
| exclude | []string | No | Glob patterns for files to exclude |
| schedule | string | No | Cron or interval expression (default: @every 24h) |
| maxFileSizeBytes | int64 | No | Maximum file size limit |
| mcpServer.url | string | Yes | MCP server endpoint URL |
| mcpServer.authSecretRef | SecretRef | Yes | Secret containing MCP auth token |
| metadata | map[string]string | No | Metadata attached to all documents |

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| active | bool | Whether sync is active |
| documentCount | int | Number of synced documents |
| skippedDocuments | int | Number of skipped documents |
| skippedFiles | []SkippedFile | Details of skipped files |
| lastSyncTime | Time | Timestamp of last sync |
| lastSyncedCommit | string | Git commit SHA of last sync |
| nextScheduledSync | Time | Next scheduled sync time |
| syncErrors | int | Number of errors in last sync |
| lastError | string | Most recent error message |
| conditions | []Condition | Detailed status conditions |

### Example

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: platform-docs
spec:
  repository:
    url: https://github.com/acme/platform.git
    branch: main
  paths:
    - "docs/**/*.md"
  exclude:
    - "docs/internal/**"
  schedule: "@every 12h"
  maxFileSizeBytes: 1048576
  mcpServer:
    url: http://mcp-server:3456
    authSecretRef:
      name: mcp-auth
      key: token
```

## RemediationPolicy

The RemediationPolicy CRD enables event-driven remediation with AI-powered analysis.

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| eventSelector | EventSelector | Yes | Filter for Kubernetes events |
| mode | string | No | Execution mode (automatic, manual) |
| mcpEndpoint | string | Yes | MCP server endpoint |
| rateLimiting | RateLimitConfig | No | Rate limiting configuration |

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| processedEvents | int | Total events processed |
| remediationsApplied | int | Successful remediations |
| lastEventTime | Time | Time of last processed event |
