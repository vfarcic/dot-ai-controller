# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

<!-- towncrier release notes start -->

## [0.44.1] - 2026-01-28

### Bug Fixes

- **Fix Infinite Reconciliation Loop When Status Exceeds etcd Size Limit**

  Resolves an issue where the ResourceSyncConfig controller would enter an infinite reconciliation loop when the status object grew too large, exceeding etcd's 3MB object size limit. This caused massive CPU usage and log flooding as the controller repeatedly attempted status updates that could never succeed.

  The controller now caps the `SyncErrors` counter at 100,000 to prevent unbounded growth and truncates error messages to 1KB maximum. When status updates fail due to "entity too large" errors, the controller enters a 5-minute backoff period instead of retrying immediately. All status fields are sanitized before each update to ensure they fit within etcd limits.

  If you encounter this issue before upgrading, the workaround is to delete and recreate the affected ResourceSyncConfig to reset its status. ([#42](https://github.com/vfarcic/dot-ai-controller/issues/42))


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
