# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

<!-- towncrier release notes start -->

## [0.44.0] - 2026-01-23

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


## [0.43.1] - 2026-01-19

### Documentation

- **Stack Installation Notes in Feature Guides**

  The Resource Sync Guide and Capability Scan Guide now include a "Stack Installation" section that clarifies these features are automatically configured when using the [DevOps AI Toolkit Stack](https://devopstoolkit.ai/docs/stack). Users who installed via the Stack can verify their configuration exists and skip the manual Quick Start steps. This helps users understand they only need to follow the manual setup if customizing the configuration or installing the controller individually. ([#38](https://github.com/vfarcic/dot-ai-controller/issues/38))
