
### Bug Fixes

- ## GitKnowledgeSource Handles Deleted Files in Incremental Sync

  GitKnowledgeSource incremental sync now correctly handles files deleted from the repository. Previously, deleted files caused persistent "file not found" errors and a `SyncPartial` status on every reconciliation cycle because the controller attempted to read content for files that no longer existed at HEAD.

  The controller now categorizes changed files as modified or deleted using git diff metadata. Modified and added files are ingested to the MCP knowledge base as before. Deleted files trigger a `deleteByUri` call to remove their chunks from the knowledge base, keeping the index accurate. Status messages now include the deleted document count (e.g., "Synced 3 documents, deleted 1").
