**ResourceSyncConfig: safer namespace list and CRD cleanup**

- `watchNamespaces` now ignores duplicate entries and rejects empty names, which previously could double-count resources or silently watch every namespace.
- Deleting a CRD now removes the informers for all of its served versions, not just one, so a CRD deleted during startup can no longer hold up the initial resync.
- The [Resource Sync Guide](https://devopstoolkit.ai/docs/controller/resource-sync-guide) documents the resource filtering and namespace scoping fields.
