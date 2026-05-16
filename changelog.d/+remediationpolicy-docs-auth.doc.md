## RemediationPolicy Docs Now Include Required mcpAuthSecretRef

The RemediationPolicy quickstart in `docs/index.md` and the API reference table in `examples/docs/api-reference.md` previously omitted `mcpAuthSecretRef`, even though the CRD marks it required. Users copying the published examples hit either a CRD validation error or silent `HTTP 401` failures from the MCP server with no remediation ever running ([#53](https://github.com/vfarcic/dot-ai-controller/issues/53)).

The quickstart now creates `dot-ai-secrets` and references it via `mcpAuthSecretRef`, mirroring the ResourceSyncConfig pattern. The API reference table marks `mcpAuthSecretRef` as required.
