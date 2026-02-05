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
  schedule: "@every 24h"             # default: once per day, staggered
  mcpServer:
    url: http://mcp-server.dot-ai.svc:3456
    authSecretRef:
      name: mcp-auth
      key: token
  metadata:                         # optional, attached to all docs
    source: "platform-docs"
  maxFileSizeBytes: 1048576         # optional, no limit if unset
  deletionPolicy: Delete            # optional, default: Delete. Options: Delete, Retain
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
- HTTPS authentication via token from Secret (SSH not supported)
- Clone directory: `/tmp/knowledge-sources/<namespace>-<name>-<uid[:8]>`
- **Clone-fresh approach**: Clone repository, sync, then delete clone (no persistent storage)
- Use `--shallow-since=<lastSyncTime>` for incremental clones (fetches only commits since last sync)
- First sync uses `--depth=1` (shallow clone)

### Change Detection
- Use git commit diff: `git diff --name-only <lastSyncedCommit>..HEAD`
- `--shallow-since` ensures `lastSyncedCommit` is in the cloned history
- First sync: process all matching files
- Subsequent syncs: only process files changed since last synced commit
- **Spec change triggers full sync**: Compare `metadata.generation` vs `status.observedGeneration`; if different, do full sync (ensures new path patterns pick up existing files)
- Fallback to full sync if `lastSyncedCommit` not found in history (e.g., force push on remote)
- Clone deleted after each sync to avoid storage buildup (important for large monorepos)

### MCP Integration
- Call `POST /api/v1/tools/manageKnowledge` with operations: `ingest`, `deleteByUri`
- Build URIs: `https://github.com/{org}/{repo}/blob/{branch}/{path}`
- Retry with exponential backoff + jitter (3 attempts)
- **Source identifier for bulk operations**: Pass `metadata.sourceIdentifier` with value `"namespace/name"` on every ingest call. This enables MCP to bulk-delete all documents from a source.
- **Bulk deletion**: Use `DELETE /api/v1/knowledge/source/:sourceIdentifier` endpoint for cleanup (CR deletion, full re-sync)

### Scheduling
- Parse cron/interval expressions with `robfig/cron/v3`
- Supports standard cron (`0 3 * * *`) and intervals (`@every 24h`, `@every 6h`)
- Use controller `RequeueAfter` for scheduling (non-blocking, concurrent)
- **Default schedule: `@every 24h`** (staggered - each CR syncs 24h after its last sync)
- Immediate sync on CR creation/update
- **Invalid schedule handling**: Show `ScheduleError` condition in status with parse error message; do not requeue (user must fix CR to trigger next sync)
- **Concurrent sync prevention**: In-memory mutex per CR with `TryLock()`; if sync already running, skip and requeue after 30s
- **Sync timeout**: Overall 30-minute timeout on sync operation to prevent stuck locks

## Success Criteria

1. Documents from Git repositories are searchable via MCP knowledge base
2. Changes are detected and synced within the scheduled interval
3. Deleted files are removed from the knowledge base
4. Status accurately reflects sync state, document counts, and errors
5. Skipped files (size limit) are reported in status
6. Private repositories work with token authentication
7. CR deletion cleans up MCP documents (clone dir already cleaned after each sync)

## Dependencies

- MCP server with `manageKnowledge` API (from dot-ai-prd-356-knowledge-base-system)
- MCP `DELETE /api/v1/knowledge/source/:sourceIdentifier` endpoint (from dot-ai-prd-356-knowledge-base-system) - **required for M8**
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
| Large repositories slow down sync | `--shallow-since` clones only recent commits; clone deleted after sync |
| Large monorepos with docs alongside code | Clone-fresh approach avoids persistent storage; `--shallow-since` minimizes clone size |
| MCP server unavailable | Retry with exponential backoff, error reporting in status |
| Git auth failures | Clear error messages, validate credentials on first sync |
| Race conditions on concurrent syncs | Single active sync per CR, mutex protection |
| Force push on remote invalidates lastSyncedCommit | Fallback to full sync if commit not found in history |

## Milestones

Milestones are ordered so each one delivers working, testable functionality. E2e tests start at M3 when the controller is first wired up.

- [x] **M1: CRD Definition** - GitKnowledgeSource types with spec/status, code generation, sample CR
- [x] **M2: Git Operations** - Git client library (clone, auth, file listing) + pattern matcher library. Unit tests only.
- [x] **M3: MCP Integration** - Wire git+patterns into controller, MCP client, sync docs to MCP. E2e: CR created → docs in MCP
- [x] **M4: Change Detection** - Only sync changed files since last sync (optimization). E2e: second sync is incremental
- [x] **M5: Scheduling** - Cron/interval parsing, RequeueAfter integration. E2e: verify scheduled requeue
- [x] **M6: Skip Tracking** - File size filtering, skipped files in status. TDD approach: (1) Write e2e test first with `maxFileSizeBytes: 1024` expecting 4 docs synced, 1 skipped (api-reference.md at 3.8KB), (2) Run test - should FAIL (feature not implemented, gets 5 docs), (3) Implement skip tracking in controller, (4) Run test - should PASS
- [x] **M7: Source Identifier** - Add `metadata.sourceIdentifier` (namespace/name) to all ingest calls. Enables MCP to track document sources for bulk operations. No external dependency.
- [x] **M8: Finalizer/Cleanup** - Use MCP `deleteBySource` API to remove all documents on CR deletion. Requires MCP endpoint from dot-ai-prd-356. E2e: delete CR → MCP docs removed
- [x] **M9: Documentation** - Update CLAUDE.md, user docs, sample configurations. Include note that the CRD should work with all Git providers (GitHub, GitLab, Bitbucket, Gitea, self-hosted), but testing was done only with GitHub. Welcome user feedback on experience with other providers.
- [x] **M10: Feature Response** - Write response to requesting project with usage examples

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
| 2026-02-03 | Default sync schedule: 24 hours | Documentation doesn't change frequently. Daily sync is reasonable default. Users can customize via `schedule` field. | Added default to scheduling; CRD example updated |
| 2026-02-03 | Clone-fresh approach: delete clone after each sync | Repos may be large monorepos with docs alongside code. Persistent clones would clog storage. Also avoids merge/corruption issues. | Changed from persistent clones to clone-fresh; cleanup happens after sync, not on CR deletion |
| 2026-02-03 | Use `--shallow-since=<lastSyncTime>` for change detection | Enables git diff for change detection without keeping persistent clones. Fetches only commits since last sync, keeping clone size small even for large repos. Falls back to full sync if `lastSyncedCommit` not in history. | M3 milestone approach updated; embeddings only generated for changed files |
| 2026-02-03 | Default schedule `@every 24h` instead of fixed cron time | Using `0 0 * * *` (midnight) would cause thundering herd - all CRs syncing simultaneously. `@every 24h` means each CR syncs 24h after its last sync, naturally staggered based on creation time. robfig/cron/v3 supports both `@every` intervals and standard cron. | Avoids resource spikes; CRD default updated |
| 2026-02-03 | Include e2e tests in each milestone instead of separate M8 | Incremental testing catches integration issues early. Each milestone validates its functionality in a real Kind cluster. Easier to debug when scope is smaller. Removed separate M8 Testing milestone; renumbered M9→M8, M10→M9. | Milestones restructured; e2e tests now part of each milestone |
| 2026-02-03 | Reorder milestones: MCP Integration (M3) before Change Detection (M4) | MCP integration is core functionality; change detection is an optimization. After M3, end-to-end flow works (CR → docs in MCP). Change detection can come later. | Swapped M3 and M4; first e2e testable milestone is now M3 |
| 2026-02-03 | M2 is library code only (unit tests), wiring happens in M3 | Git client and pattern matcher are standalone libraries. No value in wiring to controller without MCP (clone but do nothing). M3 wires everything together for first working e2e test. | M2 complete with unit tests only; e2e tests start at M3 |
| 2026-02-04 | Show error for invalid schedules instead of silent fallback | Silent fallback to default is poor UX. Users should see why their schedule isn't working. Perform sync, set `ScheduleError` condition, don't requeue until user fixes CR. | M5 error handling defined |
| 2026-02-04 | Prevent concurrent syncs with in-memory mutex | If sync takes longer than RequeueAfter interval, overlapping syncs waste resources and cause race conditions. Use `TryLock()` per CR; if locked, skip and requeue after 30s. | M5 concurrency protection added |
| 2026-02-04 | Add 30-minute sync timeout | Prevents stuck locks if sync hangs indefinitely. Git/MCP clients have their own timeouts, but overall timeout provides safety net. Uses `context.WithTimeout()`. | M5 timeout protection added |
| 2026-02-04 | Test scheduling with `@every 1s` interval | Use very short interval in tests, verify `status.lastSyncTime` changes over time. Pragmatic approach that actually tests scheduled execution happens. | M5 test strategy defined |
| 2026-02-04 | Spec changes trigger full sync | If user changes `spec.paths`, incremental sync won't pick up existing files that now match new patterns. Compare `generation` vs `observedGeneration`; if different, do full sync. | M5 scope expanded; fixes edge case in change detection |
| 2026-02-04 | Add test fixtures in `examples/docs/` for deterministic e2e tests | Previous tests used loose assertions (`count >= 1`) against the whole repo, which could change over time. Adding 5 known markdown files (4 small ~250-400 bytes, 1 large ~3.8KB) enables exact count validation. For M6 skip tracking: set `maxFileSizeBytes: 1024` to skip the large file and verify exact counts. Test fixtures pushed directly to main branch so tests always use `branch: main`. | E2e tests updated to use `examples/docs/**/*.md` pattern with exact document counts; enables deterministic skip tracking tests |
| 2026-02-04 | `documentCount` is cumulative counter using increment (`+=`) | When status update triggers re-reconcile, incremental sync processes 0 files but was resetting `documentCount` to 0. Changed to increment so count accumulates across syncs. Count represents total files sent to MCP for processing (new or updated). | M6 implementation; fixed status update race condition |
| 2026-02-04 | Use MCP `sourceIdentifier` for bulk deletion instead of tracking URIs in status | Tracking URIs in controller status has size concerns (etcd 1.5MB limit) and requires iterating through each URI to delete. MCP can store `sourceIdentifier` with each document and provide `deleteBySource` endpoint for efficient bulk deletion. Also solves deleted-files-during-resync problem. | Split M7 into M7 (add sourceIdentifier) and M8 (use deleteBySource); M8 depends on MCP feature from dot-ai-prd-356 |
| 2026-02-04 | Two-phase cleanup implementation | M7 (sourceIdentifier) can proceed immediately; M8 (deleteBySource) blocked on MCP endpoint. Allows progress while waiting for cross-project dependency. | Milestones renumbered: M8→M9, M9→M10 |
| 2026-02-05 | Add `spec.deletionPolicy` field with values `Delete` (default) and `Retain` | Follows Kubernetes PersistentVolume pattern. Allows users to retain documents in MCP when CR is deleted (migration scenarios, prevent accidental data loss). Default `Delete` maintains expected cleanup behavior. | M8 implementation; CRD updated with new optional field |
| 2026-02-05 | Documentation at `docs/knowledge-source-guide.md` (URL: `/docs/controller/knowledge-source-guide`) | Named `knowledge-source-guide` (not `git-knowledge-source-guide`) to allow for future source types. MCP server docs link to this URL for cross-project integration. | M9 documentation; committed URL to MCP server project |
