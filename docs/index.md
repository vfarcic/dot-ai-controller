# DevOps AI Toolkit Controller

A Kubernetes controller that provides resource tracking, event-driven remediation, and resource visibility capabilities for your cluster.

## Mission

The DevOps AI Toolkit Controller bridges the gap between Kubernetes resources and intelligent operations. It enables:

- **Resource awareness** through logical grouping and health aggregation
- **Proactive remediation** through AI-powered event analysis
- **Resource discoverability** through semantic search integration

## Who Should Use This

- **Platform Engineers** building self-service Kubernetes platforms
- **DevOps Teams** looking to automate incident response
- **SREs** who want intelligent monitoring and remediation
- **Developers** deploying applications and needing resource visibility

## Scope

### In Scope

- Kubernetes resource tracking and lifecycle management
- Event-driven remediation with AI analysis
- Resource synchronization for semantic search
- Integration with DevOps AI Toolkit MCP

### Out of Scope

- Direct AI/LLM processing (delegated to MCP)
- Application-level monitoring
- Multi-cluster management
- GitOps workflows

## Features

The DevOps AI Toolkit Controller provides four main capabilities:

### 1. Solution CRD - Resource Tracking

Track and manage deployed Kubernetes resources as logical solutions:

- **Resource Grouping**: Links all resources (Deployments, Services, etc.) that compose a logical solution
- **Intent Preservation**: Stores the original user intent and deployment context
- **Metadata Storage**: Captures deployment rationale, patterns, policies, and documentation links
- **Health Monitoring**: Aggregates health status across all tracked resources
- **Automatic Cleanup**: Deleting a Solution CR automatically deletes all child resources via ownerReferences

**Works standalone** - No external dependencies required.

### 2. RemediationPolicy CRD - Event-Driven Remediation

Monitor Kubernetes events and automatically remediate issues using the [DevOps AI Toolkit](https://devopstoolkit.ai/docs/mcp):

- **Event Watching**: Configurable filtering of Kubernetes events by type, reason, and involved objects
- **Automatic Mode**: System detects, analyzes, and fixes issues without human intervention
- **Manual Mode**: System provides remediation recommendations via Slack for human execution
- **Slack Notifications**: Rich notifications with remediation results and next steps
- **Rate Limiting**: Prevents event storms with configurable cooldowns
- **Status Reporting**: Comprehensive observability through status updates

**Requires** [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) for AI-powered analysis.

### 3. ResourceSyncConfig CRD - Resource Visibility

Enable semantic search and resource discovery across your cluster:

- **Resource Discovery**: Automatically discovers all resource types in your cluster
- **Change Tracking**: Watches for resource changes (create, update, delete)
- **Semantic Search**: Syncs resource metadata to MCP for natural language queries
- **Debounced Sync**: Batches changes to reduce API calls
- **Periodic Resync**: Full state sync catches any missed events

**Requires** [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) for semantic search capabilities.

### 4. CapabilityScanConfig CRD - Autonomous Capability Discovery

Keep your cluster's capability data up-to-date for AI-powered recommendations:

- **Autonomous Discovery**: Automatically detects CRD changes (create, update, delete)
- **Event-Driven Scanning**: Triggers capability scans when new CRDs are installed
- **Startup Reconciliation**: Syncs cluster state with MCP on controller restart
- **Resource Filtering**: Include/exclude patterns for targeted scanning
- **Debounced Batching**: Groups rapid CRD changes into efficient batch requests

**Requires** [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp) for capability storage and analysis.

## Quick Start

### 1. Install Controller

```bash
# Set the version from https://github.com/vfarcic/dot-ai-controller/pkgs/container/dot-ai-controller%2Fcharts%2Fdot-ai-controller
export DOT_AI_CONTROLLER_VERSION="..."

helm install dot-ai-controller oci://ghcr.io/vfarcic/dot-ai-controller/charts/dot-ai-controller \
  --version $DOT_AI_CONTROLLER_VERSION \
  --namespace dot-ai \
  --create-namespace \
  --wait
```

This installs all four CRDs (Solution, RemediationPolicy, ResourceSyncConfig, and CapabilityScanConfig) and the controller.

### 2. Choose Your Feature

**For Event Remediation:**

First, install the [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp), then:

```bash
# Create a RemediationPolicy to handle events
kubectl apply --filename - <<'EOF'
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: auto-remediate
  namespace: dot-ai
spec:
  eventSelectors:
    - type: Warning
      reason: FailedScheduling
      mode: automatic
  mcpEndpoint: http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
EOF
```

See the [Remediation Guide](remediation-guide.md) for complete examples, configuration options, and best practices.

**For Resource Visibility:**

First, install the [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp), then:

```bash
# Create a ResourceSyncConfig to enable semantic search
kubectl apply --filename - <<'EOF'
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: default-sync
spec:
  mcpEndpoint: http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/resources/sync
  debounceWindowSeconds: 10
  resyncIntervalMinutes: 60
EOF
```

See the [Resource Sync Guide](resource-sync-guide.md) for complete examples and semantic search usage.

**For Capability Discovery:**

First, install the [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp), then:

```bash
# Create a CapabilityScanConfig to enable autonomous scanning
kubectl apply --filename - <<'EOF'
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: default-scan
  namespace: dot-ai
spec:
  mcp:
    endpoint: http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/manageOrgData
    authSecretRef:
      name: dot-ai-secrets
      key: auth-token
EOF
```

See the [Capability Scan Guide](capability-scan-guide.md) for complete examples and configuration options.

**For Resource Tracking:**
```bash
# Create a Solution CR to track your deployed resources
kubectl apply --filename - <<'EOF'
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: my-app
  namespace: default
spec:
  intent: "Production web application with database"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: web-app
    - apiVersion: v1
      kind: Service
      name: web-app-service
    - apiVersion: apps/v1
      kind: StatefulSet
      name: postgresql
EOF
```

See the [Solution Guide](solution-guide.md) for complete examples and usage patterns.

## Documentation

- **[Setup Guide](setup-guide.md)** - Installation and prerequisites
- **[Remediation Guide](remediation-guide.md)** - Event-driven remediation
- **[Resource Sync Guide](resource-sync-guide.md)** - Resource visibility and semantic search
- **[Capability Scan Guide](capability-scan-guide.md)** - Autonomous capability discovery
- **[Solution Guide](solution-guide.md)** - Resource tracking and lifecycle management
- **[Troubleshooting Guide](troubleshooting.md)** - Common issues and solutions

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                 │
│                                                     │
│  ┌──────────────────────┐                          │
│  │  Solution CR         │  (Parent Resource)       │
│  │  ─────────────       │                          │
│  │  metadata:           │                          │
│  │    intent: "..."     │                          │
│  │    resources: [...]  │                          │
│  └──────────────────────┘                          │
│           ▲                                         │
│           │ ownerReferences                         │
│           │                                         │
│  ┌────────┴──────────┬──────────────┬─────────┐   │
│  │                   │              │         │   │
│  ▼                   ▼              ▼         ▼   │
│  Deployment      Service         PVC      ConfigMap│
│  (child)         (child)       (child)   (child)  │
│                                                     │
│  ┌─────────────────────────────────────────────┐   │
│  │  Controller                                 │   │
│  │  ───────────                                │   │
│  │  • Watches Solution CRs                     │   │
│  │  • Manages ownerReferences                  │   │
│  │  • Tracks resource health                   │   │
│  │  • Processes events (RemediationPolicy)     │   │
│  │  • Syncs resources to MCP (ResourceSync)    │   │
│  │  • Scans capabilities (CapabilityScan)      │   │
│  └─────────────────────────────────────────────┘   │
│                                                     │
└─────────────────────────────────────────────────────┘
```
