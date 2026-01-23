
### Features

- **Global Annotations Support in Helm Chart**

  The Helm chart now supports global annotations that apply to all rendered Kubernetes resources. Previously, users couldn't add custom annotations for tools like Reloader, compliance requirements, or consistent metadata across deployments.

  Set `annotations` in your Helm values to apply annotations to Deployments, Pods, ServiceAccounts, ClusterRoles, and ClusterRoleBindings. For example, enabling Reloader for automatic rolling updates when ConfigMaps change:

  ```yaml
  annotations:
    reloader.stakater.com/auto: "true"
    company.com/managed-by: "platform-team"
  ```

  Resource-specific annotations (when available) take precedence over global annotations, allowing overrides where needed. CRDs are intentionally excluded since annotation tools like Reloader watch workloads, not API schemas.

  See the [Setup Guide](https://devopstoolkit.ai/docs/controller/setup-guide) for the full configuration reference. ([#39](https://github.com/vfarcic/dot-ai-controller/issues/39))
