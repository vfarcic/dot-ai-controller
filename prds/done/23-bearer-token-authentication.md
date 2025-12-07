# PRD #23: Bearer Token Authentication for MCP Endpoint

## Overview

**Problem**: The dot-ai MCP server now supports optional Bearer token authentication (implemented in [vfarcic/dot-ai#257](https://github.com/vfarcic/dot-ai/pull/258)). When authentication is enabled on the MCP server via `DOT_AI_AUTH_TOKEN` environment variable, the controller cannot connect because it doesn't send the required `Authorization: Bearer <token>` header.

**Solution**: Add optional authentication support to the RemediationPolicy CRD, allowing users to reference a Kubernetes Secret containing the auth token. When configured, the controller will include the `Authorization: Bearer <token>` header in MCP requests.

**Priority**: High

**GitHub Issue**: [#23](https://github.com/vfarcic/dot-ai-controller/issues/23)

## User Stories

1. **As a platform engineer**, I want to configure my RemediationPolicy to authenticate with a secured dot-ai MCP server so that my production deployments remain secure.

2. **As an existing user**, I want authentication to be optional so that my current RemediationPolicy resources continue to work without changes.

3. **As a security-conscious operator**, I want to store the auth token in a Kubernetes Secret (not in the CRD spec directly) so that credentials are properly secured.

## Design Decisions

### Authentication Configuration Pattern
- **Secret Reference**: Follow the existing `SecretReference` pattern already used for Slack and Google Chat webhooks
- **Optional**: Authentication is opt-in; existing CRDs without auth config continue to work
- **CRD-level config**: Each RemediationPolicy can have its own auth token, allowing different policies to connect to different MCP servers with different credentials

### CRD Changes

Add new field to `RemediationPolicySpec`:

```go
// McpAuthSecretRef - Kubernetes Secret reference for MCP authentication (optional)
// When configured, the controller will include "Authorization: Bearer <token>" header
// References a Secret in the same namespace as the RemediationPolicy
// +optional
McpAuthSecretRef *SecretReference `json:"mcpAuthSecretRef,omitempty"`
```

### HTTP Request Changes

In `sendMcpRequest()`, add the Authorization header when `McpAuthSecretRef` is configured:

```go
// If auth is configured, add Authorization header
if authToken != "" {
    req.Header.Set("Authorization", "Bearer "+authToken)
}
```

## Technical Approach

### 1. CRD Type Changes (`api/v1alpha1/remediationpolicy_types.go`)

Add `McpAuthSecretRef` field to `RemediationPolicySpec` struct.

### 2. Controller Changes (`internal/controller/remediationpolicy_mcp.go`)

- Add `getMcpAuthToken()` helper to resolve auth token from Secret
- Update `sendMcpRequest()` to accept and include Authorization header

### 3. CRD Manifest Updates

Regenerate CRD manifests with `make generate && make manifests`.

### 4. Documentation Updates

Update docs/remediation-guide.md with authentication configuration examples.

## Success Criteria

1. RemediationPolicy CRD supports optional `mcpAuthSecretRef` field
2. Controller reads auth token from referenced Secret
3. Controller includes `Authorization: Bearer <token>` header when auth is configured
4. Existing RemediationPolicies without auth config continue to work unchanged
5. Clear error handling when Secret doesn't exist or key is missing
6. Documentation updated with authentication examples

## Milestones

- [x] Add `McpAuthSecretRef` field to RemediationPolicySpec
- [x] Update controller to resolve auth token from Secret
- [x] Update `sendMcpRequest` to include Authorization header
- [x] Regenerate CRD manifests (`make generate && make manifests`)
- [x] Update documentation and examples

## Status

**Complete** - 2025-12-07

## Progress Log

| Date | Update |
|------|--------|
| 2025-12-07 | PRD created based on dot-ai#257 implementation |
| 2025-12-07 | Implementation complete: CRD field, controller logic, docs updated. |

## Out of Scope (Future Enhancements)

- Controller-level default auth token (via Helm values)
- mTLS authentication
- OAuth2 / OIDC integration
- Token rotation / refresh mechanisms
