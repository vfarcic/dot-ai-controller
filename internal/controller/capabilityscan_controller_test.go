package controller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// Unit tests for pattern matching functions
func TestMatchesPattern(t *testing.T) {
	tests := []struct {
		name       string
		resourceID string
		pattern    string
		want       bool
	}{
		// Exact matches
		{
			name:       "exact match with group",
			resourceID: "RDSInstance.database.aws.crossplane.io",
			pattern:    "RDSInstance.database.aws.crossplane.io",
			want:       true,
		},
		{
			name:       "exact match core resource",
			resourceID: "Service",
			pattern:    "Service",
			want:       true,
		},
		{
			name:       "no match different kind",
			resourceID: "Deployment.apps",
			pattern:    "StatefulSet.apps",
			want:       false,
		},

		// Wildcard all
		{
			name:       "wildcard matches everything",
			resourceID: "RDSInstance.database.aws.crossplane.io",
			pattern:    "*",
			want:       true,
		},
		{
			name:       "wildcard matches core resource",
			resourceID: "Service",
			pattern:    "*",
			want:       true,
		},

		// Kind wildcard
		{
			name:       "kind wildcard matches any kind in group",
			resourceID: "RDSInstance.database.aws.crossplane.io",
			pattern:    "*.database.aws.crossplane.io",
			want:       true,
		},
		{
			name:       "kind wildcard different group no match",
			resourceID: "RDSInstance.database.aws.crossplane.io",
			pattern:    "*.s3.aws.crossplane.io",
			want:       false,
		},

		// Group wildcard
		{
			name:       "group wildcard matches any group",
			resourceID: "Deployment.apps",
			pattern:    "Deployment.*",
			want:       true,
		},
		{
			name:       "group wildcard different kind no match",
			resourceID: "StatefulSet.apps",
			pattern:    "Deployment.*",
			want:       false,
		},

		// Suffix patterns (*.crossplane.io)
		{
			name:       "suffix pattern matches nested group",
			resourceID: "RDSInstance.database.aws.crossplane.io",
			pattern:    "*.crossplane.io",
			want:       true,
		},
		{
			name:       "suffix pattern matches direct group",
			resourceID: "Provider.pkg.crossplane.io",
			pattern:    "*.crossplane.io",
			want:       true,
		},
		{
			name:       "suffix pattern no match different suffix",
			resourceID: "Application.argoproj.io",
			pattern:    "*.crossplane.io",
			want:       false,
		},

		// Both wildcards
		{
			name:       "both wildcards match grouped resource",
			resourceID: "Deployment.apps",
			pattern:    "*.*",
			want:       true,
		},
		{
			name:       "both wildcards no match core resource",
			resourceID: "Service",
			pattern:    "*.*",
			want:       false,
		},

		// Edge cases
		{
			name:       "empty pattern no match",
			resourceID: "Service",
			pattern:    "",
			want:       false,
		},
		{
			name:       "core resource pattern no match grouped",
			resourceID: "Deployment.apps",
			pattern:    "Deployment",
			want:       false,
		},
		{
			name:       "grouped pattern no match core",
			resourceID: "Service",
			pattern:    "Service.v1",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPattern(tt.resourceID, tt.pattern)
			if got != tt.want {
				t.Errorf("matchesPattern(%q, %q) = %v, want %v", tt.resourceID, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchesGroupPattern(t *testing.T) {
	tests := []struct {
		name    string
		group   string
		pattern string
		want    bool
	}{
		{
			name:    "exact match",
			group:   "apps",
			pattern: "apps",
			want:    true,
		},
		{
			name:    "wildcard matches all",
			group:   "database.aws.crossplane.io",
			pattern: "*",
			want:    true,
		},
		{
			name:    "suffix pattern matches",
			group:   "database.aws.crossplane.io",
			pattern: "*.crossplane.io",
			want:    true,
		},
		{
			name:    "suffix pattern partial match",
			group:   "pkg.crossplane.io",
			pattern: "*.crossplane.io",
			want:    true,
		},
		{
			name:    "suffix pattern no match",
			group:   "argoproj.io",
			pattern: "*.crossplane.io",
			want:    false,
		},
		{
			name:    "no match different group",
			group:   "apps",
			pattern: "batch",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesGroupPattern(tt.group, tt.pattern)
			if got != tt.want {
				t.Errorf("matchesGroupPattern(%q, %q) = %v, want %v", tt.group, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestShouldProcessResource(t *testing.T) {
	reconciler := &CapabilityScanReconciler{}

	tests := []struct {
		name       string
		config     *dotaiv1alpha1.CapabilityScanConfig
		resourceID string
		want       bool
	}{
		{
			name: "no filters processes all",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{},
			},
			resourceID: "RDSInstance.database.aws.crossplane.io",
			want:       true,
		},
		{
			name: "include filter matches",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io"},
				},
			},
			resourceID: "RDSInstance.database.aws.crossplane.io",
			want:       true,
		},
		{
			name: "include filter no match",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io"},
				},
			},
			resourceID: "Application.argoproj.io",
			want:       false,
		},
		{
			name: "exclude filter blocks",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					ExcludeResources: []string{"*.internal.example.com"},
				},
			},
			resourceID: "Secret.internal.example.com",
			want:       false,
		},
		{
			name: "exclude filter allows others",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					ExcludeResources: []string{"*.internal.example.com"},
				},
			},
			resourceID: "RDSInstance.database.aws.crossplane.io",
			want:       true,
		},
		{
			name: "include and exclude combined - include matches exclude blocks",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io"},
					ExcludeResources: []string{"*.aws.crossplane.io"},
				},
			},
			resourceID: "RDSInstance.database.aws.crossplane.io",
			want:       false,
		},
		{
			name: "include and exclude combined - include matches exclude allows",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io"},
					ExcludeResources: []string{"*.aws.crossplane.io"},
				},
			},
			resourceID: "Provider.pkg.crossplane.io",
			want:       true,
		},
		{
			name: "multiple include patterns - first matches",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io", "*.argoproj.io"},
				},
			},
			resourceID: "RDSInstance.database.aws.crossplane.io",
			want:       true,
		},
		{
			name: "multiple include patterns - second matches",
			config: &dotaiv1alpha1.CapabilityScanConfig{
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					IncludeResources: []string{"*.crossplane.io", "*.argoproj.io"},
				},
			},
			resourceID: "Application.argoproj.io",
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconciler.shouldProcessResource(tt.config, tt.resourceID)
			if got != tt.want {
				t.Errorf("shouldProcessResource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildCapabilityID(t *testing.T) {
	tests := []struct {
		name string
		crd  *apiextensionsv1.CustomResourceDefinition
		want string
	}{
		{
			name: "grouped resource",
			crd: &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "database.aws.crossplane.io",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind: "RDSInstance",
					},
				},
			},
			want: "RDSInstance.database.aws.crossplane.io",
		},
		{
			name: "apps group",
			crd: &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "apps",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind: "Deployment",
					},
				},
			},
			want: "Deployment.apps",
		},
		{
			name: "empty group (core)",
			crd: &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Kind: "Service",
					},
				},
			},
			want: "Service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCapabilityID(tt.crd)
			if got != tt.want {
				t.Errorf("buildCapabilityID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStringSlicesEqual(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{
			name: "equal slices",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "b", "c"},
			want: true,
		},
		{
			name: "different length",
			a:    []string{"a", "b"},
			b:    []string{"a", "b", "c"},
			want: false,
		},
		{
			name: "different content",
			a:    []string{"a", "b", "c"},
			b:    []string{"a", "x", "c"},
			want: false,
		},
		{
			name: "both empty",
			a:    []string{},
			b:    []string{},
			want: true,
		},
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "nil vs empty",
			a:    nil,
			b:    []string{},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringSlicesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("stringSlicesEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeCapabilityDiff(t *testing.T) {
	tests := []struct {
		name            string
		clusterCRDs     []string
		mcpCapabilities []string
		wantToScan      []string
		wantToDelete    []string
	}{
		{
			name:            "both empty",
			clusterCRDs:     []string{},
			mcpCapabilities: []string{},
			wantToScan:      nil,
			wantToDelete:    nil,
		},
		{
			name:            "cluster has CRDs, MCP empty",
			clusterCRDs:     []string{"A.group", "B.group", "C.group"},
			mcpCapabilities: []string{},
			wantToScan:      []string{"A.group", "B.group", "C.group"},
			wantToDelete:    nil,
		},
		{
			name:            "cluster empty, MCP has capabilities",
			clusterCRDs:     []string{},
			mcpCapabilities: []string{"X.group", "Y.group"},
			wantToScan:      nil,
			wantToDelete:    []string{"X.group", "Y.group"},
		},
		{
			name:            "in sync",
			clusterCRDs:     []string{"A.group", "B.group", "C.group"},
			mcpCapabilities: []string{"A.group", "B.group", "C.group"},
			wantToScan:      nil,
			wantToDelete:    nil,
		},
		{
			name:            "missing CRDs in MCP",
			clusterCRDs:     []string{"A.group", "B.group", "C.group", "D.group"},
			mcpCapabilities: []string{"A.group", "B.group"},
			wantToScan:      []string{"C.group", "D.group"},
			wantToDelete:    nil,
		},
		{
			name:            "orphaned capabilities in MCP",
			clusterCRDs:     []string{"A.group", "B.group"},
			mcpCapabilities: []string{"A.group", "B.group", "C.group", "D.group"},
			wantToScan:      nil,
			wantToDelete:    []string{"C.group", "D.group"},
		},
		{
			name:            "mixed changes",
			clusterCRDs:     []string{"A.group", "B.group", "D.group"},
			mcpCapabilities: []string{"A.group", "B.group", "C.group"},
			wantToScan:      []string{"D.group"},
			wantToDelete:    []string{"C.group"},
		},
		{
			name:            "complete replacement",
			clusterCRDs:     []string{"X.group", "Y.group"},
			mcpCapabilities: []string{"A.group", "B.group"},
			wantToScan:      []string{"X.group", "Y.group"},
			wantToDelete:    []string{"A.group", "B.group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToScan, gotToDelete := computeCapabilityDiff(tt.clusterCRDs, tt.mcpCapabilities)

			// Compare toScan
			if len(gotToScan) != len(tt.wantToScan) {
				t.Errorf("toScan length = %v, want %v", len(gotToScan), len(tt.wantToScan))
			} else {
				toScanSet := make(map[string]bool)
				for _, s := range gotToScan {
					toScanSet[s] = true
				}
				for _, want := range tt.wantToScan {
					if !toScanSet[want] {
						t.Errorf("toScan missing %v", want)
					}
				}
			}

			// Compare toDelete
			if len(gotToDelete) != len(tt.wantToDelete) {
				t.Errorf("toDelete length = %v, want %v", len(gotToDelete), len(tt.wantToDelete))
			} else {
				toDeleteSet := make(map[string]bool)
				for _, s := range gotToDelete {
					toDeleteSet[s] = true
				}
				for _, want := range tt.wantToDelete {
					if !toDeleteSet[want] {
						t.Errorf("toDelete missing %v", want)
					}
				}
			}
		})
	}
}

func TestConfigChanged(t *testing.T) {
	reconciler := &CapabilityScanReconciler{}

	baseConfig := &dotaiv1alpha1.CapabilityScanConfig{
		Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
			MCP: dotaiv1alpha1.MCPCapabilityConfig{
				Endpoint:   "http://mcp:8080",
				Collection: "capabilities",
				AuthSecretRef: dotaiv1alpha1.SecretReference{
					Name: "mcp-secret",
					Key:  "token",
				},
			},
			IncludeResources: []string{"*.crossplane.io"},
			ExcludeResources: []string{"*.internal.example.com"},
		},
	}

	tests := []struct {
		name    string
		old     *dotaiv1alpha1.CapabilityScanConfig
		new     *dotaiv1alpha1.CapabilityScanConfig
		changed bool
	}{
		{
			name:    "no changes",
			old:     baseConfig,
			new:     baseConfig.DeepCopy(),
			changed: false,
		},
		{
			name: "endpoint changed",
			old:  baseConfig,
			new: func() *dotaiv1alpha1.CapabilityScanConfig {
				c := baseConfig.DeepCopy()
				c.Spec.MCP.Endpoint = "http://new-mcp:8080"
				return c
			}(),
			changed: true,
		},
		{
			name: "collection changed",
			old:  baseConfig,
			new: func() *dotaiv1alpha1.CapabilityScanConfig {
				c := baseConfig.DeepCopy()
				c.Spec.MCP.Collection = "new-collection"
				return c
			}(),
			changed: true,
		},
		{
			name: "auth secret name changed",
			old:  baseConfig,
			new: func() *dotaiv1alpha1.CapabilityScanConfig {
				c := baseConfig.DeepCopy()
				c.Spec.MCP.AuthSecretRef.Name = "new-secret"
				return c
			}(),
			changed: true,
		},
		{
			name: "include resources changed",
			old:  baseConfig,
			new: func() *dotaiv1alpha1.CapabilityScanConfig {
				c := baseConfig.DeepCopy()
				c.Spec.IncludeResources = []string{"*.argoproj.io"}
				return c
			}(),
			changed: true,
		},
		{
			name: "exclude resources changed",
			old:  baseConfig,
			new: func() *dotaiv1alpha1.CapabilityScanConfig {
				c := baseConfig.DeepCopy()
				c.Spec.ExcludeResources = []string{"*.new.example.com"}
				return c
			}(),
			changed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconciler.configChanged(tt.old, tt.new)
			if got != tt.changed {
				t.Errorf("configChanged() = %v, want %v", got, tt.changed)
			}
		})
	}
}

// Ginkgo integration tests
var _ = Describe("CapabilityScan Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When creating a CapabilityScanConfig", func() {
		It("Should be created successfully", func() {
			ctx := context.Background()

			// Create a CapabilityScanConfig
			config := &dotaiv1alpha1.CapabilityScanConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-capability-scan-config",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.CapabilityScanConfigSpec{
					MCP: dotaiv1alpha1.MCPCapabilityConfig{
						Endpoint:   "http://mcp-test:8080",
						Collection: "test-capabilities",
					},
					IncludeResources: []string{"*.crossplane.io"},
				},
			}

			Expect(k8sClient.Create(ctx, config)).Should(Succeed())

			// Verify the config was created
			configLookupKey := types.NamespacedName{Name: config.Name, Namespace: config.Namespace}
			createdConfig := &dotaiv1alpha1.CapabilityScanConfig{}

			Eventually(func() bool {
				err := k8sClient.Get(ctx, configLookupKey, createdConfig)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			Expect(createdConfig.Spec.MCP.Endpoint).To(Equal("http://mcp-test:8080"))
			Expect(createdConfig.Spec.IncludeResources).To(ContainElement("*.crossplane.io"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, config)).Should(Succeed())
		})
	})
})
