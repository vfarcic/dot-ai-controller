package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("ResourceSync Controller", func() {
	var (
		reconciler *ResourceSyncReconciler
		testCtx    context.Context
	)

	BeforeEach(func() {
		testCtx = context.Background()
		reconciler = &ResourceSyncReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			Recorder:   record.NewFakeRecorder(100),
			RestConfig: cfg,
		}
	})

	Describe("shouldSkipResource", func() {
		It("should skip core events", func() {
			Expect(reconciler.shouldSkipResource("", "events")).To(BeTrue())
		})

		It("should skip events.k8s.io events", func() {
			Expect(reconciler.shouldSkipResource("events.k8s.io", "events")).To(BeTrue())
		})

		It("should skip coordination.k8s.io leases", func() {
			Expect(reconciler.shouldSkipResource("coordination.k8s.io", "leases")).To(BeTrue())
		})

		It("should skip discovery.k8s.io endpointslices", func() {
			Expect(reconciler.shouldSkipResource("discovery.k8s.io", "endpointslices")).To(BeTrue())
		})

		It("should NOT skip pods", func() {
			Expect(reconciler.shouldSkipResource("", "pods")).To(BeFalse())
		})

		It("should NOT skip deployments", func() {
			Expect(reconciler.shouldSkipResource("apps", "deployments")).To(BeFalse())
		})

		It("should NOT skip custom resources", func() {
			Expect(reconciler.shouldSkipResource("dot-ai.devopstoolkit.live", "solutions")).To(BeFalse())
		})

		It("should NOT skip services", func() {
			Expect(reconciler.shouldSkipResource("", "services")).To(BeFalse())
		})

		It("should NOT skip configmaps", func() {
			Expect(reconciler.shouldSkipResource("", "configmaps")).To(BeFalse())
		})

		It("should NOT skip secrets", func() {
			Expect(reconciler.shouldSkipResource("", "secrets")).To(BeFalse())
		})
	})

	Describe("gvrFromCRD", func() {
		It("should extract GVR from a valid CRD", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]interface{}{
						"name": "solutions.dot-ai.devopstoolkit.live",
					},
					"spec": map[string]interface{}{
						"group": "dot-ai.devopstoolkit.live",
						"names": map[string]interface{}{
							"kind":     "Solution",
							"plural":   "solutions",
							"singular": "solution",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":    "v1alpha1",
								"served":  true,
								"storage": true,
							},
						},
					},
				},
			}

			gvr, err := reconciler.gvrFromCRD(crd)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Group).To(Equal("dot-ai.devopstoolkit.live"))
			Expect(gvr.Version).To(Equal("v1alpha1"))
			Expect(gvr.Resource).To(Equal("solutions"))
		})

		It("should prefer storage version when multiple versions exist", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]interface{}{
						"name": "databases.example.com",
					},
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"kind":   "Database",
							"plural": "databases",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":    "v1alpha1",
								"served":  true,
								"storage": false,
							},
							map[string]interface{}{
								"name":    "v1beta1",
								"served":  true,
								"storage": true,
							},
							map[string]interface{}{
								"name":    "v1",
								"served":  true,
								"storage": false,
							},
						},
					},
				},
			}

			gvr, err := reconciler.gvrFromCRD(crd)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Version).To(Equal("v1beta1")) // Storage version
		})

		It("should use first served version if no storage version specified", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]interface{}{
						"name": "widgets.example.com",
					},
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"kind":   "Widget",
							"plural": "widgets",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":   "v1",
								"served": true,
								// No storage field
							},
						},
					},
				},
			}

			gvr, err := reconciler.gvrFromCRD(crd)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Version).To(Equal("v1"))
		})

		It("should skip non-served versions", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]interface{}{
						"name": "gadgets.example.com",
					},
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"kind":   "Gadget",
							"plural": "gadgets",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":    "v1alpha1",
								"served":  false, // Not served
								"storage": false,
							},
							map[string]interface{}{
								"name":    "v1",
								"served":  true,
								"storage": true,
							},
						},
					},
				},
			}

			gvr, err := reconciler.gvrFromCRD(crd)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Version).To(Equal("v1")) // Should skip v1alpha1
		})

		It("should error if spec.group is missing", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"names": map[string]interface{}{
							"plural": "things",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":   "v1",
								"served": true,
							},
						},
					},
				},
			}

			_, err := reconciler.gvrFromCRD(crd)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.group"))
		})

		It("should error if spec.names.plural is missing", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"kind": "Thing",
							// Missing plural
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":   "v1",
								"served": true,
							},
						},
					},
				},
			}

			_, err := reconciler.gvrFromCRD(crd)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.names.plural"))
		})

		It("should error if no versions exist", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"plural": "things",
						},
						"versions": []interface{}{},
					},
				},
			}

			_, err := reconciler.gvrFromCRD(crd)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("versions"))
		})

		It("should error if no served versions exist", func() {
			crd := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"spec": map[string]interface{}{
						"group": "example.com",
						"names": map[string]interface{}{
							"plural": "things",
						},
						"versions": []interface{}{
							map[string]interface{}{
								"name":   "v1",
								"served": false, // Not served
							},
						},
					},
				},
			}

			_, err := reconciler.gvrFromCRD(crd)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no served version"))
		})
	})

	Describe("configChanged", func() {
		var oldConfig, newConfig *dotaiv1alpha1.ResourceSyncConfig

		BeforeEach(func() {
			oldConfig = &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint:           "https://mcp.example.com",
					DebounceWindowSeconds: 10,
					ResyncIntervalMinutes: 60,
				},
			}
			newConfig = oldConfig.DeepCopy()
		})

		It("should detect endpoint change", func() {
			newConfig.Spec.McpEndpoint = "https://mcp2.example.com"
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect debounce window change", func() {
			newConfig.Spec.DebounceWindowSeconds = 20
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect resync interval change", func() {
			newConfig.Spec.ResyncIntervalMinutes = 120
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect auth secret ref added", func() {
			newConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret",
				Key:  "api-key",
			}
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect auth secret ref removed", func() {
			oldConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret",
				Key:  "api-key",
			}
			newConfig.Spec.McpAuthSecretRef = nil
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect auth secret name change", func() {
			oldConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret",
				Key:  "api-key",
			}
			newConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret-new",
				Key:  "api-key",
			}
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect auth secret key change", func() {
			oldConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret",
				Key:  "api-key",
			}
			newConfig.Spec.McpAuthSecretRef = &dotaiv1alpha1.SecretReference{
				Name: "mcp-secret",
				Key:  "token",
			}
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should return false when nothing changed", func() {
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeFalse())
		})

		It("should return false when only metadata changed", func() {
			newConfig.ObjectMeta.ResourceVersion = "12345"
			newConfig.ObjectMeta.Generation = 2
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeFalse())
		})
	})

	Describe("containsVerb helper", func() {
		It("should find existing verb", func() {
			verbs := []string{"get", "list", "watch", "create"}
			Expect(containsVerb(verbs, "list")).To(BeTrue())
			Expect(containsVerb(verbs, "watch")).To(BeTrue())
		})

		It("should not find missing verb", func() {
			verbs := []string{"get", "list"}
			Expect(containsVerb(verbs, "watch")).To(BeFalse())
			Expect(containsVerb(verbs, "delete")).To(BeFalse())
		})

		It("should handle empty slice", func() {
			Expect(containsVerb([]string{}, "get")).To(BeFalse())
		})
	})

	Describe("GetDebounceWindow helper", func() {
		It("should return configured value", func() {
			config := &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					DebounceWindowSeconds: 30,
				},
			}
			Expect(config.GetDebounceWindow()).To(Equal(30))
		})

		It("should return default when zero", func() {
			config := &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					DebounceWindowSeconds: 0,
				},
			}
			Expect(config.GetDebounceWindow()).To(Equal(10)) // Default
		})

		It("should return default when negative", func() {
			config := &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					DebounceWindowSeconds: -5,
				},
			}
			Expect(config.GetDebounceWindow()).To(Equal(10)) // Default
		})
	})

	Describe("GetResyncInterval helper", func() {
		It("should return configured value", func() {
			config := &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					ResyncIntervalMinutes: 120,
				},
			}
			Expect(config.GetResyncInterval()).To(Equal(120))
		})

		It("should return default when zero", func() {
			config := &dotaiv1alpha1.ResourceSyncConfig{
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					ResyncIntervalMinutes: 0,
				},
			}
			Expect(config.GetResyncInterval()).To(Equal(60)) // Default
		})
	})

	Describe("Controller Reconciliation", func() {
		It("should handle non-existent ResourceSyncConfig gracefully", func() {
			// Reconcile a config that doesn't exist
			result, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "non-existent-config",
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())
		})

		It("should start watcher when ResourceSyncConfig is created", func() {
			// Create a ResourceSyncConfig
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config-" + randString(5),
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint:           "https://mcp.example.com/resources/sync",
					DebounceWindowSeconds: 10,
					ResyncIntervalMinutes: 60,
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			// Clean up after test
			defer func() {
				reconciler.stopWatcher(config.Name)
				_ = k8sClient.Delete(testCtx, config)
			}()

			// Reconcile
			result, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: config.Name,
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify watcher was started
			Expect(reconciler.GetActiveConfigCount()).To(Equal(1))

			// Verify GVRs were discovered
			gvrs := reconciler.GetWatchedGVRs(config.Name)
			Expect(len(gvrs)).To(BeNumerically(">", 0))

			// Verify common resources are being watched
			hasDeployments := false
			hasPods := false
			hasCRDs := false
			for _, gvr := range gvrs {
				if gvr.Group == "apps" && gvr.Resource == "deployments" {
					hasDeployments = true
				}
				if gvr.Group == "" && gvr.Resource == "pods" {
					hasPods = true
				}
				if gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions" {
					hasCRDs = true
				}
			}
			Expect(hasDeployments).To(BeTrue(), "Should watch deployments")
			Expect(hasPods).To(BeTrue(), "Should watch pods")
			Expect(hasCRDs).To(BeTrue(), "Should watch CRDs")
		})

		It("should stop watcher when ResourceSyncConfig is deleted", func() {
			// Create a ResourceSyncConfig
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config-delete-" + randString(5),
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint: "https://mcp.example.com/resources/sync",
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			// Reconcile to start watcher
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: config.Name,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(reconciler.GetActiveConfigCount()).To(Equal(1))

			// Delete the config
			Expect(k8sClient.Delete(testCtx, config)).To(Succeed())

			// Reconcile again - should stop watcher
			_, err = reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: config.Name,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(reconciler.GetActiveConfigCount()).To(Equal(0))
		})

		It("should restart watcher when config changes", func() {
			// Create a ResourceSyncConfig
			configName := "test-config-restart-" + randString(5)
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: configName,
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint:           "https://mcp.example.com/resources/sync",
					DebounceWindowSeconds: 10,
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			defer func() {
				reconciler.stopWatcher(configName)
				_ = k8sClient.Delete(testCtx, &dotaiv1alpha1.ResourceSyncConfig{
					ObjectMeta: metav1.ObjectMeta{Name: configName},
				})
			}()

			// Initial reconcile
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: configName,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch fresh copy before updating (to avoid conflict)
			freshConfig := &dotaiv1alpha1.ResourceSyncConfig{}
			Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: configName}, freshConfig)).To(Succeed())

			// Update the config
			freshConfig.Spec.DebounceWindowSeconds = 20
			Expect(k8sClient.Update(testCtx, freshConfig)).To(Succeed())

			// Reconcile again - should restart watcher
			_, err = reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: configName,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Watcher should still be active
			Expect(reconciler.GetActiveConfigCount()).To(Equal(1))
		})

		It("should update status with watched resource count", func() {
			// Create a ResourceSyncConfig
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-config-status-" + randString(5),
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint: "https://mcp.example.com/resources/sync",
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			defer func() {
				reconciler.stopWatcher(config.Name)
				_ = k8sClient.Delete(testCtx, config)
			}()

			// Reconcile
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: config.Name,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch updated config
			updatedConfig := &dotaiv1alpha1.ResourceSyncConfig{}
			Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: config.Name}, updatedConfig)).To(Succeed())

			// Verify status was updated
			Expect(updatedConfig.Status.Active).To(BeTrue())
			Expect(updatedConfig.Status.WatchedResourceTypes).To(BeNumerically(">", 0))

			// Verify Ready condition
			var readyCondition *metav1.Condition
			for i := range updatedConfig.Status.Conditions {
				if updatedConfig.Status.Conditions[i].Type == "Ready" {
					readyCondition = &updatedConfig.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("WatcherActive"))
		})
	})

	Describe("Resource Discovery", func() {
		It("should discover built-in resource types", func() {
			// Initialize clients
			Expect(reconciler.ensureClients()).To(Succeed())

			// Discover resources
			gvrs, err := reconciler.discoverResources(testCtx)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(gvrs)).To(BeNumerically(">", 0))

			// Check for expected built-in resources
			gvrMap := make(map[schema.GroupVersionResource]bool)
			for _, gvr := range gvrs {
				gvrMap[gvr] = true
			}

			// Core resources
			Expect(gvrMap[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}]).To(BeTrue())

			// Apps resources
			Expect(gvrMap[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}]).To(BeTrue())
			Expect(gvrMap[schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}]).To(BeTrue())
		})

		It("should skip events and leases", func() {
			// Initialize clients
			Expect(reconciler.ensureClients()).To(Succeed())

			// Discover resources
			gvrs, err := reconciler.discoverResources(testCtx)
			Expect(err).NotTo(HaveOccurred())

			// Check that skipped resources are not included
			for _, gvr := range gvrs {
				Expect(gvr.Resource).NotTo(Equal("events"), "Should not include events")
				Expect(gvr.Resource).NotTo(Equal("leases"), "Should not include leases")
				Expect(gvr.Resource).NotTo(Equal("endpointslices"), "Should not include endpointslices")
			}
		})

		It("should skip subresources", func() {
			// Initialize clients
			Expect(reconciler.ensureClients()).To(Succeed())

			// Discover resources
			gvrs, err := reconciler.discoverResources(testCtx)
			Expect(err).NotTo(HaveOccurred())

			// Check that no subresources are included
			for _, gvr := range gvrs {
				Expect(gvr.Resource).NotTo(ContainSubstring("/"), "Should not include subresources like pods/log")
			}
		})
	})
})

// Helper function to generate random strings for unique test names
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
