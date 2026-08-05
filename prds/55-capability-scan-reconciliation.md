# PRD: Fix Capability Scan Reconciliation

**Issue**: #55
**Status**: Draft
**Priority**: High
**Created**: 2026-08-05
**Last Updated**: 2026-08-05

## Related Context

Reported in [vfarcic/dot-ai discussion #709](https://github.com/vfarcic/dot-ai/discussions/709) by @FlorianGerdes, who hit this repeatedly on fresh `dot-ai-stack` installs, diagnosed it in a downstream fork, and patched it there.

This PRD supersedes the **MCP Server API Reference** section of [PRD #34](prds/done/34-autonomous-capability-scanning.md#L132-L256), which documents the wrong response shape and is the origin of the defect.

Companion work in the MCP server: [vfarcic/dot-ai#714](https://github.com/vfarcic/dot-ai/issues/714). See **Sequencing** below — that work is not a prerequisite, but one guard here exists specifically because it may not be deployed.

## Problem Statement

On a fresh install the `capabilities` collection can stay empty indefinitely while `CapabilityScanConfig` reports `Ready`, carries no `lastError`, records a recent `lastScanTime`, and the controller pod runs with zero restarts. Every status surface claims health. Restarting the controller is the only reliable fix, which is what pointed the reporter at startup-bound behavior.

The reporter describes three bugs. In the code it is **one root cause plus two independent gaps**.

### Root cause: `ManageOrgDataResponse` does not match what the server returns

`internal/controller/capabilityscan_mcp.go:43-59` declares:

```go
Data *struct {
    Result *struct {
        Success      bool             `json:"success"`
        Capabilities []CapabilityInfo `json:"capabilities,omitempty"`
        TotalCount   int              `json:"totalCount,omitempty"`
        ...
    } `json:"result,omitempty"`
} `json:"data,omitempty"`
```

Three separate mismatches, each independently sufficient to break reconciliation.

**(a) The payload is one level deeper.** The REST layer wraps tool output as `{success, data:{result:<toolOutput>, tool, executionTime}, meta}` (`dot-ai` `src/interfaces/rest-api.ts:918`), and the tool output is itself `{success, operation, dataType, data:{capabilities, totalCount, returnedCount, limit}}` (`dot-ai` `src/core/capability-operations.ts:113`). The real path is `data.result.data.capabilities`, as the server's own integration tests assert (`dot-ai` `tests/integration/tools/manage-org-data-capabilities.test.ts:157`).

⇒ `GetCapabilityIDs()` always returns empty. `GetTotalCount()` always returns 0.

**(b) `id` is not the resource identity.** It is a deterministic UUID, `sha256("capability-" + resourceName)` reformatted 8-4-4-4-12 (`dot-ai` `src/core/capabilities.ts:328-337`). `computeCapabilityDiff` compares it against `Kind.group` strings built from the Discovery API (`capabilityscan_controller.go:442-454`), so it could never match even with (a) fixed. The field carrying `Kind.group` is `resourceName`, already present in the same payload.

**(c) `resp.Success` is the transport flag, not the operation result.** `manageOrgData` catches every error and returns normally (`dot-ai` `src/tools/organizational-data.ts:868-905`), so the envelope's `success` means only "the handler did not throw." An unreachable Qdrant yields `{success:false, error:{message:'Vector DB (Qdrant) connection required'}}` at `data.result` under **HTTP 200 with envelope `success:true`** (`dot-ai` `src/core/capability-operations.ts:884-897`). Nothing in this repo reads `data.result.success`, so for this endpoint `resp.Success` is effectively a constant `true`.

Note the inner flag *does* distinguish the two states that matter: an absent collection returns inner `success:true` with an empty array — the honest answer on a fresh install — whereas an unreachable backend returns inner `success:false`. The signal was always there; we never read it.

### That single struct produces both reported symptoms

**"Empty forever, but green."** `TriggerScan` against a not-ready backend returns 200 with inner `success:false`, so `sendWithRetry` treats it as success on the first attempt — no retry, no error. The controller logs `✅ Targeted scan triggered successfully` (`capabilityscan_controller.go:270`) and calls `updateLastScanTime`, which sets `LastScanTime` **and clears `LastError`** (`capabilityscan_controller.go:640-642`). Ready, no error, recent scan, empty collection.

**Re-scan storm.** `ListCapabilityIDs` always returns empty, so `computeCapabilityDiff` puts every cluster resource type into `toScan` on every controller start regardless of how full the collection already is — hundreds of AI inference calls per cycle.

### Gap 1: the diff only ever runs once per process

`capabilityscan_controller.go:113-116`, the unchanged-config path:

```go
} else {
    // Config unchanged, nothing to do
    return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}
```

`performStartupReconciliation` runs only when a config is newly added to `activeConfigs` (line 165). So the controller wakes every 60 seconds and deliberately does nothing. A trigger that failed because the backend was not ready is never retried; recovery requires a controller restart or a spec change.

The reporter is right that this is inconsistent with the sibling controller: `ResourceSyncConfig` has `resyncIntervalMinutes` defaulting to 60 (`api/v1alpha1/resourcesyncconfig_types.go:27-33`) and a `periodicResyncLoop` goroutine (`internal/controller/resourcesync_controller.go:489-490`, `1396`). Nothing suggests startup-only was deliberate here — PRD #34's own commit note, "Remove InitialScanComplete status check (always reconcile on startup)," shows the intent was to re-derive from observed state, which a resync completes rather than contradicts. The `InitialScanComplete` field survives as dead weight (`api/v1alpha1/capabilityscanconfig_types.go:87`), never set and never read.

### Gap 2: a triggered scan has no completion signal

The server's scan is fire-and-forget by design: `handleFireAndForgetScan` saves a session and starts `handleScanningCore` **without awaiting** (`dot-ai` `src/tools/organizational-data.ts:186-255`). A 200 means "accepted," nothing more.

So even with (c) fixed, a successful trigger says nothing about whether the scan finished. A pod restart mid-scan, an AI provider outage, or per-resource inference failures all leave a partially-populated collection under a green CR. The server exposes a `progress` operation for exactly this and the controller never calls it.

**This is why the bug is flaky rather than deterministic**, and it is the part the reporter's three fixes do not address.

### Why CI never caught any of this

The e2e mock MCP server hand-writes the response as `data.result.capabilities` with `id` values `"Pod"`, `"Service"`, `"FakeStaleCRD.old.example.com"` (`test/e2e/mock-capability-scan-server.yaml:20-23`), and the unit fixtures do the same with `"Deployment.apps"` (`internal/controller/capabilityscan_mcp_test.go:167-184`). Both encode the same two wrong assumptions as the production code, so **the tests assert the bug**. Nothing exercises this controller against a real MCP server.

The assumptions are not careless — they come straight from PRD #34's API reference, which documented `data.result.capabilities` and elided the array contents as `"capabilities": [...]`, hiding the `id`/`resourceName` distinction. The contract was written by hand from the server's prose rather than captured from a real response.

### Latent hazard: fixing the parse arms a deletion path that has never run

Because `mcpSet` is always empty, `toDelete` is always empty (`capabilityscan_controller.go:317-322`) — the orphaned-capability deletion has **never executed in production**. It starts executing the moment matching works.

That path is unguarded. `listAllResourceIDs` deliberately tolerates partial discovery failure (`capabilityscan_controller.go:415-422`):

```go
if !discovery.IsGroupDiscoveryFailedError(err) {
    return nil, fmt.Errorf("failed to discover API resources: %w", err)
}
logger.V(1).Info("Partial discovery failure (some API groups unavailable)", ...)
```

then proceeds with a partial list. An unavailable aggregated APIService — a metrics adapter mid-restart, a webhook backend briefly down, entirely routine — omits every kind in that group from `clusterSet`, and every stored capability for those kinds is deleted, then re-scanned on the next cycle. Deleting and re-inferring capabilities is expensive and flapping, and it is a genuine regression risk introduced *by the fix*.

The same reasoning applies to the MCP side of the diff: any list the controller cannot prove is complete must not drive deletions. Since the server still caps `list` at 100 until dot-ai#714 ships, this is not hypothetical.

## Solution Overview

1. **Correct the response contract** — parse the real nesting, match on `resourceName`, and check `data.result.success` rather than the envelope.
2. **Guard the diff** so it never acts on a resource list it cannot prove complete, on either side.
3. **Make status truthful** — stop clearing `lastError` on the success path; distinguish "trigger accepted" from "scan succeeded."
4. **Add `resyncIntervalMinutes`** with a periodic resync mirroring `ResourceSyncConfig`, making the initial scan self-healing.
5. **Close the loop on fire-and-forget** by polling the server's `progress` operation.
6. **Replace hand-written fixtures** with the contract artifact from dot-ai#714, so a server-side shape change breaks this build instead of production.

**Key design principle, unchanged from PRD #34:** the controller is a coordinator, not a scanner. Everything here is about seeing the server's state accurately and reacting to it honestly.

## Sequencing

Deliberately different from the order proposed in the discussion.

1. **Guards ship with the matching fix, in the same change.** Not sequentially — the deletion path is inert only while matching is broken, so a commit that fixes matching without the guard is a regression window.
2. **Periodic resync ships after the diff is correct.** The discussion proposes resync first. With the diff still broken, a 60-minute resync means a full-cluster AI re-scan every hour, forever. Resync amplifies whatever the diff concludes, so the diff must be right first.
3. **dot-ai#714 is not a prerequisite.** With the truncation guard, this controller degrades safely against a server that still caps at 100: it declines to compute a destructive diff and says so, rather than storming or deleting. Once #714's raised cap is deployed, the diff converges to zero. That guard is precisely what decouples the two repos.
4. **`progress` polling last**, because it depends on everything above being trustworthy and is the only piece with an unresolved dependency (open question 3).

## Technical Design

### Response parsing

Add the missing nesting level, and read `resourceName`:

```go
Data *struct {
    Result *struct {
        Success bool `json:"success"`
        Data    *struct {
            Capabilities  []CapabilityInfo `json:"capabilities,omitempty"`
            TotalCount    int              `json:"totalCount,omitempty"`
            ReturnedCount int              `json:"returnedCount,omitempty"`
        } `json:"data,omitempty"`
        ...
    } `json:"result,omitempty"`
} `json:"data,omitempty"`
```

`CapabilityInfo` gains `ResourceName string \`json:"resourceName"\``. `GetCapabilityIDs()` returns `resourceName` values for diffing; `id` is retained solely because `DeleteCapability` addresses by it (`capabilityscan_mcp.go:253-259`), so the two must be kept associated rather than one replaced by the other.

`sendWithRetry` (`capabilityscan_mcp.go:274-321`) treats inner `success == false` as a retryable failure, so a not-ready backend now consumes the existing backoff (`maxAttempts`, `backoffSeconds`, `maxBackoffSeconds` — already configurable per `CapabilityScanConfig`) instead of being mistaken for success on the first attempt.

### Completeness guard

`listAllResourceIDs` returns whether discovery was complete, instead of silently degrading. The diff then has three outcomes rather than two:

- **Both sides complete** → scan and delete as today.
- **Either side incomplete** → scan the missing (additive, safe) but **skip deletions entirely**, and record why on the CR status.
- **Cluster discovery hard-failed** → no action, surface the error.

On the MCP side, completeness means `returnedCount == totalCount`. `totalCount` is not capped by the server's limit (`dot-ai` `src/core/capability-operations.ts:136-138`), which is what makes the check possible against an unpatched server.

Scanning-while-incomplete is safe because scans are idempotent — a capability that already exists is simply re-inferred. Deleting while incomplete is not.

### Status truthfulness

`updateLastScanTime` (`capabilityscan_controller.go:630-649`) stops clearing `LastError`; clearing becomes explicit and only on a genuinely clean reconcile. `LastScanTime` must mean "a scan completed," not "a trigger was accepted" — which requires the `progress` poll, so until M5 it should mean "diff completed and trigger accepted" and be named or documented accordingly rather than silently overstating.

### Periodic resync

`ResyncIntervalMinutes int` on `CapabilityScanConfigSpec` with a `GetResyncInterval()` helper defaulting to 60, mirroring `resourcesyncconfig_types.go:27-33` and `:119-125`. Added to `configChanged` (`capabilityscan_controller.go:176-197`) so an interval change re-activates the config.

Two viable shapes: a `periodicResyncLoop` goroutine matching `resourcesync_controller.go:1396`, or driving off the reconciler's existing 60s requeue by comparing `LastScanTime` against the interval. The goroutine matches the sibling controller exactly and is the safer choice for consistency; the requeue approach needs no new goroutine lifecycle and survives leader-election changes more cleanly. Decide at M4 — the goroutine is the default unless the requeue approach proves simpler in review.

Either way, resync must not overlap with an in-flight scan.

## Milestones

### Milestone 1: Correct contract, with guards
- [ ] Response struct fixed: nesting, `resourceName`, inner `success` checked in `sendWithRetry`
- [ ] `listAllResourceIDs` reports discovery completeness
- [ ] Diff skips deletions when either side is incomplete, and records why on status
- [ ] MCP-side completeness derived from `returnedCount` vs `totalCount`
- [ ] Verified against a real MCP server, not a mock — this is the acceptance bar for the milestone

### Milestone 2: Truthful status
- [ ] `updateLastScanTime` no longer clears `LastError`
- [ ] Trigger-accepted and scan-succeeded are distinguishable on the CR
- [ ] Events emitted for scan-trigger failure, matching the existing `ConfigActivated` pattern (`capabilityscan_controller.go:168`)
- [ ] Dead code removed: `ListCapabilities` (test-only, `capabilityscan_mcp.go:167`) and `InitialScanComplete` (`capabilityscanconfig_types.go:87`); `make generate manifests` re-run

### Milestone 3: Tests that would have caught this
- [ ] Unit fixtures replaced with captured real-server responses via dot-ai#714's contract artifact
- [ ] E2E mock server response shape corrected (`test/e2e/mock-capability-scan-server.yaml`)
- [ ] Coverage for: inner `success:false` surfaces as an error; truncated list suppresses deletions; partial discovery suppresses deletions
- [ ] A test that fails if the server's shape changes

### Milestone 4: Periodic resync
- [ ] `resyncIntervalMinutes` on the CRD, default 60, wired into `configChanged`; `make generate manifests`
- [ ] Resync re-runs the diff; no overlap with in-flight scans
- [ ] A scan that failed because the backend was not ready recovers without a restart or CR change — the headline fix, verified end to end
- [ ] Helm chart CRD template regenerated

### Milestone 5: Completion feedback
- [ ] `progress` polled after a trigger; partial and failed scans surfaced on status
- [ ] `LastScanTime` means a scan completed
- [ ] Behavior defined for "session not found" after an MCP restart (open question 3)

### Milestone 6: Docs and release
- [ ] `docs/capability-scan-guide.md` updated: resync interval, status semantics, guard behavior
- [ ] PRD #34's API reference marked superseded, pointing here — it is still the most discoverable description of this API and will mislead the next reader otherwise
- [ ] Changelog fragment in `changelog.d/`
- [ ] Reply on dot-ai#709 with the specific fixes, so the reporter can retire the corresponding patches in their fork

## Success Metrics

- On a fresh install where the vector backend is not yet ready, the collection populates without operator intervention — no controller restart, no CR recreation.
- A scan trigger that fails is visible on the CR as an error and is retried. `Ready` with no `lastError` and an empty collection becomes unreachable.
- With the collection fully populated, a resync computes an empty diff and triggers no scans. Steady state costs zero AI inference.
- Neither a truncated MCP list nor a partial cluster discovery can delete a stored capability.
- Against a server that still caps `list` at 100, the controller degrades safely and says why — it does not storm and does not delete.
- Changing the server's response shape fails this repo's tests.
- `make test` and e2e green.

## Risks & Mitigation

### Risk 1: The fix arms untested deletion logic
Orphan deletion has never run in production. Its first real execution will be against live data, driven by a Discovery API that is allowed to return partial results.
**Mitigation**: the completeness guard is non-negotiable and ships in M1 with the matching fix. M3 covers both incompleteness paths explicitly.

### Risk 2: Resync turns a bounded cost into a recurring one
A correct diff makes resync nearly free; an incorrect one makes it a full-cluster AI re-scan every hour.
**Mitigation**: sequencing — M4 lands after M1. Consider logging the diff size on every resync so an unexpected non-zero steady state is visible rather than silent.

### Risk 3: `progress` sessions are not durable
The server keeps scan sessions on the pod filesystem (`dot-ai` `src/core/capability-operations.ts:512-545`), so progress does not survive an MCP restart, and the chart pins `replicas: 1` (`dot-ai` `charts/templates/deployment.yaml:13`) — polling would break silently if that ever changes.
**Mitigation**: treat "session not found" as "unknown, re-diff on next resync," which is correct and self-healing. Tracked as dot-ai#714 open question 3.

### Risk 4: Version skew
Users run mixed controller and MCP versions.
**Mitigation**: the completeness guard makes the old-server case safe by construction. Worth confirming whether the new struct should tolerate the *old* shape for forward compatibility, or fail loudly — leaning loudly, since silent tolerance is what produced this bug.

## Dependencies

- **Not blocking**: [vfarcic/dot-ai#714](https://github.com/vfarcic/dot-ai/issues/714) — raised `list` cap, identity projection, published contract, `/readyz`. M3's fixtures want the contract artifact; the rest proceeds without it.
- **Existing**: `CapabilityScanConfig` retry settings (`capabilityscanconfig_types.go:65-80`) are reused for the now-correctly-detected failures. No new retry configuration.

## Open Questions

1. **Should the resync be a goroutine or driven off the existing 60s requeue?** The goroutine matches `ResourceSyncConfig` exactly, which is worth something for a codebase with two near-identical controllers. The requeue approach adds no goroutine lifecycle to manage and behaves better under leader-election changes, and the reconciler is already waking every 60s doing nothing. Decide at M4.

2. **Should incomplete-but-actionable be `Ready: false`?** Skipping deletions because discovery was partial is a degraded but functioning state — scans still happen. Marking it not-Ready is honest but noisy on clusters with a chronically flaky aggregated APIService. A distinct condition type (`DiffComplete`) alongside `Ready` may fit better. Whatever is chosen must not reproduce the original sin of a status that looks healthier than reality.

3. **How long should the controller follow a scan it triggered?** A full-cluster scan on a large cluster takes a long time, and the controller would be polling `progress` throughout. Polling until terminal risks a long-lived goroutine per scan; polling once per resync is cheaper but reports completion late. Leaning toward checking at resync boundaries only — the resync loop already provides the cadence, and a late-but-correct answer is worth more than a prompt one.

4. **Should the e2e mock be kept at all?** It is what validated the bug for the life of the defect. Correcting its shape (M3) fixes this instance without fixing the class — the next shape change diverges again. Options: generate it from dot-ai#714's contract artifact at test time, or drop it in favor of an e2e run against a real MCP server. The first is cheap and closes the loop; the second is more faithful and much heavier. Leaning generated-from-artifact.
