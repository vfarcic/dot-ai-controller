**ResourceSyncConfig: resource filtering and namespace scoping**

Large clusters can have hundreds of CRDs and thousands of namespaces. Without filtering, the controller creates one informer per resource type cluster-wide, leading to OOM on first install (observed: 756 informers, >4Gi memory on a real cluster).

`ResourceSyncConfig` now accepts four optional fields:

- `includeResources` — list of `resource.group` patterns to watch (default: all)
- `excludeResources` — list of `resource.group` patterns to exclude, applied after `includeResources`
- `watchNamespaces` — restrict instance watching to these namespaces (default: all)
- `watchNamespaceLabelSelector` — restrict instance watching to namespaces matching this label selector; takes precedence over `watchNamespaces`

Pattern format matches the existing CapabilityScanConfig convention: `"deployments.apps"`, `"pods."`, `"*.monitoring.coreos.com"`.

Example to reduce informers on a large cluster:

```yaml
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: default-sync
spec:
  mcpEndpoint: http://dot-ai:3456/api/v1/resources/sync
  mcpAuthSecretRef:
    name: dot-ai-secrets
    key: auth-token
  excludeResources:
    - "*.internal.autoscaling.k8s.io"
    - "*.metrics.k8s.io"
    - "events."
    - "events.events.k8s.io"
  watchNamespaceLabelSelector: "dot-ai.devopstoolkit.live/sync=true"
```

See the [Resource Sync Guide](https://devopstoolkit.ai/docs/controller/resource-sync-guide) for details.
