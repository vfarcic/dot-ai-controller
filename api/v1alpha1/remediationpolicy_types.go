/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// EventSelector defines criteria for selecting Kubernetes events
type EventSelector struct {
	// Type of event (Warning, Normal)
	// +optional
	Type string `json:"type,omitempty"`

	// Reason for the event
	// +optional
	Reason string `json:"reason,omitempty"`

	// Kind of the involved object
	// +optional
	InvolvedObjectKind string `json:"involvedObjectKind,omitempty"`

	// Namespace selector
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Remediation mode for this specific selector: "manual" or "automatic"
	// Overrides the global policy mode when specified
	// +kubebuilder:validation:Enum=manual;automatic
	// +optional
	Mode string `json:"mode,omitempty"`

	// Minimum confidence required for automatic execution (0.0-1.0)
	// Overrides the global policy confidenceThreshold when specified
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	ConfidenceThreshold *float64 `json:"confidenceThreshold,omitempty"`

	// Maximum risk level allowed for automatic execution
	// Overrides the global policy maxRiskLevel when specified
	// +kubebuilder:validation:Enum=low;medium;high
	// +optional
	MaxRiskLevel string `json:"maxRiskLevel,omitempty"`
}

// RateLimiting defines rate limiting configuration
type RateLimiting struct {
	// Maximum events per minute
	// +kubebuilder:default=10
	// +optional
	EventsPerMinute int `json:"eventsPerMinute,omitempty"`

	// Cooldown period in minutes after processing
	// +kubebuilder:default=5
	// +optional
	CooldownMinutes int `json:"cooldownMinutes,omitempty"`
}

// SlackConfig defines Slack notification configuration
type SlackConfig struct {
	// Enable Slack notifications
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Slack webhook URL (required when enabled)
	// +optional
	WebhookUrl string `json:"webhookUrl,omitempty"`

	// Slack channel (for display purposes only)
	// +optional
	Channel string `json:"channel,omitempty"`

	// Notify when remediation starts (optional, default false)
	// +kubebuilder:default=false
	// +optional
	NotifyOnStart bool `json:"notifyOnStart,omitempty"`

	// Notify when remediation completes (default true)
	// +kubebuilder:default=true
	// +optional
	NotifyOnComplete bool `json:"notifyOnComplete,omitempty"`
}

// NotificationConfig defines notification settings
type NotificationConfig struct {
	// Slack notification configuration
	// +optional
	Slack SlackConfig `json:"slack,omitempty"`
}

// RemediationPolicySpec defines the desired state of RemediationPolicy
type RemediationPolicySpec struct {
	// Event selection criteria
	// +required
	EventSelectors []EventSelector `json:"eventSelectors"`

	// MCP endpoint URL
	// +required
	McpEndpoint string `json:"mcpEndpoint"`

	// MCP tool name (always "remediate")
	// +kubebuilder:default="remediate"
	// +optional
	McpTool string `json:"mcpTool,omitempty"`

	// Remediation mode: "manual" or "automatic"
	// +kubebuilder:validation:Enum=manual;automatic
	// +kubebuilder:default="manual"
	// +optional
	Mode string `json:"mode,omitempty"`

	// Minimum confidence required for automatic execution (0.0-1.0)
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +kubebuilder:default=0.8
	// +optional
	ConfidenceThreshold *float64 `json:"confidenceThreshold,omitempty"`

	// Maximum risk level allowed for automatic execution
	// +kubebuilder:validation:Enum=low;medium;high
	// +kubebuilder:default="low"
	// +optional
	MaxRiskLevel string `json:"maxRiskLevel,omitempty"`

	// Rate limiting configuration
	// +optional
	RateLimiting RateLimiting `json:"rateLimiting,omitempty"`

	// Notification configuration
	// +optional
	Notifications NotificationConfig `json:"notifications,omitempty"`
}

// McpRequest represents the JSON request structure sent to the MCP remediate tool
// Based on the actual OpenAPI schema from /api/v1/openapi
type McpRequest struct {
	// Human-readable description of the issue (required, 1-2000 chars)
	Issue string `json:"issue"`

	// Remediation mode: "manual" or "automatic"
	Mode string `json:"mode"`

	// For automatic mode: minimum confidence required for execution (0.0-1.0)
	// Only included when mode is "automatic"
	ConfidenceThreshold *float64 `json:"confidenceThreshold,omitempty"`

	// For automatic mode: maximum risk level allowed for execution
	// Only included when mode is "automatic"
	MaxRiskLevel string `json:"maxRiskLevel,omitempty"`
}

// RemediationPolicyStatus defines the observed state of RemediationPolicy.
type RemediationPolicyStatus struct {
	// Timestamp of last processed event
	// +optional
	LastProcessedEvent *metav1.Time `json:"lastProcessedEvent,omitempty"`

	// Total number of events processed
	// +optional
	TotalEventsProcessed int64 `json:"totalEventsProcessed,omitempty"`

	// Number of successful remediation calls
	// +optional
	SuccessfulRemediations int64 `json:"successfulRemediations,omitempty"`

	// Number of failed remediation calls
	// +optional
	FailedRemediations int64 `json:"failedRemediations,omitempty"`

	// Total number of MCP messages generated
	// +optional
	TotalMcpMessagesGenerated int64 `json:"totalMcpMessagesGenerated,omitempty"`

	// Timestamp of last MCP message generated
	// +optional
	LastMcpMessageGenerated *metav1.Time `json:"lastMcpMessageGenerated,omitempty"`

	// Number of events that were rate limited
	// +optional
	RateLimitedEvents int64 `json:"rateLimitedEvents,omitempty"`

	// Timestamp of last rate limited event
	// +optional
	LastRateLimitedEvent *metav1.Time `json:"lastRateLimitedEvent,omitempty"`

	// Current conditions of the policy
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Whether the policy is ready"
// +kubebuilder:printcolumn:name="Events",type="integer",JSONPath=".status.totalEventsProcessed",description="Total events processed"
// +kubebuilder:printcolumn:name="Successful",type="integer",JSONPath=".status.successfulRemediations",description="Successful remediations"
// +kubebuilder:printcolumn:name="Failed",type="integer",JSONPath=".status.failedRemediations",description="Failed remediations"
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode",description="Remediation mode"
// +kubebuilder:printcolumn:name="Selectors",type="string",JSONPath=".spec.eventSelectors",description="Number of event selectors",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// RemediationPolicy is the Schema for the remediationpolicies API
type RemediationPolicy struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of RemediationPolicy
	// +required
	Spec RemediationPolicySpec `json:"spec"`

	// status defines the observed state of RemediationPolicy
	// +optional
	Status RemediationPolicyStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// RemediationPolicyList contains a list of RemediationPolicy
type RemediationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemediationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemediationPolicy{}, &RemediationPolicyList{})
}
