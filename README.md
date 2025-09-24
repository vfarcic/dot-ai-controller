# DevOps AI Toolkit Controller

A Kubernetes controller that watches cluster events and forwards them to the [DevOps AI Toolkit](https://github.com/vfarcic/dot-ai) MCP remediate tool for AI-powered analysis and remediation. This controller acts as the bridge between Kubernetes cluster events and the DevOps AI Toolkit.

## Description

The DevOps AI Toolkit Controller monitors Kubernetes events (like pod failures, crashes, scheduling issues) and automatically sends them to the [DevOps AI Toolkit MCP remediate tool](https://github.com/vfarcic/dot-ai/blob/main/docs/mcp-remediate-guide.md) for analysis. It supports:

- **Event Watching**: Configurable filtering of Kubernetes events by type, reason, and involved objects
- **DevOps AI Toolkit Integration**: Sends events to the DevOps AI Toolkit MCP remediate tool for intelligent analysis and remediation suggestions  
- **Slack Notifications**: Rich notifications with remediation results and next steps
- **Rate Limiting**: Prevents event storms with configurable cooldowns
- **Status Reporting**: Comprehensive observability through status updates and Kubernetes events

## Prerequisites

- **kubectl** v1.11.3+ (tested with v1.33.3)
- **Helm** v3.0+ (tested with v3.18.4)
- **Access to a Kubernetes cluster** v1.11.3+ (tested with v1.33.1)
- **[DevOps AI Toolkit](https://github.com/vfarcic/dot-ai) MCP service** running and accessible from the cluster

### Setting up a Test Cluster (Optional)

If you don't have a Kubernetes cluster, you can create one locally using Kind:

```bash
# Use isolated kubeconfig to avoid affecting existing configuration
export KUBECONFIG=$PWD/kubeconfig.yaml

# Create test cluster
kind create cluster --name dot-ai-controller-test

# Verify cluster access
kubectl cluster-info
```

## Installing the DevOps AI Toolkit MCP

The controller requires the DevOps AI Toolkit MCP service to be running in your cluster. Install it before deploying the controller:

### Prerequisites for MCP
- **Anthropic API key** - Get one from [Anthropic Console](https://console.anthropic.com/)

> **Note**: Check for the latest MCP version at [DevOps AI Toolkit Releases](https://github.com/vfarcic/dot-ai/releases)

### Install MCP Service

```bash
# Set your Anthropic API key
export ANTHROPIC_API_KEY="sk-ant-api03-..."

# Install the DevOps AI Toolkit MCP (check for latest version at link below)
helm install dot-ai-mcp oci://ghcr.io/vfarcic/dot-ai/charts/dot-ai:0.97.0 \
  --set secrets.anthropic.apiKey="$ANTHROPIC_API_KEY" \
  --create-namespace \
  --namespace dot-ai \
  --wait
```

### Verify MCP Installation

```bash
# Check that MCP pods are running
kubectl get pods -n dot-ai

# Verify MCP service is accessible
kubectl get svc -n dot-ai
```

You should see the `dot-ai-mcp` service running on port 3456. The controller will use the internal service URL: `http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/api/v1/tools/remediate`
