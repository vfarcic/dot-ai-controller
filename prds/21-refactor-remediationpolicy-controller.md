# PRD: Refactor RemediationPolicy Controller

**Issue**: [#21](https://github.com/vfarcic/dot-ai-controller/issues/21)
**Status**: Complete
**Priority**: Medium
**Created**: 2025-12-03

## Problem Statement

The `remediationpolicy_controller.go` file has grown to 2,361 lines with 50+ functions, making it difficult to:

- **Navigate**: Finding specific functionality requires extensive scrolling/searching
- **Maintain**: Changes to one area risk unintended impacts on unrelated code
- **Review**: Code reviews are challenging due to file size
- **Test**: Test files mirror the monolithic structure, making test organization unclear
- **Onboard**: New contributors struggle to understand the codebase structure

### Current State

The file contains multiple distinct domains mixed together:
- Core reconciliation logic (~300 lines)
- Event matching and processing (~200 lines)
- Rate limiting logic (~150 lines)
- MCP client types and HTTP calls (~250 lines)
- Slack notification types and functions (~600 lines)
- Google Chat notification types and functions (~550 lines)
- Shared notification helpers (~150 lines)

## Solution Overview

Split the controller into logical files by domain, keeping all code within the same `controller` package. Each file should:
- Have a single responsibility
- Be under 700 lines
- Have clear boundaries with other files
- Follow Go conventions for file organization

### Target File Structure

```
internal/controller/
├── remediationpolicy_controller.go      # Core reconciliation (~500 lines)
├── remediationpolicy_ratelimit.go       # Rate limiting logic (~200 lines)
├── remediationpolicy_mcp.go             # MCP types and HTTP client (~250 lines)
├── remediationpolicy_slack.go           # Slack notification code (~650 lines)
├── remediationpolicy_googlechat.go      # Google Chat notification code (~600 lines)
├── remediationpolicy_notifications.go   # Shared notification helpers (~150 lines)
└── remediationpolicy_controller_test.go # Existing tests (may also split)
```

### File Contents

| File | Contents |
|------|----------|
| `remediationpolicy_controller.go` | `RemediationPolicyReconciler` struct, `Reconcile`, `reconcilePolicy`, `reconcileEvent`, `processEvent`, `SetupWithManager`, event matching functions |
| `remediationpolicy_ratelimit.go` | `isRateLimited`, `getRateLimitKey`, `resolveOwnerForRateLimiting`, `parseCronJobNameFromPodName`, cooldown tracking |
| `remediationpolicy_mcp.go` | `McpResponse` type, `sendMcpRequest`, `generateMcpRequest`, `generateAndLogMcpRequest`, `generateIssueDescription` |
| `remediationpolicy_slack.go` | All Slack types (`SlackMessage`, `SlackBlock`, etc.), `sendSlackNotification`, `createSlackMessage`, `sendSlackWebhook`, block creation helpers |
| `remediationpolicy_googlechat.go` | All Google Chat types, `sendGoogleChatNotification`, `createGoogleChatMessage`, `sendGoogleChatWebhook`, section creation helpers |
| `remediationpolicy_notifications.go` | `validateSlackConfiguration`, `validateGoogleChatConfiguration`, `resolveWebhookUrl`, `updateNotificationHealthCondition` |

## Technical Design

### Approach: Iterative Extraction

Refactor one domain at a time in this order (dependencies flow downward):
1. **MCP client** - No dependencies on other extracted files
2. **Rate limiting** - No dependencies on notification code
3. **Slack notifications** - Depends on shared notification helpers
4. **Google Chat notifications** - Depends on shared notification helpers
5. **Shared notification helpers** - Extract last to identify common patterns

### Extraction Process (Per File)

1. Create new file with package declaration and imports
2. Move types and functions to new file
3. Remove moved code from original file
4. Update imports in original file
5. Move related tests to corresponding test file (if applicable)
6. Run `make test` to verify no regressions
7. Commit the change

### Import Management

All files remain in the `controller` package, so no import changes are needed for cross-file function calls. Each new file needs its own import block for external dependencies.

## Success Criteria

1. **No functional changes**: All existing tests pass without modification
2. **Improved readability**: Each file under 700 lines with single responsibility
3. **Clear organization**: Related code grouped together
4. **Maintained test coverage**: Tests reorganized but coverage unchanged
5. **Documentation**: File headers explain each file's purpose

## Milestones

### Milestone 1: Extract MCP Client Code
- [x] Create `remediationpolicy_mcp.go` with MCP types and functions
- [x] Remove MCP code from controller
- [x] Run tests to verify no regressions

### Milestone 2: Extract Rate Limiting Code
- [x] Create `remediationpolicy_ratelimit.go` with rate limiting functions
- [x] Remove rate limiting code from controller
- [x] Run tests to verify no regressions

### Milestone 3: Extract Slack Notifications
- [x] Create `remediationpolicy_slack.go` with Slack types and functions
- [x] Remove Slack code from controller
- [x] Run tests to verify no regressions

### Milestone 4: Extract Google Chat Notifications
- [x] Create `remediationpolicy_googlechat.go` with Google Chat types and functions
- [x] Remove Google Chat code from controller
- [x] Run tests to verify no regressions

### Milestone 5: Extract Shared Notification Helpers
- [x] Create `remediationpolicy_notifications.go` with shared helpers
- [x] Remove shared notification code from controller
- [x] Run tests to verify no regressions
- [x] Verify final controller file is focused on reconciliation

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking existing functionality | High | Run tests after each extraction; commit incrementally |
| Circular dependencies | Medium | Extract in dependency order (MCP first, notifications last) |
| Test file complexity | Low | Consider splitting test file if it exceeds 1000 lines |

## Dependencies

- None - this is an internal refactoring with no external dependencies

## Out of Scope

- Refactoring the Solution controller (already small)
- Changing any public APIs or behaviors
- Adding new functionality
- Modifying test coverage

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-03 | PRD created |
| 2025-12-03 | Implementation complete - all 5 milestones finished |

### 2025-12-03: Implementation Complete

**Files Created:**
- `remediationpolicy_mcp.go` (278 lines) - MCP types and HTTP client
- `remediationpolicy_ratelimit.go` (257 lines) - Rate limiting and cooldown logic
- `remediationpolicy_notifications.go` (221 lines) - Shared notification helpers
- `remediationpolicy_slack.go` (509 lines) - Slack notification code
- `remediationpolicy_googlechat.go` (544 lines) - Google Chat notification code

**Results:**
- Original controller: 2,361 lines → 637 lines (73% reduction)
- All files under 700 lines target
- Test coverage unchanged at 78.3%
- All tests passing

---

*This PRD tracks the refactoring of the RemediationPolicy controller into multiple focused files.*
