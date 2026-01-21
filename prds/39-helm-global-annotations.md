# PRD: Global Annotations Support in Helm Chart

**Issue**: [#39](https://github.com/vfarcic/dot-ai-controller/issues/39)
**Status**: Draft
**Priority**: Low
**Created**: 2026-01-21

---

## Problem Statement

The Helm chart doesn't support custom annotations on Kubernetes resources. Users cannot:

1. Use tools like [Reloader](https://github.com/stakater/Reloader) to trigger rolling updates when ConfigMaps/Secrets change
2. Add audit/compliance annotations required by organizational policies
3. Integrate with external-secrets-operator, sealed-secrets, or similar tools
4. Apply consistent metadata across all deployed resources

Currently, no resources in the chart support custom annotations.

## Solution Overview

Add a single global `annotations` entry in `values.yaml` that applies to **all** rendered Kubernetes resources.

### Values Configuration

```yaml
# Global annotations applied to ALL resources
annotations: {}
  # Example: Reloader integration
  # reloader.stakater.com/auto: "true"
  # Example: Compliance
  # company.com/managed-by: "platform-team"
```

## Technical Design

### Template Helper Function

Create a helper function in `_helpers.tpl` to render annotations:

```yaml
{{/*
Render global annotations if defined.
*/}}
{{- define "dot-ai-controller.annotations" -}}
{{- if .Values.annotations -}}
  {{- toYaml .Values.annotations -}}
{{- end -}}
{{- end -}}
```

### Resources to Update

All templates rendering Kubernetes resources:

| Template | Resource(s) | Notes |
|----------|------------|-------|
| `deployment.yaml` | Deployment, Pod template | Pod annotations critical for Reloader |
| `rbac.yaml` | ServiceAccount, ClusterRole, ClusterRoleBinding | |
| `manager-role.yaml` | ClusterRole | Manager role for controller |
| CRD templates | CustomResourceDefinitions | Annotations on CRDs (if desired) |

## Success Criteria

1. **Global Application**: Setting `annotations` in values.yaml applies annotations to all rendered resources
2. **No Breaking Changes**: Existing configurations continue to work without modification
3. **Pod Annotations**: Reloader use case works (pod template annotations applied)
4. **Empty by Default**: `annotations: {}` produces no annotations (clean default output)

## Out of Scope

- Per-resource annotation overrides
- Label management (this PRD focuses on annotations only)
- Annotation validation

## Applicability

This PRD follows the same pattern as the dot-ai MCP server Helm chart (PRD #336) for consistency across all dot-ai projects.

---

## Milestones

- [ ] Create helper function for annotations in `_helpers.tpl`
- [ ] Add `annotations: {}` to `values.yaml` with documentation comments
- [ ] Update all templates to include global annotations
- [ ] Add unit tests for annotation rendering
- [ ] Update chart documentation with examples
