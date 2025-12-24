package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CapabilityScanConfigSpec defines the desired state of CapabilityScanConfig
type CapabilityScanConfigSpec struct {
	// MCP configuration for capability scanning
	// +required
	MCP MCPCapabilityConfig `json:"mcp"`

	// IncludeResources specifies patterns for resources to include in scanning
	// Patterns support wildcards: "*.crossplane.io", "deployments.apps", "Service"
	// Format: "Kind.group" for grouped resources, "Kind" for core resources
	// If empty, all resources are included (subject to excludeResources)
	// +optional
	IncludeResources []string `json:"includeResources,omitempty"`

	// ExcludeResources specifies patterns for resources to exclude from scanning
	// Applied after includeResources filtering
	// Patterns support wildcards: "*.internal.example.com", "events.*"
	// +optional
	ExcludeResources []string `json:"excludeResources,omitempty"`

	// Retry configuration for MCP API calls
	// +optional
	Retry RetryConfig `json:"retry,omitempty"`

	// DebounceWindowSeconds is the time window to collect CRD events before sending to MCP
	// When a CRD event is received, the controller waits for this duration to collect
	// more events, then sends them all in a single batched request.
	// This reduces HTTP requests when operators are installed (many CRDs at once).
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300
	// +optional
	DebounceWindowSeconds int `json:"debounceWindowSeconds,omitempty"`
}

// MCPCapabilityConfig holds MCP server configuration for capability scanning
type MCPCapabilityConfig struct {
	// Endpoint is the MCP server URL
	// +required
	Endpoint string `json:"endpoint"`

	// Collection is the Qdrant collection name for storing capabilities
	// +kubebuilder:default=capabilities
	// +optional
	Collection string `json:"collection,omitempty"`

	// AuthSecretRef references a Kubernetes Secret containing the MCP authentication token
	// The Secret must exist in the same namespace as the CapabilityScanConfig
	// +optional
	AuthSecretRef SecretReference `json:"authSecretRef,omitempty"`
}

// RetryConfig defines retry behavior for MCP API calls
type RetryConfig struct {
	// MaxAttempts is the maximum number of retry attempts (including initial attempt)
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxAttempts int `json:"maxAttempts,omitempty"`

	// BackoffSeconds is the initial backoff duration in seconds
	// Subsequent retries use exponential backoff (backoff * 2^attempt)
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300
	// +optional
	BackoffSeconds int `json:"backoffSeconds,omitempty"`

	// MaxBackoffSeconds is the maximum backoff duration in seconds
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3600
	// +optional
	MaxBackoffSeconds int `json:"maxBackoffSeconds,omitempty"`
}

// CapabilityScanConfigStatus defines the observed state of CapabilityScanConfig
type CapabilityScanConfigStatus struct {
	// Whether the initial scan has been completed
	// +optional
	InitialScanComplete bool `json:"initialScanComplete,omitempty"`

	// Timestamp of last successful scan trigger
	// +optional
	LastScanTime *metav1.Time `json:"lastScanTime,omitempty"`

	// Last error message if any
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Current conditions of the config
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Ready status"
// +kubebuilder:printcolumn:name="Initial Scan",type="boolean",JSONPath=".status.initialScanComplete",description="Initial scan completed"
// +kubebuilder:printcolumn:name="Last Scan",type="date",JSONPath=".status.lastScanTime",description="Last scan time"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// CapabilityScanConfig is the Schema for the capabilityscanconfigs API
// It configures the controller to watch CRDs and trigger capability scans via MCP
type CapabilityScanConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of CapabilityScanConfig
	// +required
	Spec CapabilityScanConfigSpec `json:"spec"`

	// status defines the observed state of CapabilityScanConfig
	// +optional
	Status CapabilityScanConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CapabilityScanConfigList contains a list of CapabilityScanConfig
type CapabilityScanConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapabilityScanConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CapabilityScanConfig{}, &CapabilityScanConfigList{})
}

// GetMaxAttempts returns the max retry attempts with default
func (r *CapabilityScanConfig) GetMaxAttempts() int {
	if r.Spec.Retry.MaxAttempts <= 0 {
		return 3
	}
	return r.Spec.Retry.MaxAttempts
}

// GetBackoffSeconds returns the initial backoff duration with default
func (r *CapabilityScanConfig) GetBackoffSeconds() int {
	if r.Spec.Retry.BackoffSeconds <= 0 {
		return 5
	}
	return r.Spec.Retry.BackoffSeconds
}

// GetMaxBackoffSeconds returns the max backoff duration with default
func (r *CapabilityScanConfig) GetMaxBackoffSeconds() int {
	if r.Spec.Retry.MaxBackoffSeconds <= 0 {
		return 300
	}
	return r.Spec.Retry.MaxBackoffSeconds
}

// GetCollection returns the Qdrant collection name with default
func (r *CapabilityScanConfig) GetCollection() string {
	if r.Spec.MCP.Collection == "" {
		return "capabilities"
	}
	return r.Spec.MCP.Collection
}

// GetDebounceWindowSeconds returns the debounce window with default
func (r *CapabilityScanConfig) GetDebounceWindowSeconds() int {
	if r.Spec.DebounceWindowSeconds <= 0 {
		return 10
	}
	return r.Spec.DebounceWindowSeconds
}
