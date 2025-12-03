package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SolutionSpec defines the desired state of Solution
type SolutionSpec struct {
	// Intent describes the original user intent that led to this deployment
	// Example: "Deploy Go microservice with PostgreSQL database and Redis cache"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Intent string `json:"intent"`

	// Context contains solution-level information not available in individual resources
	// +optional
	Context SolutionContext `json:"context,omitempty"`

	// Resources lists all Kubernetes resources that compose this solution
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Resources []ResourceReference `json:"resources"`

	// DocumentationURL points to documentation for this solution
	// This field is populated by external tools (e.g., dot-ai PRD #228)
	// +optional
	// +kubebuilder:validation:Pattern=`^https?://.*`
	DocumentationURL string `json:"documentationURL,omitempty"`
}

// SolutionContext contains contextual information about the solution deployment
type SolutionContext struct {
	// CreatedBy identifies the tool or user that created this solution
	// +optional
	CreatedBy string `json:"createdBy,omitempty"`

	// Rationale explains why this solution was deployed this way
	// +optional
	Rationale string `json:"rationale,omitempty"`

	// Patterns lists the organizational patterns applied to this solution
	// +optional
	Patterns []string `json:"patterns,omitempty"`

	// Policies lists the policies applied to this solution
	// +optional
	Policies []string `json:"policies,omitempty"`
}

// ResourceReference identifies a Kubernetes resource that is part of this solution
type ResourceReference struct {
	// APIVersion of the resource
	// +kubebuilder:validation:Required
	APIVersion string `json:"apiVersion"`

	// Kind of the resource
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name of the resource
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the resource (if namespaced)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SolutionStatus defines the observed state of Solution
type SolutionStatus struct {
	// State represents the overall state of the solution
	// +kubebuilder:validation:Enum=pending;deployed;degraded;failed
	// +optional
	State string `json:"state,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Resources provides a summary of resource health
	// +optional
	Resources ResourceSummary `json:"resources,omitempty"`

	// Conditions represent the latest available observations of the solution's state
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// ResourceSummary provides statistics about solution resources
type ResourceSummary struct {
	// Total number of resources in this solution
	// +optional
	Total int `json:"total,omitempty"`

	// Ready number of resources that are ready
	// +optional
	Ready int `json:"ready,omitempty"`

	// Failed number of resources that have failed
	// +optional
	Failed int `json:"failed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sol
// +kubebuilder:printcolumn:name="Intent",type=string,JSONPath=`.spec.intent`,description="Original user intent"
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`,description="Solution state"
// +kubebuilder:printcolumn:name="Resources",type=string,JSONPath=`.status.resources.ready`,description="Ready resources"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="Time since creation"

// Solution is the Schema for the solutions API
type Solution struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SolutionSpec   `json:"spec,omitempty"`
	Status SolutionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SolutionList contains a list of Solution
type SolutionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Solution `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Solution{}, &SolutionList{})
}
