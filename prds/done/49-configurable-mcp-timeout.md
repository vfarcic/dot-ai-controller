# PRD #49: Configurable HTTP Timeout for GitKnowledgeSource MCP Calls

**Status**: Complete (2026-02-09)

## Problem Statement

The GitKnowledgeSource controller uses a hardcoded 30-second HTTP timeout (`DefaultMCPTimeout`) for MCP API calls. When ingesting large documents (e.g., 116KB markdown files), the MCP server can take up to 48 seconds to process and respond. This causes the HTTP client to timeout waiting for response headers, resulting in `SyncPartial` warnings and failed document ingestion.

The issue was detected in production when the `devops-catalog` GitKnowledgeSource failed to sync `manuscript/ama/2026-02-06-FF5N7adh5N4.md` (116.95KB), with the remediation system reporting 92% confidence that the hardcoded timeout was the root cause.

## Solution Overview

Add a configurable `httpTimeoutSeconds` field to the shared `McpServerConfig` type, allowing users to tune the HTTP timeout per GitKnowledgeSource CR. Increase the default from 30s to 120s to handle large documents without user intervention.

## User Journey

1. User has a GitKnowledgeSource that fails to sync large documents due to timeout
2. User sees `SyncPartial` warning events with error details
3. **After fix (default change)**: Most users won't need to do anything - the new 120s default handles typical large documents
4. **For edge cases**: User can set `spec.mcpServer.httpTimeoutSeconds: 300` to allow up to 5 minutes for very large documents
5. Sync succeeds without timeout errors

## CRD Change

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: devops-catalog
spec:
  mcpServer:
    url: http://mcp-server.dot-ai.svc:3456
    authSecretRef:
      name: mcp-auth
      key: token
    httpTimeoutSeconds: 180  # NEW: optional, default 120
```

## Technical Approach

### Changes Required

1. **`api/v1alpha1/knowledgesource_common_types.go`** - Add `HttpTimeoutSeconds` field to `McpServerConfig` with kubebuilder markers: default=120, minimum=5

2. **`internal/controller/gitknowledgesource_mcp.go`** - Update `DefaultMCPTimeout` from 30s to 120s, add `Timeout` field to `MCPKnowledgeClientConfig`, use it when creating the default HTTP client

3. **`internal/controller/gitknowledgesource_controller.go`** - At both `NewMCPKnowledgeClient` call sites, read `gks.Spec.McpServer.HttpTimeoutSeconds` and pass it to the client config

4. **Code generation** - Run `make generate manifests` to regenerate deepcopy and CRD manifests

### Design Decisions

- **Shared type**: `McpServerConfig` is used by GitKnowledgeSource and potentially future knowledge source CRDs. Adding the timeout here makes it available to all.
- **Seconds (int32) vs duration string**: Using `int32` seconds is simpler to validate with kubebuilder markers and consistent with existing patterns in this codebase (`debounceWindowSeconds`, `backoffSeconds`).
- **Default 120s**: 4x the previous default, covers the observed 48s processing time with generous headroom. Users with extremely large docs can increase further.

## Success Criteria

1. Default timeout is 120s (no user action needed for typical large documents)
2. Users can configure custom timeout via `spec.mcpServer.httpTimeoutSeconds`
3. Validation prevents unreasonably low values (minimum 5s)
4. All existing tests pass
5. CRD manifests and Helm chart templates updated automatically

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Higher default timeout delays error detection for genuinely failed requests | 120s is still reasonable; users can lower it if needed |
| Breaking change for existing CRs | New field is optional with a default; existing CRs work unchanged |

## Milestones

- [x] **M1: Add configurable timeout** - Add `httpTimeoutSeconds` to `McpServerConfig`, update default to 120s, wire through controller, update tests, run `make generate manifests`
- [x] **M2: Documentation** - Update CLAUDE.md and user docs with new field

## Out of Scope

- Configurable timeouts for ResourceSyncConfig and CapabilityScanConfig (they have separate MCP config patterns and 60s defaults that work fine)
- Per-document timeout (overkill for this use case)
- Configurable retry count/backoff (could be future PRD if needed)

## Decision Log

| Date | Decision | Rationale | Impact |
|------|----------|-----------|--------|
| 2026-02-09 | Use `int32` seconds instead of duration string | Consistent with `debounceWindowSeconds`, `backoffSeconds` patterns in codebase. Easier kubebuilder validation. | Simple field type |
| 2026-02-09 | Default 120s instead of 60s | Observed processing times up to 48s. With 3 retries and backoff, 60s might still fail for edge cases. 120s provides comfortable headroom. | Generous default reduces user intervention |
| 2026-02-09 | Add to shared `McpServerConfig` not GitKnowledgeSource-specific | Future knowledge source CRDs will benefit from same configuration. Consistent API surface. | Field available to all knowledge sources |
