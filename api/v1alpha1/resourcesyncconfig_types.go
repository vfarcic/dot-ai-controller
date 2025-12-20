package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceSyncConfigSpec defines the desired state of ResourceSyncConfig
type ResourceSyncConfigSpec struct {
	// MCP endpoint URL for resource sync
	// +required
	McpEndpoint string `json:"mcpEndpoint"`

	// McpAuthSecretRef references a Kubernetes Secret containing the MCP authentication token
	// The controller will include "Authorization: Bearer <token>" header in MCP requests
	// The Secret must exist in the same namespace as the ResourceSyncConfig
	// +required
	McpAuthSecretRef SecretReference `json:"mcpAuthSecretRef"`

	// DebounceWindowSeconds is the time window to collect changes before sending to MCP
	// Multiple changes to the same resource within this window are batched together
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300
	// +optional
	DebounceWindowSeconds int `json:"debounceWindowSeconds,omitempty"`

	// ResyncIntervalMinutes is how often to perform a full resync with MCP
	// This ensures eventual consistency by reconciling any missed changes
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1440
	// +optional
	ResyncIntervalMinutes int `json:"resyncIntervalMinutes,omitempty"`
}

// ResourceSyncConfigStatus defines the observed state of ResourceSyncConfig
type ResourceSyncConfigStatus struct {
	// Whether resource syncing is currently active
	// +optional
	Active bool `json:"active,omitempty"`

	// Number of resource types being watched
	// +optional
	WatchedResourceTypes int `json:"watchedResourceTypes,omitempty"`

	// Total resources synced to MCP
	// +optional
	TotalResourcesSynced int64 `json:"totalResourcesSynced,omitempty"`

	// Timestamp of last successful sync to MCP
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Timestamp of last full resync
	// +optional
	LastResyncTime *metav1.Time `json:"lastResyncTime,omitempty"`

	// Number of sync errors
	// +optional
	SyncErrors int64 `json:"syncErrors,omitempty"`

	// Last error message if any
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Current conditions of the config
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Active",type="boolean",JSONPath=".status.active",description="Whether sync is active"
// +kubebuilder:printcolumn:name="Watched",type="integer",JSONPath=".status.watchedResourceTypes",description="Resource types being watched"
// +kubebuilder:printcolumn:name="Synced",type="integer",JSONPath=".status.totalResourcesSynced",description="Total resources synced"
// +kubebuilder:printcolumn:name="Last Sync",type="date",JSONPath=".status.lastSyncTime",description="Last sync time"
// +kubebuilder:printcolumn:name="Errors",type="integer",JSONPath=".status.syncErrors",description="Sync errors"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ResourceSyncConfig is the Schema for the resourcesyncconfigs API
// It configures the controller to watch cluster resources and sync them to MCP
type ResourceSyncConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of ResourceSyncConfig
	// +required
	Spec ResourceSyncConfigSpec `json:"spec"`

	// status defines the observed state of ResourceSyncConfig
	// +optional
	Status ResourceSyncConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceSyncConfigList contains a list of ResourceSyncConfig
type ResourceSyncConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceSyncConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceSyncConfig{}, &ResourceSyncConfigList{})
}

// GetDebounceWindow returns the debounce window duration with default
func (r *ResourceSyncConfig) GetDebounceWindow() int {
	if r.Spec.DebounceWindowSeconds <= 0 {
		return 10 // default 10 seconds
	}
	return r.Spec.DebounceWindowSeconds
}

// GetResyncInterval returns the resync interval with default
func (r *ResourceSyncConfig) GetResyncInterval() int {
	if r.Spec.ResyncIntervalMinutes <= 0 {
		return 60 // default 60 minutes
	}
	return r.Spec.ResyncIntervalMinutes
}
