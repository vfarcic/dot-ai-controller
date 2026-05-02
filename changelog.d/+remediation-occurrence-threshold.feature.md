## RemediationPolicy Occurrence Threshold

RemediationPolicy now supports an occurrence-threshold filter that suppresses transient single-occurrence failures so remediation only triggers when an event recurs. This addresses a long-standing gap where the *first* matching event always triggered an MCP call, paying the full analysis cost (and emitting Slack/Google Chat notifications) for self-healing blips like probe failures during VPA in-place resizes.

Two new optional fields are available on each `eventSelector` entry, with matching global defaults at the policy level: `minOccurrences` (count required before remediating) and `occurrenceWindowSeconds` (sliding window in which the count must accumulate, default 300). Selector values override the policy defaults — including an explicit `minOccurrences: 0` to disable filtering for that selector even when a global default is set — mirroring how `confidenceThreshold` and `maxRiskLevel` already work. Counters are tracked per `(policy, involvedObject, reason, message)` so a transient `503` blip on a probe doesn't mask a genuine `500` failure of the same probe — distinct messages are counted independently. Pods owned by Jobs/CronJobs share counters via the existing owner-resolution scheme.

Sub-threshold events are silently counted and exit early — no object cooldown is started, no rate-limit slot is consumed, no MCP call, no notification. After remediation triggers, the counter for that key resets, requiring the threshold to be rebuilt within a fresh window before another remediation fires.

Example — tolerate transient probe blips during VPA resizes while preserving visibility into real probe failures:

```yaml
eventSelectors:
  - type: Warning
    reason: Unhealthy
    involvedObjectKind: Pod
    minOccurrences: 2
    occurrenceWindowSeconds: 300
```

Existing policies are unaffected: both fields default to zero, which means "remediate on first match" — identical to today's behavior.
