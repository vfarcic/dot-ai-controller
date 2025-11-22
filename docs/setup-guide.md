# Setup Guide

This guide covers installation and initial setup of the DevOps AI Toolkit Controller.

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

The controller provides two features:
- **Solution CRD**: Resource tracking and lifecycle management (standalone)
- **RemediationPolicy CRD**: Event-driven remediation (requires [DevOps AI Toolkit MCP](https://github.com/vfarcic/dot-ai))

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
- RemediationPolicy CRD
- Solution CRD
- RBAC permissions

### Verify Installation

```bash
# Check controller is running
kubectl get pods --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai

# Check controller logs
kubectl logs --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai --tail 10

# Verify CRDs are installed
kubectl get crds | grep dot-ai.devopstoolkit.live
```

You should see both CRDs:
```
remediationpolicies.dot-ai.devopstoolkit.live
solutions.dot-ai.devopstoolkit.live
```

## Optional: Install DevOps AI Toolkit MCP

**Only required for RemediationPolicy features.** If you're only using the Solution CRD, skip this step.

For MCP installation instructions, see the [DevOps AI Toolkit documentation](https://github.com/vfarcic/dot-ai).

The controller expects the MCP service at: `http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/remediate`

## What's Next

Choose which features you want to use:

- **Solution CRD**: [Solution Guide](solution-guide.md) - Resource tracking and lifecycle management (works immediately, no MCP needed)
- **RemediationPolicy CRD**: [Remediation Guide](remediation-guide.md) - Event-driven remediation (requires MCP from previous step)

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
