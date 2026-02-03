package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KnowledgeSourceSpec defines the desired state of KnowledgeSource
type KnowledgeSourceSpec struct {
	// Repository defines the Git repository to sync documents from
	// +kubebuilder:validation:Required
	Repository RepositoryConfig `json:"repository"`

	// Paths specifies glob patterns for files to include
	// Example: ["docs/**/*.md", "README.md"]
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Paths []string `json:"paths"`

	// Exclude specifies glob patterns for files to exclude
	// Example: ["docs/internal/**"]
	// +optional
	Exclude []string `json:"exclude,omitempty"`

	// Schedule specifies when to sync in cron format
	// Example: "0 */6 * * *" (every 6 hours)
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// McpServer configures the MCP server endpoint for knowledge ingestion
	// +kubebuilder:validation:Required
	McpServer McpServerConfig `json:"mcpServer"`

	// Metadata contains key-value pairs attached to all ingested documents
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`

	// MaxFileSizeBytes limits the maximum file size to process
	// Files larger than this are skipped and reported in status
	// +optional
	MaxFileSizeBytes *int64 `json:"maxFileSizeBytes,omitempty"`
}

// RepositoryConfig defines the Git repository configuration
type RepositoryConfig struct {
	// URL is the Git repository URL (HTTPS only)
	// Example: "https://github.com/acme/platform.git"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https://.*`
	URL string `json:"url"`

	// Branch is the Git branch to sync from
	// +kubebuilder:default:="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// Depth is the shallow clone depth
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	Depth *int `json:"depth,omitempty"`

	// SecretRef references a Secret containing the Git authentication token
	// The token should be in the specified key within the Secret
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`
}

// McpServerConfig defines the MCP server connection settings
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

// KnowledgeSourceStatus defines the observed state of KnowledgeSource
type KnowledgeSourceStatus struct {
	// Active indicates whether the controller is actively syncing this source
	// +optional
	Active bool `json:"active,omitempty"`

	// LastSyncTime is the timestamp of the last successful sync
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// LastSyncedCommit is the Git commit SHA of the last successful sync
	// +optional
	LastSyncedCommit string `json:"lastSyncedCommit,omitempty"`

	// NextScheduledSync is the timestamp of the next scheduled sync
	// +optional
	NextScheduledSync *metav1.Time `json:"nextScheduledSync,omitempty"`

	// DocumentCount is the number of documents currently synced
	// +optional
	DocumentCount int `json:"documentCount,omitempty"`

	// SkippedDocuments is the count of documents skipped due to filters
	// +optional
	SkippedDocuments int `json:"skippedDocuments,omitempty"`

	// SkippedFiles lists files that were skipped with reasons
	// +optional
	SkippedFiles []SkippedFile `json:"skippedFiles,omitempty"`

	// SyncErrors is the count of errors in the last sync
	// +optional
	SyncErrors int `json:"syncErrors,omitempty"`

	// LastError contains the most recent error message
	// +optional
	LastError string `json:"lastError,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the KnowledgeSource's state
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// SkippedFile represents a file that was skipped during sync
type SkippedFile struct {
	// Path is the file path relative to repository root
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Reason explains why the file was skipped
	// +kubebuilder:validation:Required
	Reason string `json:"reason"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ks
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`,description="Whether sync is active"
// +kubebuilder:printcolumn:name="Documents",type=integer,JSONPath=`.status.documentCount`,description="Number of synced documents"
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSyncTime`,description="Time of last sync"
// +kubebuilder:printcolumn:name="Errors",type=integer,JSONPath=`.status.syncErrors`,description="Sync error count"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="Time since creation"

// KnowledgeSource is the Schema for the knowledgesources API
// It defines a Git repository to sync documents from into the MCP knowledge base
type KnowledgeSource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KnowledgeSourceSpec   `json:"spec,omitempty"`
	Status KnowledgeSourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KnowledgeSourceList contains a list of KnowledgeSource
type KnowledgeSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KnowledgeSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KnowledgeSource{}, &KnowledgeSourceList{})
}
