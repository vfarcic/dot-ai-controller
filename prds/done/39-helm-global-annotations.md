# PRD: Global Annotations Support in Helm Chart

**Issue**: [#39](https://github.com/vfarcic/dot-ai-controller/issues/39)
**Status**: Complete
**Completed**: 2026-01-23
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

Create a helper function in `_helpers.tpl` that merges global and local annotations (consistent with dot-ai-ui pattern):

```yaml
{{/*
Merge global annotations with resource-specific annotations.
Resource-specific annotations take precedence over global annotations.
Usage: include "dot-ai-controller.annotations" (dict "global" .Values.annotations "local" .Values.ingress.annotations)
*/}}
{{- define "dot-ai-controller.annotations" -}}
{{- $global := .global | default dict -}}
{{- $local := .local | default dict -}}
{{- $merged := merge $local $global -}}
{{- if $merged -}}
{{- toYaml $merged -}}
{{- end -}}
{{- end -}}
```

### Resources to Update

Templates rendering workload-related Kubernetes resources:

| Template | Resource(s) | Notes |
|----------|------------|-------|
| `deployment.yaml` | Deployment, Pod template | Pod annotations critical for Reloader |
| `rbac.yaml` | ServiceAccount, ClusterRoleBinding | |
| `manager-role.yaml` | ClusterRole | Manager role for controller |

**Note**: CRDs are intentionally excluded - see Decision Log below.

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

## Decision Log

### CRDs Excluded from Global Annotations (2026-01-23)

**Decision**: CRD templates do not receive global annotations.

**Rationale**:
1. **Use case mismatch**: Reloader and similar tools watch Deployments/Pods, not CRDs
2. **CRDs are API schemas**: They define resource types, not workloads
3. **Release workflow constraint**: CRDs are copied from `config/crd/bases/` during release, overwriting any templating
4. **Consistency**: dot-ai-ui (which has no CRDs) uses the same pattern

---

## Milestones

- [x] Create helper function for annotations in `_helpers.tpl`
- [x] Add `annotations: {}` to `values.yaml` with documentation comments
- [x] Update all templates to include global annotations
- [~] Add unit tests for annotation rendering (deferred - no Helm test framework)
- [x] Update chart documentation with examples
