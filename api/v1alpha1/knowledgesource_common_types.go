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

	// HttpTimeoutSeconds is the HTTP timeout in seconds for MCP API calls
	// Increase this if syncing large documents that take longer to process
	// +kubebuilder:default=120
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=600
	// +optional
	HttpTimeoutSeconds int `json:"httpTimeoutSeconds,omitempty"`
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
