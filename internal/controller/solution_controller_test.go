package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// Test helper types
type unstructuredDeployment = unstructured.Unstructured
type unstructuredService = unstructured.Unstructured
type unstructuredConfigMap = unstructured.Unstructured

// Helper functions for creating test resources

func createUnstructuredDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"app": name,
					},
				},
				"template": map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app": name,
						},
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{
								"name":  "nginx",
								"image": "nginx:latest",
							},
						},
					},
				},
			},
		},
	}
}

func createUnstructuredService(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"app": name,
				},
				"ports": []interface{}{
					map[string]interface{}{
						"protocol": "TCP",
						"port":     80,
					},
				},
			},
		},
	}
}

func createUnstructuredConfigMap(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"data": map[string]interface{}{
				"key": "value",
			},
		},
	}
}

func setDeploymentAvailable(deployment *unstructured.Unstructured, available bool) {
	status := "True"
	if !available {
		status = "False"
	}

	_ = unstructured.SetNestedSlice(deployment.Object, []interface{}{
		map[string]interface{}{
			"type":               "Available",
			"status":             status,
			"lastTransitionTime": metav1.Now().Format(time.RFC3339),
			"reason":             "TestCondition",
			"message":            "Test deployment condition",
		},
	}, "status", "conditions")
}

var _ = Describe("Solution Controller", func() {
	var (
		reconciler *SolutionReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &SolutionReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(100),
		}
	})

	Describe("Basic Reconciliation", func() {
		It("should initialize status for a new Solution", func() {
			// Create a test Solution
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-init",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test solution for initialization",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "Service",
							Name:       "test-service",
							Namespace:  "default",
						},
					},
				},
			}

			// Create the Solution in the cluster
			Expect(k8sClient.Create(ctx, solution)).To(Succeed())

			// Defer cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliation
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Fetch the Solution and check status
			var updatedSolution dotaiv1alpha1.Solution
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return false
				}
				return updatedSolution.Status.ObservedGeneration > 0
			}, 5*time.Second, 500*time.Millisecond).Should(BeTrue())

			// Verify status fields
			Expect(updatedSolution.Status.State).To(Equal("deployed"))
			Expect(updatedSolution.Status.ObservedGeneration).To(Equal(updatedSolution.Generation))
			Expect(updatedSolution.Status.Resources.Total).To(Equal(2))
			Expect(updatedSolution.Status.Resources.Ready).To(Equal(0))
			Expect(updatedSolution.Status.Resources.Failed).To(Equal(0))

			// Verify Ready condition exists
			Expect(updatedSolution.Status.Conditions).NotTo(BeEmpty())
			var readyCondition *metav1.Condition
			for i := range updatedSolution.Status.Conditions {
				if updatedSolution.Status.Conditions[i].Type == "Ready" {
					readyCondition = &updatedSolution.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("SolutionCreated"))
		})

		It("should update observedGeneration on subsequent reconciliations", func() {
			// Create a test Solution
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-update",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test solution for updates",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "v1",
							Kind:       "ConfigMap",
							Name:       "test-config",
							Namespace:  "default",
						},
					},
				},
			}

			// Create the Solution in the cluster
			Expect(k8sClient.Create(ctx, solution)).To(Succeed())

			// Defer cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger first reconciliation
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Wait for status initialization
			var updatedSolution dotaiv1alpha1.Solution
			Eventually(func() bool {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return false
				}
				return updatedSolution.Status.ObservedGeneration > 0
			}, 5*time.Second, 500*time.Millisecond).Should(BeTrue())

			initialGeneration := updatedSolution.Status.ObservedGeneration

			// Update the Solution spec to trigger generation increment
			updatedSolution.Spec.Intent = "Updated intent"
			Expect(k8sClient.Update(ctx, &updatedSolution)).To(Succeed())

			// Trigger second reconciliation
			_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify observedGeneration was updated
			Eventually(func() int64 {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return 0
				}
				return updatedSolution.Status.ObservedGeneration
			}, 5*time.Second, 500*time.Millisecond).Should(BeNumerically(">", initialGeneration))
		})

		It("should handle Solution deletion gracefully", func() {
			// Create a test Solution
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-delete",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test solution for deletion",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "v1",
							Kind:       "Secret",
							Name:       "test-secret",
							Namespace:  "default",
						},
					},
				},
			}

			// Create the Solution in the cluster
			Expect(k8sClient.Create(ctx, solution)).To(Succeed())

			// Trigger reconciliation
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Delete the Solution
			Expect(k8sClient.Delete(ctx, solution)).To(Succeed())

			// Trigger reconciliation after deletion
			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Requeue).To(BeFalse())

			// Verify Solution is deleted
			var deletedSolution dotaiv1alpha1.Solution
			err = k8sClient.Get(ctx, namespacedName, &deletedSolution)
			Expect(err).To(HaveOccurred())
		})

		It("should track correct resource count", func() {
			// Create a test Solution with multiple resources
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-count",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test solution with multiple resources",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "app-deployment",
							Namespace:  "default",
						},
						{
							APIVersion: "apps/v1",
							Kind:       "StatefulSet",
							Name:       "db-statefulset",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "Service",
							Name:       "app-service",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "ConfigMap",
							Name:       "app-config",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "Secret",
							Name:       "app-secrets",
							Namespace:  "default",
						},
					},
				},
			}

			// Create the Solution in the cluster
			Expect(k8sClient.Create(ctx, solution)).To(Succeed())

			// Defer cleanup
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliation
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Fetch the Solution and check status
			var updatedSolution dotaiv1alpha1.Solution
			Eventually(func() int {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return 0
				}
				return updatedSolution.Status.Resources.Total
			}, 5*time.Second, 500*time.Millisecond).Should(Equal(5))

			// Verify all status fields
			Expect(updatedSolution.Status.State).To(Equal("deployed"))
			Expect(updatedSolution.Status.ObservedGeneration).To(Equal(updatedSolution.Generation))
		})
	})

	Describe("Resource Tracking (Milestone 2)", func() {
		It("should add ownerReferences to child resources", func() {
			// Create child resources first
			deployment := createUnstructuredDeployment("test-deployment-owned", "default")
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, deployment)
			}()

			service := createUnstructuredService("test-service-owned", "default")
			Expect(k8sClient.Create(ctx, service)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, service)
			}()

			// Create Solution referencing these resources
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-ownership",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test ownerReference management",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment-owned",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "Service",
							Name:       "test-service-owned",
							Namespace:  "default",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, solution)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliation twice (first initializes, second adds ownerRefs)
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify ownerReferences were added to child resources
			Eventually(func() bool {
				updatedDeployment := &unstructured.Unstructured{}
				updatedDeployment.SetGroupVersionKind(deployment.GroupVersionKind())
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-deployment-owned", Namespace: "default"}, updatedDeployment); err != nil {
					return false
				}
				owners := updatedDeployment.GetOwnerReferences()
				for _, owner := range owners {
					if owner.Kind == "Solution" && owner.Name == solution.Name {
						return true
					}
				}
				return false
			}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

			// Verify service also has ownerReference
			Eventually(func() int {
				updatedService := &unstructured.Unstructured{}
				updatedService.SetGroupVersionKind(service.GroupVersionKind())
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-service-owned", Namespace: "default"}, updatedService); err != nil {
					return 0
				}
				return len(updatedService.GetOwnerReferences())
			}, 10*time.Second, 500*time.Millisecond).Should(BeNumerically(">", 0))
		})

		It("should track resource health correctly", func() {
			// Create a Deployment with Available condition
			deployment := createUnstructuredDeployment("test-deployment-ready", "default")
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, deployment)
			}()

			// Update status to mark it as available (status is a subresource)
			setDeploymentAvailable(deployment, true)
			Expect(k8sClient.Status().Update(ctx, deployment)).To(Succeed())

			// Create a ConfigMap (always ready when exists)
			configMap := createUnstructuredConfigMap("test-config-ready", "default")
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, configMap)
			}()

			// Create Solution
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-health",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test resource health checking",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment-ready",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "ConfigMap",
							Name:       "test-config-ready",
							Namespace:  "default",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, solution)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliation
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}

			// First reconciliation initializes
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Second reconciliation checks health
			_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify status reflects health
			var updatedSolution dotaiv1alpha1.Solution
			Eventually(func() int {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return -1
				}
				return updatedSolution.Status.Resources.Ready
			}, 10*time.Second, 500*time.Millisecond).Should(Equal(2))

			Expect(updatedSolution.Status.Resources.Total).To(Equal(2))
			Expect(updatedSolution.Status.Resources.Failed).To(Equal(0))
			Expect(updatedSolution.Status.State).To(Equal("deployed"))

			// Verify Ready condition
			var readyCondition *metav1.Condition
			for i := range updatedSolution.Status.Conditions {
				if updatedSolution.Status.Conditions[i].Type == "Ready" {
					readyCondition = &updatedSolution.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal("AllResourcesReady"))
		})

		It("should detect unhealthy resources", func() {
			// Create a Deployment that is NOT available
			deployment := createUnstructuredDeployment("test-deployment-unhealthy", "default")
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, deployment)
			}()

			// Update status to mark it as NOT available (status is a subresource)
			setDeploymentAvailable(deployment, false)
			Expect(k8sClient.Status().Update(ctx, deployment)).To(Succeed())

			// Create Solution
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-unhealthy",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test unhealthy resource detection",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment-unhealthy",
							Namespace:  "default",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, solution)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliations
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify status shows degraded state
			var updatedSolution dotaiv1alpha1.Solution
			Eventually(func() string {
				if err := k8sClient.Get(ctx, namespacedName, &updatedSolution); err != nil {
					return ""
				}
				return updatedSolution.Status.State
			}, 10*time.Second, 500*time.Millisecond).Should(Equal("degraded"))

			Expect(updatedSolution.Status.Resources.Total).To(Equal(1))
			Expect(updatedSolution.Status.Resources.Ready).To(Equal(0))
			Expect(updatedSolution.Status.Resources.Failed).To(Equal(1))

			// Verify Ready condition is False
			var readyCondition *metav1.Condition
			for i := range updatedSolution.Status.Conditions {
				if updatedSolution.Status.Conditions[i].Type == "Ready" {
					readyCondition = &updatedSolution.Status.Conditions[i]
					break
				}
			}
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCondition.Reason).To(Equal("ResourcesNotReady"))
		})

		It("should set ownerReferences for garbage collection", func() {
			// Create child resources
			deployment := createUnstructuredDeployment("test-deployment-gc", "default")
			Expect(k8sClient.Create(ctx, deployment)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, deployment)
			}()

			configMap := createUnstructuredConfigMap("test-config-gc", "default")
			Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, configMap)
			}()

			// Create Solution referencing these resources
			solution := &dotaiv1alpha1.Solution{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-solution-gc",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.SolutionSpec{
					Intent: "Test garbage collection setup",
					Resources: []dotaiv1alpha1.ResourceReference{
						{
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Name:       "test-deployment-gc",
							Namespace:  "default",
						},
						{
							APIVersion: "v1",
							Kind:       "ConfigMap",
							Name:       "test-config-gc",
							Namespace:  "default",
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, solution)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, solution)
			}()

			// Trigger reconciliations to add ownerReferences
			namespacedName := types.NamespacedName{
				Name:      solution.Name,
				Namespace: solution.Namespace,
			}
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify ownerReferences were added with correct settings for GC
			Eventually(func() bool {
				updatedDeployment := &unstructured.Unstructured{}
				updatedDeployment.SetGroupVersionKind(deployment.GroupVersionKind())
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-deployment-gc", Namespace: "default"}, updatedDeployment); err != nil {
					return false
				}
				owners := updatedDeployment.GetOwnerReferences()
				for _, owner := range owners {
					if owner.Kind == "Solution" &&
						owner.Name == solution.Name &&
						*owner.BlockOwnerDeletion &&
						!*owner.Controller {
						return true
					}
				}
				return false
			}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

			// Verify ConfigMap also has correct ownerReference
			Eventually(func() bool {
				updatedConfigMap := &unstructured.Unstructured{}
				updatedConfigMap.SetGroupVersionKind(configMap.GroupVersionKind())
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-config-gc", Namespace: "default"}, updatedConfigMap); err != nil {
					return false
				}
				owners := updatedConfigMap.GetOwnerReferences()
				for _, owner := range owners {
					if owner.Kind == "Solution" &&
						owner.Name == solution.Name &&
						*owner.BlockOwnerDeletion &&
						!*owner.Controller {
						return true
					}
				}
				return false
			}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

			// Note: Actual garbage collection behavior cannot be tested in envtest as the
			// Kubernetes GC controller doesn't run here. Since Controller is set to false,
			// deleting the Solution will NOT delete tracked resources in a real cluster.
		})
	})
})
