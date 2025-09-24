# Troubleshooting Guide

This guide covers common issues encountered when running the DevOps AI Toolkit Controller and their solutions.

## Common Issues and Solutions

### 1. Controller Pod Not Starting

**Symptoms:**
```bash
kubectl get pods --namespace dot-ai
# Shows controller pod in CrashLoopBackOff or ImagePullBackOff
```

**Diagnosis:**
```bash
kubectl logs --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai
kubectl describe pod --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai
```

**Common Causes:**
- **RBAC Issues**: Missing leader election permissions (we encountered this during testing)
- **Image Issues**: Wrong architecture or missing image
- **Resource Constraints**: Insufficient memory/CPU limits

**Solution:**
```bash
# Check if leader election RBAC is missing (error we fixed during testing):
# "leases.coordination.k8s.io is forbidden"
kubectl get clusterrole dot-ai-controller-manager-role --output yaml

# Add missing leader election permissions if needed:
kubectl patch clusterrole dot-ai-controller-manager-role --type='json' \
  --patch='[{"op": "add", "path": "/rules/-", "value": {"apiGroups": ["coordination.k8s.io"], "resources": ["leases"], "verbs": ["create", "get", "list", "update"]}}]'
```

### 2. Events Not Being Processed

**Symptoms:**
```bash
kubectl logs --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai --tail 50
# Shows: "No RemediationPolicies found - event will not be processed"
```

**Diagnosis:**
```bash
# Check if RemediationPolicies exist
kubectl get remediationpolicies --all-namespaces

# Check policy selectors
kubectl get remediationpolicies --namespace dot-ai --output yaml
```

**Common Causes:**
- No RemediationPolicy created
- Event doesn't match policy selectors
- Policy in wrong namespace

### 3. MCP Connection Failures

**Symptoms:**
```bash
# Controller logs show:
# "❌ HTTP request failed" or "Failed to send MCP request"
```

**Diagnosis:**
```bash
# Check MCP pod status
kubectl get pods --namespace dot-ai --selector app.kubernetes.io/name=dot-ai

# Test MCP connectivity from controller
kubectl exec --namespace dot-ai deployment/dot-ai-controller-manager -- \
  curl -v http://dot-ai-mcp.dot-ai.svc.cluster.local:3456/health
```

**Common Causes:**
- MCP pod not running
- Wrong MCP endpoint URL in RemediationPolicy
- Network policies blocking communication

### 4. Slack Notifications Not Working

**Symptoms:**
```bash
# Controller logs show:
# "failed to send Slack start notification"
```

**Diagnosis:**
```bash
# Check Slack webhook configuration
kubectl get remediationpolicies --namespace dot-ai --output yaml | grep --after-context 5 slack

# Test webhook manually
curl -X POST -H 'Content-type: application/json' \
  --data '{"text":"Test message"}' \
  YOUR_SLACK_WEBHOOK_URL
```

**Common Causes:**
- Invalid Slack webhook URL
- Slack webhook disabled (`enabled: false`)
- Network connectivity issues

### 5. Rate Limiting Active

**Symptoms:**
```bash
# Controller logs show:
# "Event processing rate limited" and "cooldown active for Xm Ys more"
```

**This is Expected Behavior:** Rate limiting prevents spam processing of duplicate events. The default settings are:
- `eventsPerMinute: 5`  
- `cooldownMinutes: 15`

**To Adjust:** Modify your RemediationPolicy:
```yaml
rateLimiting:
  eventsPerMinute: 10    # Increase if needed
  cooldownMinutes: 5     # Decrease if needed
```

### 6. MCP Analysis Failures

**Symptoms:**
```bash
# Controller logs show:
# "MCP remediation failed" or "McpRemediationFailed" events
```

**Diagnosis:**
```bash
# Check MCP logs for detailed error messages
kubectl logs --namespace dot-ai --selector app.kubernetes.io/name=dot-ai --tail 50

# Check RemediationPolicy status
kubectl describe remediationpolicies --namespace dot-ai
```

**Common Causes:**
- Invalid Anthropic API key
- API rate limits exceeded
- Network connectivity to Anthropic services
- Malformed event data

## Getting Help

### Collect Diagnostic Information

When reporting issues, include this diagnostic information:

```bash
# Controller status and logs
kubectl get pods --namespace dot-ai
kubectl logs --selector app.kubernetes.io/name=dot-ai-controller --namespace dot-ai --tail 100

# MCP status and logs
kubectl logs --namespace dot-ai --selector app.kubernetes.io/name=dot-ai --tail 50

# RemediationPolicy configuration
kubectl get remediationpolicies --namespace dot-ai --output yaml

# Recent events
kubectl get events --namespace dot-ai --sort-by='.lastTimestamp' --field-selector type=Warning
```

### Enable Debug Logging

For more detailed troubleshooting, you can increase log verbosity:

```bash
# Edit the controller deployment to add debug flags
kubectl patch deployment dot-ai-controller-manager --namespace dot-ai --patch='
{
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {
            "name": "manager",
            "args": ["--leader-elect", "--health-probe-bind-address=:8081", "-v=2"]
          }
        ]
      }
    }
  }
}'
```

## Resource Requirements

The default resource limits are:

**Controller:**
- Limits: 500m CPU, 128Mi memory
- Requests: 10m CPU, 64Mi memory

**MCP:**
- Limits: 1 CPU, 2Gi memory  
- Requests: 200m CPU, 512Mi memory

These should be sufficient for most use cases, but may need adjustment for high-volume environments.