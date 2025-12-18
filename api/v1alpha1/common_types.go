package v1alpha1

// Common types shared across multiple CRDs

// SecretReference references a key in a Kubernetes Secret
type SecretReference struct {
	// Name of the secret in the same namespace as the resource
	// +required
	Name string `json:"name"`

	// Key within the secret containing the value
	// +required
	Key string `json:"key"`
}
