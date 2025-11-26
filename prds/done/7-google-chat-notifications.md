# PRD: Google Chat Webhook Notifications

**Issue**: [#7](https://github.com/vfarcic/dot-ai-controller/issues/7)
**Status**: Complete
**Priority**: Medium
**Created**: 2025-11-26
**Requested By**: @giz33 ([#6](https://github.com/vfarcic/dot-ai-controller/issues/6))

---

## Problem Statement

Users want to receive remediation notifications via Google Chat, but currently only Slack is supported. This limits adoption for organizations that use Google Workspace as their primary collaboration platform.

Google Chat supports incoming webhooks for Google Workspace paid accounts, enabling automated notifications to be sent to chat spaces.

## Solution Overview

Add Google Chat webhook notification support alongside existing Slack notifications, following the same configuration patterns and providing equivalent notification content. The implementation will:

1. Add `GoogleChatConfig` to the `NotificationConfig` struct in the CRD
2. Implement Google Chat message formatting using Google Chat's card-based API
3. Send notifications at the same lifecycle points as Slack (start/complete)
4. Validate webhook URLs and handle errors gracefully

## User Experience

### Configuration Example

Users will configure Google Chat notifications in their `RemediationPolicy` resource:

```yaml
apiVersion: dot.ai/v1alpha1
kind: RemediationPolicy
metadata:
  name: my-policy
spec:
  notifications:
    slack:
      enabled: true
      webhookUrl: "https://hooks.slack.com/services/..."
    googleChat:
      enabled: true
      webhookUrl: "https://chat.googleapis.com/v1/spaces/..."
      notifyOnStart: false
      notifyOnComplete: true
```

### Notification Content

Google Chat notifications will contain the same information as Slack notifications:

**Start Notification:**
- Event type, resource, namespace
- Execution mode (automatic/manual)
- Issue description

**Complete Notification:**
- Success/failure status
- MCP result message
- Execution details (time, confidence, root cause)
- Commands executed or recommended
- Validation status

## Technical Approach

### CRD Changes

Add `GoogleChatConfig` struct to `api/v1alpha1/remediationpolicy_types.go`:

```go
type GoogleChatConfig struct {
    Enabled          bool   `json:"enabled,omitempty"`
    WebhookUrl       string `json:"webhookUrl,omitempty"`
    NotifyOnStart    bool   `json:"notifyOnStart,omitempty"`
    NotifyOnComplete bool   `json:"notifyOnComplete,omitempty"`
}

type NotificationConfig struct {
    Slack      SlackConfig      `json:"slack,omitempty"`
    GoogleChat GoogleChatConfig `json:"googleChat,omitempty"`
}
```

### Google Chat Message Format

Google Chat uses a card-based message format. The implementation will use Google Chat's Card v2 API:

```json
{
  "cardsV2": [{
    "cardId": "remediation-notification",
    "card": {
      "header": {
        "title": "Remediation Started",
        "subtitle": "dot-ai Kubernetes Event Controller",
        "imageUrl": "...",
        "imageType": "CIRCLE"
      },
      "sections": [...]
    }
  }]
}
```

### Implementation Files

| File | Changes |
|------|---------|
| `api/v1alpha1/remediationpolicy_types.go` | Add `GoogleChatConfig` struct |
| `internal/controller/remediationpolicy_controller.go` | Add validation, sending, and formatting functions |
| `config/crd/bases/*.yaml` | Auto-generated CRD updates |
| `config/samples/remediationpolicy_comprehensive.yaml` | Add Google Chat example |
| `docs/remediation-guide.md` | Document Google Chat configuration |

## Success Criteria

1. Users can configure Google Chat webhook notifications in RemediationPolicy
2. Notifications are sent at start and/or completion based on configuration
3. Message content is equivalent to Slack notifications
4. Webhook URL validation rejects invalid URLs
5. Notification failures don't block remediation processing
6. Documentation is complete and includes examples
7. All existing tests pass, new tests cover Google Chat functionality

## Milestones

- [x] **M1: CRD and API Updates** - Add GoogleChatConfig to types, generate manifests, update Helm chart CRDs
- [x] **M2: Core Implementation** - Implement validation, message formatting, and webhook sending for Google Chat
- [x] **M3: Integration and Testing** - Add unit tests, integration tests, and verify end-to-end functionality
- [x] **M4: Documentation** - Update remediation guide, add sample configurations, update README if needed
- [x] **M5: Release Ready** - All tests passing, code reviewed, ready for merge

## Out of Scope

- Google Chat bot/app integration (only webhooks)
- Interactive message actions (buttons, forms)
- Thread-based conversations
- OAuth-based authentication

## Dependencies

- Google Workspace paid account (webhooks require paid tier)
- Existing Slack notification implementation as reference

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Google Chat API changes | Use stable Card v2 API, add version comments |
| Webhook URL format changes | Validate URL pattern, document requirements |
| Rate limiting | Non-blocking sends, errors logged but don't fail remediation |

## Progress Log

| Date | Update |
|------|--------|
| 2025-11-26 | PRD created based on issue #6 request |
| 2025-11-26 | M1-M4 completed: API types, controller implementation, tests, documentation |
| 2025-11-26 | M5 completed: Release ready, PR created for merge |

---

## References

- [Google Chat Webhooks Documentation](https://developers.google.com/chat/how-tos/webhooks)
- [Google Chat Card v2 API](https://developers.google.com/chat/api/reference/rest/v1/cards)
- [Original Feature Request - Issue #6](https://github.com/vfarcic/dot-ai-controller/issues/6)
