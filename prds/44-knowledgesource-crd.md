# PRD #44: GitKnowledgeSource CRD for Git Repository Document Ingestion

## Problem Statement

The MCP server provides a `manageKnowledge` API for ingesting, searching, and deleting documents from a vector-based knowledge base. However, the MCP server doesn't handle:
- Git operations (clone, pull, diff)
- Source configuration management
- Scheduled syncing of documents
- Change detection (which files changed since last sync)

Users need a Kubernetes-native way to declare Git repositories containing documentation and have those documents automatically synced to the knowledge base.

## Solution Overview

Create a `GitKnowledgeSource` CRD and controller that:
1. Defines a declarative way to specify Git repositories and file patterns
2. Periodically clones/pulls repositories and detects changes via git commit diff
3. Syncs new/modified documents to MCP via the `manageKnowledge` API
4. Removes deleted documents from the knowledge base
5. Reports sync status, document counts, and skipped files

## User Journey

1. User creates a `GitKnowledgeSource` CR specifying:
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
kind: GitKnowledgeSource
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
- Use `go-git/go-git/v6` library (pure Go, no external binary)
- Support shallow clones for efficiency
- HTTPS authentication via token from Secret (SSH not supported)
- Clone directory: `/tmp/knowledge-sources/<namespace>-<name>-<uid[:8]>`

### Change Detection
- Use git commit diff: `git diff --name-only <lastSyncedCommit>..HEAD`
- Leverages `lastSyncedCommit` already stored in status
- First sync: process all matching files
- Subsequent syncs: only process files changed since last synced commit
- No ConfigMap needed for state - commit SHA in status is sufficient

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
7. CR deletion cleans up all state (clone dir, MCP documents)

## Dependencies

- MCP server with `manageKnowledge` API (from dot-ai-prd-356-knowledge-base-system)
- Go libraries: `go-git/go-git/v6`, `robfig/cron/v3`, `bmatcuk/doublestar/v4`

## Test Infrastructure

### Test Repositories
- **Public repo**: `vfarcic/dot-ai-controller` (this repo) - for testing public clone
- **Private repo**: `vfarcic/dot-ai-test-private` - for testing token authentication
  - Contains a few dummy markdown files for pattern matching tests
  - GitHub token stored as GitHub Actions secret `TEST_PRIVATE_REPO_TOKEN`

### E2E Test Setup
- Create Kubernetes Secret from GitHub Actions secret during test setup
- Test both public and private repo scenarios
- Verify authentication failures produce clear error messages

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Large repositories slow down sync | Shallow clones, file size limits |
| MCP server unavailable | Retry with exponential backoff, error reporting in status |
| Git auth failures | Clear error messages, validate credentials on first sync |
| Race conditions on concurrent syncs | Single active sync per CR, mutex protection |

## Milestones

- [x] **M1: CRD Definition** - GitKnowledgeSource types with spec/status, code generation, sample CR
- [ ] **M2: Git Operations** - Clone/pull with go-git, HTTPS auth, file pattern matching
- [ ] **M3: Change Detection** - Git commit diff to identify changed files since last sync
- [ ] **M4: MCP Integration** - Client with retry logic, ingest/delete operations, URI construction
- [ ] **M5: Controller Logic** - Reconciliation loop, finalizer for cleanup, status updates
- [ ] **M6: Scheduling** - Cron parsing, RequeueAfter integration, next sync time tracking
- [ ] **M7: Skip Tracking** - File size filtering, skipped files in status
- [ ] **M8: Testing** - Unit tests, integration tests with envtest, all tests passing
- [ ] **M9: Documentation** - Update CLAUDE.md, user docs, sample configurations. Include note that the CRD should work with all Git providers (GitHub, GitLab, Bitbucket, Gitea, self-hosted), but testing was done only with GitHub. Welcome user feedback on experience with other providers.
- [ ] **M10: Feature Response** - Write response to requesting project with usage examples

## Out of Scope

- SSH key authentication (HTTPS tokens only)
- Webhook-triggered sync (only cron-based)
- Multiple branches per source (one branch per CR)
- Non-Git sources (only Git repositories) - future sources (Slack, Confluence) will be separate CRDs
- TLS options for self-hosted Git servers (insecureSkipTLS, custom CA) - can be added later

## Decision Log

| Date | Decision | Rationale | Impact |
|------|----------|-----------|--------|
| 2026-02-03 | Use go-git v6 instead of v5 | v6 released Jan 2026; no existing v5 code to migrate | Updated dependencies |
| 2026-02-03 | Use git commit diff instead of content hashing for change detection | Content hashing requires reading all files each sync, which is expensive for large repos. Git natively tracks changes between commits efficiently. `lastSyncedCommit` already in status provides the baseline. | Removed ConfigMap state requirement; simplified M3 milestone |
| 2026-02-03 | Rename to `GitKnowledgeSource`; future sources get separate CRDs | Different source types (Git, Slack, Confluence) have fundamentally different configs, auth, and sync mechanisms. Separate CRDs allow independent evolution. Git providers (GitHub, GitLab, Bitbucket) share same CRD since protocol is identical. Name makes it clear this is for Git repos. | Renamed CRD from KnowledgeSource to GitKnowledgeSource |
| 2026-02-03 | Defer TLS options (insecureSkipTLS, custom CA) for self-hosted Git servers | Current design works for public providers. Enterprise self-hosted support can be added when needed. | Keep initial scope simple |
| 2026-02-03 | Rely on CR status for `lastSyncedCommit` (no ConfigMap) | Status persists across controller restarts. Full resync on CR recreation is acceptable since MCP does upserts (idempotent). | Simpler implementation |
