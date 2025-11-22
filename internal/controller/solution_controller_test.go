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

package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

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
})
