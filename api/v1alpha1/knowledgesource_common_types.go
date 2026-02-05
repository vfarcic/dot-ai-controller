package v1alpha1

// Common types shared across all KnowledgeSource CRDs (Git, Slack, Confluence, etc.)

// McpServerConfig defines the MCP server connection settings
// Used by all knowledge source CRDs to configure where to sync documents
type McpServerConfig struct {
	// URL is the MCP server endpoint
	// Example: "http://mcp-server.dot-ai.svc:3456"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://.*`
	URL string `json:"url"`

	// AuthSecretRef references a Secret containing the MCP authentication token
	// +kubebuilder:validation:Required
	AuthSecretRef SecretReference `json:"authSecretRef"`
}

// SkippedFile represents a file or document that was skipped during sync
// Used by knowledge source CRDs to report skipped items in status
type SkippedFile struct {
	// Path is the file path or identifier relative to the source
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Reason explains why the file was skipped
	// +kubebuilder:validation:Required
	Reason string `json:"reason"`
}
