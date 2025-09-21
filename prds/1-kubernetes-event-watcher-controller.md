# PRD: Kubernetes Event Watcher Controller

**Issue**: #1  
**Created**: 2025-01-10  
**Status**: Planning  
**Priority**: High  
**Owner**: TBD  
**Related**: [dot-ai#97](https://github.com/vfarcic/dot-ai/issues/97) - MCP Remediate Tool

## Executive Summary

Build a Kubernetes controller that watches cluster events and forwards them to the dot-ai MCP remediate tool for AI-powered analysis and remediation. This controller acts as the bridge between Kubernetes cluster events and the intelligent remediation engine.

## Problem Statement

### Current Challenges
- No automated connection between Kubernetes events and remediation systems
- Manual event monitoring leads to delayed incident response
- Lack of context enrichment for events before analysis
- No standardized way to trigger AI-based remediation from cluster events

### User Impact
- **Platform Teams**: Need automated event response to reduce operational overhead
- **DevOps Engineers**: Want faster incident detection and remediation
- **SRE Teams**: Require consistent event handling across clusters

## Success Criteria

- Successfully capture and forward 100% of configured event types
- Enrich events with relevant context (logs, metrics, related events)
- Maintain < 5 second latency from event to MCP call
- Support both manual and automatic remediation workflows
- Zero event loss during controller restarts

## Scope

### In Scope
- Kubernetes controller using Kubebuilder framework
- CRD for RemediationPolicy configuration
- Event watching and filtering logic
- Context enrichment (logs, metrics, events)
- HTTP client for MCP communication
- Status tracking and reporting

### Out of Scope
- AI analysis logic (handled by MCP)
- Remediation execution (handled by MCP)
- Multi-cluster orchestration
- Notification mechanisms (deferred)

## Requirements

### Functional Requirements

1. **Event Watching**
   - Watch Kubernetes events based on configurable selectors
   - Support filtering by type, reason, involved object
   - Handle event deduplication

2. **Context Enrichment**
   - Gather pod logs (last N lines)
   - Collect related events
   - Fetch resource specifications
   - Optional metrics collection

3. **MCP Integration**
   - Format events into MCP remediate tool format
   - Handle HTTP communication with MCP server
   - Process and record MCP responses

4. **Policy Management**
   - CRD-based configuration
   - Support multiple policies per cluster
   - Enable/disable policies dynamically

### Non-Functional Requirements

- **Performance**: Process 100+ events/minute
- **Reliability**: Graceful handling of MCP unavailability
- **Scalability**: Support watching all namespaces
- **Security**: RBAC for accessing resources

## Technical Design

### CRD Schema

```yaml
apiVersion: remediation.dot-ai.io/v1alpha1
kind: RemediationPolicy
metadata:
  name: pod-failures
  namespace: default
spec:
  # Event selection
  eventSelectors:
    - type: Warning
      reason: CrashLoopBackOff
      involvedObjectKind: Pod
    - type: Warning
      reason: OOMKilled
      involvedObjectKind: Pod
  
  # Context gathering
  contextGathering:
    includeLogs: true
    logLines: 100
    includeMetrics: false
    includeRelatedEvents: true
    lookbackMinutes: 5
  
  # MCP configuration
  mcpEndpoint: http://dot-ai.127.0.0.1.nip.io/api/v1/tools/remediate
  mcpTool: remediate
  
  # Remediation mode
  mode: manual  # or "automatic"
  
  # Rate limiting
  rateLimiting:
    eventsPerMinute: 10
    cooldownMinutes: 5
  
status:
  lastProcessedEvent: "2025-01-10T15:00:00Z"
  totalEventsProcessed: 42
  successfulRemediations: 38
  failedRemediations: 4
```

### Controller → MCP Interface

```json
{
  "issue": "Pod nginx-xyz in namespace default is crash looping",
  "context": {
    "event": {
      "type": "Warning",
      "reason": "CrashLoopBackOff",
      "message": "Back-off restarting failed container",
      "involvedObject": {
        "kind": "Pod",
        "name": "nginx-xyz",
        "namespace": "default"
      }
    },
    "logs": ["Error: connection refused", "..."],
    "podSpec": { /* full pod specification */ },
    "relatedEvents": [ /* array of related events */ ],
    "metrics": { /* optional metrics data */ }
  },
  "mode": "manual",
  "policy": "pod-failures"
}
```

### Architecture

```
K8s Events → Controller → Context Enrichment → MCP Client → dot-ai MCP
                ↓                                              ↓
            CRD Status                                   Remediation
```

### Key Components

1. **Event Watcher** (`controllers/event_controller.go`)
   - Informer-based event watching
   - Filtering and deduplication
   - Queue management

2. **Context Enricher** (`pkg/enricher/enricher.go`)
   - Log collection
   - Related event gathering
   - Resource spec fetching

3. **MCP Client** (`pkg/mcp/client.go`)
   - HTTP communication
   - Request/response handling
   - Error recovery

4. **Policy Controller** (`controllers/remediationpolicy_controller.go`)
   - CRD reconciliation
   - Policy validation
   - Status updates

## Implementation Milestones

### Milestone 1: Project Setup & CRD ⬜
**Deliverable**: Kubebuilder project with RemediationPolicy CRD
- [ ] Initialize Kubebuilder project
- [ ] Define RemediationPolicy CRD
- [ ] Generate controller scaffolding
- [ ] Set up CI/CD pipeline

### Milestone 2: Event Watching ⬜
**Deliverable**: Controller successfully watching and filtering events
- [ ] Implement event informer
- [ ] Add filtering logic based on policy
- [ ] Implement deduplication
- [ ] Unit tests for event processing

### Milestone 3: Context Enrichment ⬜
**Deliverable**: Events enriched with logs and related information
- [ ] Implement log collection
- [ ] Add related event gathering
- [ ] Fetch pod/deployment specs
- [ ] Performance optimization for large logs

### Milestone 4: MCP Integration ⬜
**Deliverable**: Successfully calling MCP remediate tool
- [ ] Implement MCP HTTP client
- [ ] Format requests according to interface
- [ ] Handle responses and errors
- [ ] Integration tests with mock MCP

### Milestone 5: Production Deployment ⬜
**Deliverable**: Controller running in production cluster
- [ ] Helm chart creation
- [ ] RBAC configuration
- [ ] Monitoring and metrics
- [ ] Documentation and runbooks

## Risks & Mitigations

| Risk | Impact | Probability | Mitigation |
|------|--------|------------|------------|
| Event storms overwhelming system | High | Medium | Rate limiting, backpressure handling |
| MCP unavailability blocking events | Medium | Low | Queue events, exponential backoff |
| Memory issues with log collection | Medium | Medium | Streaming logs, size limits |
| RBAC permissions insufficient | Low | Medium | Comprehensive RBAC template |

## Dependencies

- dot-ai MCP with remediate tool (issue #97)
  - Server accessible at http://dot-ai.127.0.0.1.nip.io
  - REST API endpoint: `/api/v1/tools/remediate` (POST)
  - API documentation: https://github.com/vfarcic/dot-ai/blob/main/docs/rest-api-gateway-guide.md
- Kubernetes cluster with appropriate RBAC
- Go 1.21+ and Kubebuilder 3.x
- Access to cluster metrics (optional)

## Future Enhancements

1. **Multi-cluster Support**: Single controller managing multiple clusters
2. **Custom Event Sources**: Watch CRD events, not just core events
3. **Event Correlation**: Group related events before sending
4. **Prometheus Integration**: Use metrics in remediation decisions
5. **Webhook Support**: Alternative to polling for events

## Open Questions

1. **Event Persistence**: Should we store events locally for replay?
2. **Batch Processing**: Send multiple events in one MCP call?
3. **Namespace Isolation**: Per-namespace policies vs cluster-wide?
4. **Metrics Collection**: Which metrics provider to support first?

## Progress Log

### 2025-01-10
- Initial PRD created
- Architecture aligned with MCP remediate tool design
- Interface contract defined with MCP team
- Repository created at vfarcic/dot-ai-controller

---

*This PRD is a living document and will be updated as the implementation progresses.*