# PRD #44: KnowledgeSource CRD for Git Repository Document Ingestion

## Problem Statement

The MCP server provides a `manageKnowledge` API for ingesting, searching, and deleting documents from a vector-based knowledge base. However, the MCP server doesn't handle:
- Git operations (clone, pull, diff)
- Source configuration management
- Scheduled syncing of documents
- Change detection (which files changed since last sync)

Users need a Kubernetes-native way to declare Git repositories containing documentation and have those documents automatically synced to the knowledge base.

## Solution Overview

Create a `KnowledgeSource` CRD and controller that:
1. Defines a declarative way to specify Git repositories and file patterns
2. Periodically clones/pulls repositories and detects changes via file hash comparison
3. Syncs new/modified documents to MCP via the `manageKnowledge` API
4. Removes deleted documents from the knowledge base
5. Reports sync status, document counts, and skipped files

## User Journey

1. User creates a `KnowledgeSource` CR specifying:
   - Git repository URL and branch
   - File patterns to include (e.g., `docs/**/*.md`)
   - Optional exclusion patterns
   - Optional cron schedule for periodic sync
   - MCP server endpoint and auth credentials

2. Controller immediately syncs matching documents on CR creation

3. If schedule is configured, controller re-syncs at specified intervals, only processing changed files

4. User can monitor sync status via `kubectl get knowledgesources` showing document counts, last sync time, and errors

5. When CR is deleted, controller cleans up ingested documents from MCP

## CRD Specification

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: KnowledgeSource
metadata:
  name: platform-docs
spec:
  repository:
    url: https://github.com/acme/platform.git
    branch: main                    # default: main
    depth: 1                        # shallow clone depth, default: 1
    secretRef:                      # optional, for private repos
      name: github-credentials
      key: token
  paths:
    - "docs/**/*.md"
    - "README.md"
  exclude:                          # optional
    - "docs/internal/**"
  schedule: "0 */6 * * *"           # optional cron format
  mcpServer:
    url: http://mcp-server.dot-ai.svc:3456
    authSecretRef:
      name: mcp-auth
      key: token
  metadata:                         # optional, attached to all docs
    source: "platform-docs"
  maxFileSizeBytes: 1048576         # optional, no limit if unset
status:
  active: true
  lastSyncTime: "2024-01-15T10:30:00Z"
  lastSyncedCommit: "abc123"
  nextScheduledSync: "2024-01-15T16:00:00Z"
  documentCount: 42
  skippedDocuments: 2
  skippedFiles:
    - path: "docs/large-generated.md"
      reason: "exceeded max file size (15MB > 1MB)"
  syncErrors: 0
  lastError: ""
  conditions:
    - type: Ready
      status: "True"
      reason: Syncing
```

## Technical Approach

### Git Operations
- Use `go-git/go-git/v5` library (pure Go, no external binary)
- Support shallow clones for efficiency
- HTTPS authentication via token from Secret (SSH not supported)
- Clone directory: `/tmp/knowledge-sources/<namespace>-<name>-<uid[:8]>`

### Change Detection
- Compute SHA256 hashes of file contents
- Store hashes in ConfigMap: `<name>-sync-state` with ownerReference
- Compare hashes between syncs to detect additions, modifications, deletions

### MCP Integration
- Call `POST /api/v1/tools/manageKnowledge` with operations: `ingest`, `deleteByUri`
- Build URIs: `https://github.com/{org}/{repo}/blob/{branch}/{path}`
- Retry with exponential backoff + jitter (3 attempts)

### Scheduling
- Parse cron expressions with `robfig/cron/v3`
- Use controller `RequeueAfter` for scheduling
- Immediate sync on CR creation/update

## Success Criteria

1. Documents from Git repositories are searchable via MCP knowledge base
2. Changes are detected and synced within the scheduled interval
3. Deleted files are removed from the knowledge base
4. Status accurately reflects sync state, document counts, and errors
5. Skipped files (size limit) are reported in status
6. Private repositories work with token authentication
7. CR deletion cleans up all state (clone dir, ConfigMap, MCP documents)

## Dependencies

- MCP server with `manageKnowledge` API (from dot-ai-prd-356-knowledge-base-system)
- Go libraries: `go-git/go-git/v5`, `robfig/cron/v3`, `bmatcuk/doublestar/v4`

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Large repositories slow down sync | Shallow clones, file size limits |
| MCP server unavailable | Retry with exponential backoff, error reporting in status |
| Git auth failures | Clear error messages, validate credentials on first sync |
| Race conditions on concurrent syncs | Single active sync per CR, mutex protection |

## Milestones

- [ ] **M1: CRD Definition** - KnowledgeSource types with spec/status, code generation, sample CR
- [ ] **M2: Git Operations** - Clone/pull with go-git, HTTPS auth, file pattern matching
- [ ] **M3: Change Detection** - Hash computation, ConfigMap state persistence, diff algorithm
- [ ] **M4: MCP Integration** - Client with retry logic, ingest/delete operations, URI construction
- [ ] **M5: Controller Logic** - Reconciliation loop, finalizer for cleanup, status updates
- [ ] **M6: Scheduling** - Cron parsing, RequeueAfter integration, next sync time tracking
- [ ] **M7: Skip Tracking** - File size filtering, skipped files in status
- [ ] **M8: Testing** - Unit tests, integration tests with envtest, all tests passing
- [ ] **M9: Documentation** - Update CLAUDE.md, user docs, sample configurations
- [ ] **M10: Feature Response** - Write response to requesting project with usage examples

## Out of Scope

- SSH key authentication (HTTPS tokens only)
- Webhook-triggered sync (only cron-based)
- Multiple branches per source (one branch per CR)
- Non-Git sources (only Git repositories)
