# Setup Guide

This guide covers installation and initial setup of the DevOps AI Toolkit Controller.

> **Recommended**: For the easiest setup, we recommend installing the complete dot-ai stack which includes all components. See the [Stack Installation Guide](https://devopstoolkit.ai/docs/stack).
>
> The stack automatically applies CapabilityScanConfig and ResourceSyncConfig CRs. For RemediationPolicy and Solution CRs, see the [Remediation Guide](remediation-guide.md) and [Solution Guide](solution-guide.md) to configure them based on your needs.
>
> Continue below if you want to install this component individually (for non-Kubernetes setups or granular control).

## Prerequisites

- **kubectl** v1.11.3+
- **Helm** v3.0+
- **Kubernetes cluster** v1.11.3+

## Optional: Test Cluster Setup

If you don't have a Kubernetes cluster, create one locally using Kind:

```bash
# Use isolated kubeconfig
export KUBECONFIG=$PWD/kubeconfig.yaml

# Create test cluster
kind create cluster --name dot-ai-controller-test

# Verify cluster access
kubectl cluster-info
```

## Install Controller

The controller provides four features:
- **Solution CRD**: Resource tracking and lifecycle management (standalone)
- **RemediationPolicy CRD**: Event-driven remediation (requires [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp))
- **ResourceSyncConfig CRD**: Resource visibility and semantic search (requires [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp))
- **CapabilityScanConfig CRD**: Autonomous capability discovery (requires [DevOps AI Toolkit MCP](https://devopstoolkit.ai/docs/mcp))

### Install via Helm

```bash
# Set the version from https://github.com/vfarcic/dot-ai-controller/pkgs/container/dot-ai-controller%2Fcharts%2Fdot-ai-controller
export DOT_AI_CONTROLLER_VERSION="..."

helm install dot-ai-controller oci://ghcr.io/vfarcic/dot-ai-controller/charts/dot-ai-controller \
  --version $DOT_AI_CONTROLLER_VERSION \
  --namespace dot-ai \
  --create-namespace \
  --wait
```

This installs:
- Controller deployment
- Solution CRD
- RemediationPolicy CRD
- ResourceSyncConfig CRD
- CapabilityScanConfig CRD
- RBAC permissions

### Configuration Reference

| Parameter | Description | Default |
|-----------|-------------|---------|
| `annotations` | Global annotations applied to all resources (e.g., `reloader.stakater.com/auto: "true"`) | `{}` |
| `image.repository` | Container image repository | `ghcr.io/vfarcic/dot-ai-controller` |
| `image.tag` | Container image tag | Chart appVersion |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `resources.requests.memory` | Memory request | `128Mi` |
| `resources.requests.cpu` | CPU request | `10m` |
| `resources.limits.memory` | Memory limit | `512Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |

### Verify Installation

```bash
# Check controller is running
kubectl get pods --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai

# Check controller logs
kubectl logs --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai --tail 10

# Verify CRDs are installed
kubectl get crds | grep dot-ai.devopstoolkit.live
```

You should see all four CRDs:
```text
capabilityscanconfigs.dot-ai.devopstoolkit.live
remediationpolicies.dot-ai.devopstoolkit.live
resourcesyncconfigs.dot-ai.devopstoolkit.live
solutions.dot-ai.devopstoolkit.live
```

## Optional: Install DevOps AI Toolkit MCP

**Required for RemediationPolicy, ResourceSyncConfig, and CapabilityScanConfig features.** If you're only using the Solution CRD, skip this step.

For MCP installation instructions, see the [DevOps AI Toolkit documentation](https://devopstoolkit.ai/docs/mcp).

The controller expects the MCP service at:
- RemediationPolicy: `http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/remediate`
- ResourceSyncConfig: `http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/resources/sync`
- CapabilityScanConfig: `http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/manageOrgData`

## What's Next

Choose which features you want to use:

- **Solution CRD**: [Solution Guide](solution-guide.md) - Resource tracking and lifecycle management (works standalone, no MCP needed)
- **RemediationPolicy CRD**: [Remediation Guide](remediation-guide.md) - Event-driven remediation (requires MCP)
- **ResourceSyncConfig CRD**: [Resource Sync Guide](resource-sync-guide.md) - Resource visibility and semantic search (requires MCP)
- **CapabilityScanConfig CRD**: [Capability Scan Guide](capability-scan-guide.md) - Autonomous capability discovery (requires MCP)

## Cleanup

### Remove Controller

```bash
# Uninstall controller
helm uninstall dot-ai-controller --namespace dot-ai

# Delete namespace
kubectl delete namespace dot-ai
```

### Remove Test Cluster

If you used Kind:

```bash
# Delete cluster
kind delete cluster --name dot-ai-controller-test

# Remove kubeconfig
rm kubeconfig.yaml
```
