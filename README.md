# DevOps AI Toolkit Controller

A Kubernetes controller that provides resource tracking and event-driven remediation capabilities.

## Features

The DevOps AI Toolkit Controller provides two main capabilities:

### 1. Solution CRD - Resource Tracking

Track and manage deployed Kubernetes resources as logical solutions:

- **Resource Grouping**: Links all resources (Deployments, Services, etc.) that compose a logical solution
- **Intent Preservation**: Stores the original user intent and deployment context
- **Metadata Storage**: Captures deployment rationale, patterns, policies, and documentation links
- **Health Monitoring**: Aggregates health status across all tracked resources
- **Automatic Cleanup**: Deleting a Solution CR automatically deletes all child resources via ownerReferences

**Works standalone** - No external dependencies required.

👉 **[Solution Guide](docs/solution-guide.md)** - Complete guide to using the Solution CRD

### 2. RemediationPolicy CRD - Event-Driven Remediation

Monitor Kubernetes events and automatically remediate issues using the [DevOps AI Toolkit](https://github.com/vfarcic/dot-ai):

- **Event Watching**: Configurable filtering of Kubernetes events by type, reason, and involved objects
- **Automatic Mode**: System detects, analyzes, and fixes issues without human intervention
- **Manual Mode**: System provides remediation recommendations via Slack for human execution
- **Slack Notifications**: Rich notifications with remediation results and next steps
- **Rate Limiting**: Prevents event storms with configurable cooldowns
- **Status Reporting**: Comprehensive observability through status updates

**Requires** [DevOps AI Toolkit MCP](https://github.com/vfarcic/dot-ai) for AI-powered analysis.

👉 **[Remediation Guide](docs/remediation-guide.md)** - Complete guide to event remediation

## Quick Start

> **Note**: If you're using [DevOps AI Toolkit (dot-ai)](https://github.com/vfarcic/dot-ai), the controller is automatically installed into your cluster. Skip to step 2.

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

This installs both CRDs (Solution and RemediationPolicy) and the controller.

👉 **[Setup Guide](docs/setup-guide.md)** - Complete installation instructions

### 2. Choose Your Feature

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

See the [Solution Guide](docs/solution-guide.md) for complete examples and usage patterns.

**For Event Remediation:**

First, install the [DevOps AI Toolkit MCP](https://github.com/vfarcic/dot-ai), then:

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

See the [Remediation Guide](docs/remediation-guide.md) for complete examples, configuration options, and best practices.

## Documentation

- **[Setup Guide](docs/setup-guide.md)** - Installation and prerequisites
- **[Solution Guide](docs/solution-guide.md)** - Resource tracking and lifecycle management
- **[Remediation Guide](docs/remediation-guide.md)** - Event-driven remediation
- **[Troubleshooting Guide](docs/troubleshooting.md)** - Common issues and solutions

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
│  ┌──────────────────────────────────────┐          │
│  │  Controller                          │          │
│  │  ───────────                         │          │
│  │  • Watches Solution CRs              │          │
│  │  • Manages ownerReferences           │          │
│  │  • Tracks resource health            │          │
│  │  • Processes events (RemediationPolicy) │       │
│  └──────────────────────────────────────┘          │
│                                                     │
└─────────────────────────────────────────────────────┘
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License - see [LICENSE](LICENSE) file for details.
