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
├── remediationpolicy_controller.go           # Core reconciliation (~500 lines)
├── remediationpolicy_ratelimit.go            # Rate limiting logic (~200 lines)
├── remediationpolicy_mcp.go                  # MCP types and HTTP client (~250 lines)
├── remediationpolicy_slack.go                # Slack notification code (~650 lines)
├── remediationpolicy_googlechat.go           # Google Chat notification code (~600 lines)
├── remediationpolicy_notifications.go        # Shared notification helpers (~150 lines)
├── remediationpolicy_controller_test.go      # Core reconciliation tests
├── remediationpolicy_ratelimit_test.go       # Rate limiting tests
├── remediationpolicy_mcp_test.go             # MCP client tests
├── remediationpolicy_slack_test.go           # Slack notification tests
├── remediationpolicy_googlechat_test.go      # Google Chat notification tests
└── remediationpolicy_notifications_test.go   # Shared notification tests
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

### Milestone 6: Remove License Headers
- [x] Remove Apache license headers from all source files in `internal/controller/`
- [x] Remove Apache license headers from all source files in `api/`
- [x] Remove Apache license headers from `cmd/main.go`
- [x] Verify LICENSE file exists at repository root
- [x] Run tests to verify no regressions

### Milestone 7: Refactor Test Files
- [x] Create `remediationpolicy_mcp_test.go` with MCP-related tests
- [x] Create `remediationpolicy_ratelimit_test.go` with rate limiting tests
- [x] Create `remediationpolicy_slack_test.go` with Slack notification tests
- [x] Create `remediationpolicy_googlechat_test.go` with Google Chat tests
- [x] Create `remediationpolicy_notifications_test.go` with shared notification tests
- [x] Verify remaining `remediationpolicy_controller_test.go` contains only core reconciliation tests
- [x] Run tests to verify no regressions

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

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2025-12-03 | Split test file to match source structure | Test file is 4,150 lines - larger than original controller. Splitting mirrors source organization and improves maintainability. |
| 2025-12-03 | Remove license headers from all code files | LICENSE file at repo root covers all files. Per-file headers are redundant boilerplate. |

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-03 | PRD created |
| 2025-12-03 | Milestones 1-5 complete - source files refactored |
| 2025-12-03 | Added Milestone 6 (license removal) and Milestone 7 (test refactoring) |
| 2025-12-03 | Milestone 6 complete - license headers removed from all source files |
| 2025-12-03 | Milestone 7 complete - test files refactored. PRD complete! |

### 2025-12-03: License Header Removal

**Changes Made:**
- Emptied `hack/boilerplate.go.txt` to prevent license headers from being regenerated by controller-gen
- Removed Apache license headers from 5 files in `internal/controller/`
- Removed Apache license headers from 5 files in `api/v1alpha1/`
- Removed Apache license headers from `cmd/main.go`
- Removed Apache license headers from 3 files in `test/`

**Results:**
- All 14 Go files cleaned of license boilerplate
- LICENSE file at repository root provides coverage
- All tests passing (78.3% coverage maintained)

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

### 2025-12-03: Test File Refactoring Complete

**Files Created:**
- `remediationpolicy_mcp_test.go` (187 lines) - MCP message generation tests
- `remediationpolicy_ratelimit_test.go` (780 lines) - Rate limiting and owner resolution tests
- `remediationpolicy_slack_test.go` (697 lines) - Slack notification tests
- `remediationpolicy_googlechat_test.go` (320 lines) - Google Chat notification tests
- `remediationpolicy_notifications_test.go` (793 lines) - Secret resolution and dual notification tests

**Results:**
- Original test file: 4,134 lines → 1,526 lines (63% reduction)
- Tests organized to mirror source file structure
- Test coverage unchanged at 78.3%
- All tests passing

---

*This PRD tracks the refactoring of the RemediationPolicy controller into multiple focused files.*
