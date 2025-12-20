package controller

import (
	"context"
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
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
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-secret",
						Key:  "api-key",
					},
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

		It("should detect auth secret name change", func() {
			newConfig.Spec.McpAuthSecretRef = dotaiv1alpha1.SecretReference{
				Name: "mcp-secret-new",
				Key:  "api-key",
			}
			Expect(reconciler.configChanged(oldConfig, newConfig)).To(BeTrue())
		})

		It("should detect auth secret key change", func() {
			newConfig.Spec.McpAuthSecretRef = dotaiv1alpha1.SecretReference{
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
					Name:      "non-existent-config",
					Namespace: "default",
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())
		})

		It("should start watcher when ResourceSyncConfig is created", func() {
			// Create a ResourceSyncConfig
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config-" + randString(5),
					Namespace: "default",
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
				reconciler.stopWatcher(config.Namespace + "/" + config.Name)
				_ = k8sClient.Delete(testCtx, config)
			}()

			// Reconcile
			result, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      config.Name,
					Namespace: config.Namespace,
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify watcher was started
			Expect(reconciler.GetActiveConfigCount()).To(Equal(1))

			// Verify GVRs were discovered
			gvrs := reconciler.GetWatchedGVRs(config.Namespace + "/" + config.Name)
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
					Name:      "test-config-delete-" + randString(5),
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint: "https://mcp.example.com/resources/sync",
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			// Reconcile to start watcher
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      config.Name,
					Namespace: config.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(reconciler.GetActiveConfigCount()).To(Equal(1))

			// Delete the config
			Expect(k8sClient.Delete(testCtx, config)).To(Succeed())

			// Reconcile again - should stop watcher
			_, err = reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      config.Name,
					Namespace: config.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(reconciler.GetActiveConfigCount()).To(Equal(0))
		})

		It("should restart watcher when config changes", func() {
			// Create a ResourceSyncConfig
			configName := "test-config-restart-" + randString(5)
			configNamespace := "default"
			config := &dotaiv1alpha1.ResourceSyncConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configName,
					Namespace: configNamespace,
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint:           "https://mcp.example.com/resources/sync",
					DebounceWindowSeconds: 10,
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			defer func() {
				reconciler.stopWatcher(configNamespace + "/" + configName)
				_ = k8sClient.Delete(testCtx, &dotaiv1alpha1.ResourceSyncConfig{
					ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: configNamespace},
				})
			}()

			// Initial reconcile
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: configNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch fresh copy before updating (to avoid conflict)
			freshConfig := &dotaiv1alpha1.ResourceSyncConfig{}
			Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: configName, Namespace: configNamespace}, freshConfig)).To(Succeed())

			// Update the config
			freshConfig.Spec.DebounceWindowSeconds = 20
			Expect(k8sClient.Update(testCtx, freshConfig)).To(Succeed())

			// Reconcile again - should restart watcher
			_, err = reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      configName,
					Namespace: configNamespace,
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
					Name:      "test-config-status-" + randString(5),
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.ResourceSyncConfigSpec{
					McpEndpoint: "https://mcp.example.com/resources/sync",
				},
			}

			Expect(k8sClient.Create(testCtx, config)).To(Succeed())

			defer func() {
				reconciler.stopWatcher(config.Namespace + "/" + config.Name)
				_ = k8sClient.Delete(testCtx, config)
			}()

			// Reconcile
			_, err := reconciler.Reconcile(testCtx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      config.Name,
					Namespace: config.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Fetch updated config
			updatedConfig := &dotaiv1alpha1.ResourceSyncConfig{}
			Expect(k8sClient.Get(testCtx, types.NamespacedName{Name: config.Name, Namespace: config.Namespace}, updatedConfig)).To(Succeed())

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

	Describe("buildResourceID", func() {
		It("should build ID for namespaced resource", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "nginx",
						"namespace": "default",
					},
				},
			}
			id := buildResourceID(obj)
			Expect(id).To(Equal("default:apps/v1:Deployment:nginx"))
		})

		It("should build ID for cluster-scoped resource", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Node",
					"metadata": map[string]interface{}{
						"name": "worker-1",
					},
				},
			}
			id := buildResourceID(obj)
			Expect(id).To(Equal("_cluster:v1:Node:worker-1"))
		})

		It("should build ID for core API resource", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "kube-system",
					},
				},
			}
			id := buildResourceID(obj)
			Expect(id).To(Equal("kube-system:v1:Pod:my-pod"))
		})

		It("should build ID for CRD resource", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "dot-ai.devopstoolkit.live/v1alpha1",
					"kind":       "Solution",
					"metadata": map[string]interface{}{
						"name":      "my-solution",
						"namespace": "prod",
					},
				},
			}
			id := buildResourceID(obj)
			Expect(id).To(Equal("prod:dot-ai.devopstoolkit.live/v1alpha1:Solution:my-solution"))
		})

		It("should build ID for cluster-scoped CRD", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "rbac.authorization.k8s.io/v1",
					"kind":       "ClusterRole",
					"metadata": map[string]interface{}{
						"name": "admin",
					},
				},
			}
			id := buildResourceID(obj)
			Expect(id).To(Equal("_cluster:rbac.authorization.k8s.io/v1:ClusterRole:admin"))
		})
	})

	Describe("extractResourceData", func() {
		It("should extract basic metadata", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":              "nginx",
						"namespace":         "default",
						"creationTimestamp": "2025-01-01T10:00:00Z",
					},
				},
			}

			data := extractResourceData(obj)
			// ID is no longer in ResourceData - MCP constructs it from namespace/apiVersion/kind/name
			Expect(data.Name).To(Equal("nginx"))
			Expect(data.Namespace).To(Equal("default"))
			Expect(data.Kind).To(Equal("Deployment"))
			Expect(data.APIVersion).To(Equal("apps/v1"))
		})

		It("should use _cluster namespace for cluster-scoped resources", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Node",
					"metadata": map[string]interface{}{
						"name": "worker-1",
						// No namespace - cluster-scoped
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Namespace).To(Equal("_cluster"))
			Expect(data.Name).To(Equal("worker-1"))
			Expect(data.Kind).To(Equal("Node"))
		})

		It("should use _cluster namespace for ClusterRole", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "rbac.authorization.k8s.io/v1",
					"kind":       "ClusterRole",
					"metadata": map[string]interface{}{
						"name": "admin",
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Namespace).To(Equal("_cluster"))
			Expect(data.Kind).To(Equal("ClusterRole"))
		})

		It("should extract labels", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app":     "nginx",
							"env":     "prod",
							"version": "1.0",
						},
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Labels).To(HaveLen(3))
			Expect(data.Labels["app"]).To(Equal("nginx"))
			Expect(data.Labels["env"]).To(Equal("prod"))
			Expect(data.Labels["version"]).To(Equal("1.0"))
		})

		It("should handle missing labels", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Labels).NotTo(BeNil())
			Expect(data.Labels).To(BeEmpty())
		})

		It("should extract description annotations", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Service",
					"metadata": map[string]interface{}{
						"name":      "my-service",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"description":                       "Main web service",
							"service.kubernetes.io/description": "Backend API",
						},
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Annotations).To(HaveLen(2))
			Expect(data.Annotations["description"]).To(Equal("Main web service"))
			Expect(data.Annotations["service.kubernetes.io/description"]).To(Equal("Backend API"))
		})

		It("should skip kubectl last-applied-configuration annotation", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "my-config",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"kubectl.kubernetes.io/last-applied-configuration": "{\"very\":\"large\",\"json\":\"blob\"}",
							"description": "My config",
						},
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Annotations).NotTo(HaveKey("kubectl.kubernetes.io/last-applied-configuration"))
			Expect(data.Annotations).To(HaveKey("description"))
		})

		It("should skip helm annotations", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Secret",
					"metadata": map[string]interface{}{
						"name":      "my-secret",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"meta.helm.sh/release-name":      "my-release",
							"meta.helm.sh/release-namespace": "default",
							"description":                    "Database credentials",
						},
					},
				},
			}

			data := extractResourceData(obj)
			Expect(data.Annotations).NotTo(HaveKey("meta.helm.sh/release-name"))
			Expect(data.Annotations).NotTo(HaveKey("meta.helm.sh/release-namespace"))
			Expect(data.Annotations).To(HaveKey("description"))
		})

		It("should set UpdatedAt to current time", func() {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}

			before := time.Now()
			data := extractResourceData(obj)
			after := time.Now()

			Expect(data.UpdatedAt).To(BeTemporally(">=", before))
			Expect(data.UpdatedAt).To(BeTemporally("<=", after))
		})

		It("should copy labels to avoid mutation", func() {
			originalLabels := map[string]interface{}{
				"app": "nginx",
			}
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels":    originalLabels,
					},
				},
			}

			data := extractResourceData(obj)
			data.Labels["modified"] = "true"

			// Original should not be modified
			Expect(obj.GetLabels()).NotTo(HaveKey("modified"))
		})
	})

	Describe("hasRelevantChanges", func() {
		It("should detect label changes", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
						},
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
							"env": "prod", // Added label
						},
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeTrue())
		})

		It("should detect label removal", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
							"env": "prod",
						},
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
							// env removed
						},
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeTrue())
		})

		It("should detect label value changes", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"version": "1.0",
						},
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"version": "2.0", // Changed
						},
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeTrue())
		})

		It("should NOT detect status changes (status not synced)", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "nginx",
						"namespace": "default",
					},
					"status": map[string]interface{}{
						"availableReplicas": int64(2),
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "nginx",
						"namespace": "default",
					},
					"status": map[string]interface{}{
						"availableReplicas": int64(3), // Changed - but should NOT trigger sync
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should return false when nothing changed", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
						},
					},
					"status": map[string]interface{}{
						"phase": "Running",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
						},
					},
					"status": map[string]interface{}{
						"phase": "Running",
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should ignore resourceVersion changes", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":            "my-pod",
						"namespace":       "default",
						"resourceVersion": "12345",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":            "my-pod",
						"namespace":       "default",
						"resourceVersion": "12346", // Changed
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should ignore annotation changes (except description)", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"some-annotation": "old-value",
						},
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"some-annotation": "new-value", // Changed
						},
					},
				},
			}

			// Annotations are not part of change detection (only labels and status)
			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should handle nil labels", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should detect when labels added to previously nil labels", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
						},
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeTrue())
		})

		It("should handle nil status", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "my-config",
						"namespace": "default",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "my-config",
						"namespace": "default",
					},
				},
			}

			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})

		It("should NOT detect when status added to previously nil status (status not synced)", func() {
			oldObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
				},
			}
			newObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod",
						"namespace": "default",
					},
					"status": map[string]interface{}{
						"phase": "Running",
					},
				},
			}

			// Status changes should NOT trigger sync - only labels matter
			Expect(hasRelevantChanges(oldObj, newObj)).To(BeFalse())
		})
	})

	Describe("Event Handlers", func() {
		var (
			state *activeConfigState
		)

		BeforeEach(func() {
			state = &activeConfigState{
				changeQueue: make(chan *ResourceChange, 100),
			}
		})

		Describe("makeOnAdd", func() {
			It("should queue upsert for new resource", func() {
				handler := reconciler.makeOnAdd(state)

				obj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "nginx",
							"namespace": "default",
						},
					},
				}

				handler(obj)

				// Should have queued a change
				select {
				case change := <-state.changeQueue:
					Expect(change.Action).To(Equal(ActionUpsert))
					Expect(change.ID).To(Equal("default:apps/v1:Deployment:nginx"))
					Expect(change.Data).NotTo(BeNil())
					Expect(change.Data.Name).To(Equal("nginx"))
				case <-time.After(100 * time.Millisecond):
					Fail("Expected change to be queued")
				}
			})

			It("should handle non-unstructured objects gracefully", func() {
				handler := reconciler.makeOnAdd(state)

				// Pass a non-unstructured object
				handler("not an unstructured object")

				// Should not queue anything
				select {
				case <-state.changeQueue:
					Fail("Should not queue for non-unstructured object")
				case <-time.After(50 * time.Millisecond):
					// Expected - no change queued
				}
			})
		})

		Describe("makeOnUpdate", func() {
			It("should NOT queue upsert when only status changes (status not synced)", func() {
				handler := reconciler.makeOnUpdate(state)

				oldObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "nginx",
							"namespace": "default",
						},
						"status": map[string]interface{}{
							"readyReplicas": int64(2),
						},
					},
				}
				newObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "nginx",
							"namespace": "default",
						},
						"status": map[string]interface{}{
							"readyReplicas": int64(3), // Changed - but should NOT trigger sync
						},
					},
				}

				handler(oldObj, newObj)

				// Status changes should NOT queue anything
				select {
				case <-state.changeQueue:
					Fail("Should not queue when only status changed")
				case <-time.After(50 * time.Millisecond):
					// Expected - no change queued
				}
			})

			It("should not queue when nothing relevant changed", func() {
				handler := reconciler.makeOnUpdate(state)

				oldObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":            "nginx",
							"namespace":       "default",
							"resourceVersion": "100",
						},
					},
				}
				newObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":            "nginx",
							"namespace":       "default",
							"resourceVersion": "101", // Only resourceVersion changed
						},
					},
				}

				handler(oldObj, newObj)

				select {
				case <-state.changeQueue:
					Fail("Should not queue when nothing relevant changed")
				case <-time.After(50 * time.Millisecond):
					// Expected - no change queued
				}
			})

			It("should queue upsert when labels change", func() {
				handler := reconciler.makeOnUpdate(state)

				oldObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "my-pod",
							"namespace": "default",
							"labels": map[string]interface{}{
								"app": "nginx",
							},
						},
					},
				}
				newObj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "my-pod",
							"namespace": "default",
							"labels": map[string]interface{}{
								"app": "nginx",
								"env": "prod", // Added label
							},
						},
					},
				}

				handler(oldObj, newObj)

				select {
				case change := <-state.changeQueue:
					Expect(change.Action).To(Equal(ActionUpsert))
				case <-time.After(100 * time.Millisecond):
					Fail("Expected change to be queued for label change")
				}
			})
		})

		Describe("makeOnDelete", func() {
			It("should queue delete for removed resource", func() {
				handler := reconciler.makeOnDelete(state)

				obj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "nginx",
							"namespace": "default",
						},
					},
				}

				handler(obj)

				select {
				case change := <-state.changeQueue:
					Expect(change.Action).To(Equal(ActionDelete))
					Expect(change.ID).To(Equal("default:apps/v1:Deployment:nginx"))
					Expect(change.Data).To(BeNil())
				case <-time.After(100 * time.Millisecond):
					Fail("Expected delete to be queued")
				}
			})

			It("should handle DeletedFinalStateUnknown", func() {
				handler := reconciler.makeOnDelete(state)

				obj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "my-pod",
							"namespace": "kube-system",
						},
					},
				}

				// Wrap in DeletedFinalStateUnknown (simulates missed delete event)
				deletedObj := cache.DeletedFinalStateUnknown{
					Key: "kube-system/my-pod",
					Obj: obj,
				}

				handler(deletedObj)

				select {
				case change := <-state.changeQueue:
					Expect(change.Action).To(Equal(ActionDelete))
					Expect(change.ID).To(Equal("kube-system:v1:Pod:my-pod"))
				case <-time.After(100 * time.Millisecond):
					Fail("Expected delete to be queued from DeletedFinalStateUnknown")
				}
			})

			It("should use _cluster namespace for cluster-scoped resource deletion", func() {
				handler := reconciler.makeOnDelete(state)

				obj := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Node",
						"metadata": map[string]interface{}{
							"name": "worker-1",
							// No namespace - cluster-scoped
						},
					},
				}

				handler(obj)

				select {
				case change := <-state.changeQueue:
					Expect(change.Action).To(Equal(ActionDelete))
					Expect(change.ID).To(Equal("_cluster:v1:Node:worker-1"))
					Expect(change.DeleteIdentifier).NotTo(BeNil())
					Expect(change.DeleteIdentifier.Namespace).To(Equal("_cluster"))
					Expect(change.DeleteIdentifier.Name).To(Equal("worker-1"))
					Expect(change.DeleteIdentifier.Kind).To(Equal("Node"))
				case <-time.After(100 * time.Millisecond):
					Fail("Expected delete to be queued for cluster-scoped resource")
				}
			})
		})

		Describe("queue behavior", func() {
			It("should drop events when queue is full", func() {
				// Create a state with a tiny queue
				tinyState := &activeConfigState{
					changeQueue: make(chan *ResourceChange, 1),
				}

				handler := reconciler.makeOnAdd(tinyState)

				// First event should succeed
				handler(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "pod-1",
							"namespace": "default",
						},
					},
				})

				// Second event should be dropped (queue is full)
				handler(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Pod",
						"metadata": map[string]interface{}{
							"name":      "pod-2",
							"namespace": "default",
						},
					},
				})

				// Only first event should be in queue
				select {
				case change := <-tinyState.changeQueue:
					Expect(change.Data.Name).To(Equal("pod-1"))
				default:
					Fail("Expected at least one event in queue")
				}

				// Queue should now be empty
				select {
				case <-tinyState.changeQueue:
					Fail("Queue should be empty after draining")
				default:
					// Expected
				}
			})
		})
	})

	Describe("Resync Functions", func() {
		Describe("listAllResources", func() {
			It("should return empty slice when no informers exist", func() {
				state := &activeConfigState{
					activeInformers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
				}

				resources := reconciler.listAllResources(state)
				Expect(resources).To(BeEmpty())
			})

			It("should skip CRD informer", func() {
				// Create a mock informer with a CRD
				mockStore := &mockStore{
					items: []interface{}{
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "apiextensions.k8s.io/v1",
								"kind":       "CustomResourceDefinition",
								"metadata": map[string]interface{}{
									"name": "test.example.com",
								},
							},
						},
					},
				}
				mockInformer := &mockInformer{store: mockStore}

				state := &activeConfigState{
					activeInformers: map[schema.GroupVersionResource]cache.SharedIndexInformer{
						crdGVR: mockInformer,
					},
				}

				resources := reconciler.listAllResources(state)
				Expect(resources).To(BeEmpty())
			})

			It("should extract resources from informers", func() {
				// Create mock store with test resources
				mockStore := &mockStore{
					items: []interface{}{
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "apps/v1",
								"kind":       "Deployment",
								"metadata": map[string]interface{}{
									"name":      "nginx",
									"namespace": "default",
								},
								"status": map[string]interface{}{
									"readyReplicas": int64(3),
								},
							},
						},
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "apps/v1",
								"kind":       "Deployment",
								"metadata": map[string]interface{}{
									"name":      "redis",
									"namespace": "default",
								},
								"status": map[string]interface{}{
									"readyReplicas": int64(1),
								},
							},
						},
					},
				}
				mockInformer := &mockInformer{store: mockStore}

				deploymentsGVR := schema.GroupVersionResource{
					Group:    "apps",
					Version:  "v1",
					Resource: "deployments",
				}

				state := &activeConfigState{
					activeInformers: map[schema.GroupVersionResource]cache.SharedIndexInformer{
						deploymentsGVR: mockInformer,
					},
				}

				resources := reconciler.listAllResources(state)
				Expect(resources).To(HaveLen(2))

				// Verify extracted data
				names := []string{}
				for _, r := range resources {
					names = append(names, r.Name)
					Expect(r.Kind).To(Equal("Deployment"))
					Expect(r.APIVersion).To(Equal("apps/v1"))
					Expect(r.Namespace).To(Equal("default"))
				}
				Expect(names).To(ContainElements("nginx", "redis"))
			})

			It("should aggregate resources from multiple informers", func() {
				// Deployments store
				deploymentsStore := &mockStore{
					items: []interface{}{
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "apps/v1",
								"kind":       "Deployment",
								"metadata": map[string]interface{}{
									"name":      "nginx",
									"namespace": "default",
								},
							},
						},
					},
				}

				// Pods store
				podsStore := &mockStore{
					items: []interface{}{
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "v1",
								"kind":       "Pod",
								"metadata": map[string]interface{}{
									"name":      "nginx-abc123",
									"namespace": "default",
								},
							},
						},
						&unstructured.Unstructured{
							Object: map[string]interface{}{
								"apiVersion": "v1",
								"kind":       "Pod",
								"metadata": map[string]interface{}{
									"name":      "nginx-def456",
									"namespace": "default",
								},
							},
						},
					},
				}

				state := &activeConfigState{
					activeInformers: map[schema.GroupVersionResource]cache.SharedIndexInformer{
						{Group: "apps", Version: "v1", Resource: "deployments"}: &mockInformer{store: deploymentsStore},
						{Group: "", Version: "v1", Resource: "pods"}:            &mockInformer{store: podsStore},
					},
				}

				resources := reconciler.listAllResources(state)
				Expect(resources).To(HaveLen(3))

				// Count by kind
				kinds := make(map[string]int)
				for _, r := range resources {
					kinds[r.Kind]++
				}
				Expect(kinds["Deployment"]).To(Equal(1))
				Expect(kinds["Pod"]).To(Equal(2))
			})
		})

		Describe("performResync", func() {
			It("should skip resync when MCP client is nil", func() {
				state := &activeConfigState{
					mcpClient:       nil,
					activeInformers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
				}

				count, err := reconciler.performResync(testCtx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(count).To(Equal(0))
			})

			It("should skip resync when no resources exist", func() {
				state := &activeConfigState{
					mcpClient: &MCPResourceSyncClient{
						endpoint: "http://test.example.com",
					},
					activeInformers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
				}

				count, err := reconciler.performResync(testCtx, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(count).To(Equal(0))
			})
		})
	})
})

// Mock implementations for testing

// mockStore implements cache.Store for testing
type mockStore struct {
	items []interface{}
}

func (s *mockStore) Add(obj interface{}) error    { return nil }
func (s *mockStore) Update(obj interface{}) error { return nil }
func (s *mockStore) Delete(obj interface{}) error { return nil }
func (s *mockStore) List() []interface{}          { return s.items }
func (s *mockStore) ListKeys() []string           { return nil }
func (s *mockStore) Get(obj interface{}) (item interface{}, exists bool, err error) {
	return nil, false, nil
}
func (s *mockStore) GetByKey(key string) (item interface{}, exists bool, err error) {
	return nil, false, nil
}
func (s *mockStore) Replace([]interface{}, string) error { return nil }
func (s *mockStore) Resync() error                       { return nil }

// mockInformer implements cache.SharedIndexInformer for testing
type mockInformer struct {
	store cache.Store
}

func (m *mockInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}
func (m *mockInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}
func (m *mockInformer) AddEventHandlerWithOptions(handler cache.ResourceEventHandler, options cache.HandlerOptions) (cache.ResourceEventHandlerRegistration, error) {
	return nil, nil
}
func (m *mockInformer) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error {
	return nil
}
func (m *mockInformer) GetStore() cache.Store {
	return m.store
}
func (m *mockInformer) GetController() cache.Controller {
	return nil
}
func (m *mockInformer) Run(stopCh <-chan struct{})         {}
func (m *mockInformer) RunWithContext(ctx context.Context) {}
func (m *mockInformer) HasSynced() bool {
	return true
}
func (m *mockInformer) LastSyncResourceVersion() string {
	return ""
}
func (m *mockInformer) SetWatchErrorHandler(handler cache.WatchErrorHandler) error {
	return nil
}
func (m *mockInformer) SetWatchErrorHandlerWithContext(handler cache.WatchErrorHandlerWithContext) error {
	return nil
}
func (m *mockInformer) SetTransform(handler cache.TransformFunc) error {
	return nil
}
func (m *mockInformer) IsStopped() bool {
	return false
}
func (m *mockInformer) AddIndexers(indexers cache.Indexers) error {
	return nil
}
func (m *mockInformer) GetIndexer() cache.Indexer {
	return nil
}

// Helper function to generate random strings for unique test names
func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// Note: rand.Seed is deprecated in Go 1.20+; the global source is auto-seeded.
// No explicit seeding needed.
