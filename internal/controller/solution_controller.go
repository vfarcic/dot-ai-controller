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
	"fmt"
	"math"
	"math/rand"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// SolutionReconciler reconciles a Solution object
type SolutionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=solutions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=solutions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=solutions/finalizers,verbs=update

// Reconcile reconciles a Solution custom resource
// It updates the status based on the current state of child resources
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *SolutionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("solution", req.NamespacedName)

	// Fetch the Solution CR
	var solution dotaiv1alpha1.Solution
	if err := r.Get(ctx, req.NamespacedName, &solution); err != nil {
		if apierrors.IsNotFound(err) {
			// Solution was deleted - nothing to do
			logger.V(1).Info("Solution not found - likely deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "failed to get Solution")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling Solution",
		"intent", solution.Spec.Intent,
		"resources", len(solution.Spec.Resources),
		"observedGeneration", solution.Status.ObservedGeneration,
		"currentGeneration", solution.Generation,
	)

	// Initialize status if this is a new Solution
	if solution.Status.ObservedGeneration == 0 {
		logger.Info("Initializing status for new Solution")
		if err := r.initializeSolutionStatus(ctx, &solution); err != nil {
			logger.Error(err, "failed to initialize Solution status")
			r.Recorder.Eventf(&solution, "Warning", "StatusInitializationFailed",
				"Failed to initialize status: %v", err)
			return ctrl.Result{}, err
		}
		r.Recorder.Event(&solution, "Normal", "SolutionCreated",
			"Solution created and ready to track resources")
		return ctrl.Result{}, nil
	}

	// Update status
	if err := r.updateSolutionStatus(ctx, &solution); err != nil {
		logger.Error(err, "failed to update Solution status")
		r.Recorder.Eventf(&solution, "Warning", "StatusUpdateFailed",
			"Failed to update status: %v", err)
		return ctrl.Result{}, err
	}

	logger.Info("✅ Solution reconciled successfully",
		"state", solution.Status.State,
		"totalResources", solution.Status.Resources.Total,
	)

	// Requeue periodically for status updates
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// initializeSolutionStatus initializes the status for a new Solution
func (r *SolutionReconciler) initializeSolutionStatus(ctx context.Context, solution *dotaiv1alpha1.Solution) error {
	logger := logf.FromContext(ctx)

	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.Solution{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(solution), fresh); err != nil {
		return fmt.Errorf("failed to fetch fresh Solution: %w", err)
	}

	// Initialize status fields
	fresh.Status.State = "deployed"
	fresh.Status.ObservedGeneration = fresh.Generation
	fresh.Status.Resources = dotaiv1alpha1.ResourceSummary{
		Total:  len(fresh.Spec.Resources),
		Ready:  0, // Will be updated in Milestone 2 with actual health checks
		Failed: 0,
	}

	// Set initial Ready condition
	now := metav1.NewTime(time.Now())
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "SolutionCreated",
		Message:            fmt.Sprintf("Solution tracking %d resources", len(fresh.Spec.Resources)),
	}
	fresh.Status.Conditions = []metav1.Condition{readyCondition}

	// Update status subresource
	if err := r.Status().Update(ctx, fresh); err != nil {
		return fmt.Errorf("failed to initialize status: %w", err)
	}

	logger.Info("✅ Solution status initialized",
		"state", fresh.Status.State,
		"totalResources", fresh.Status.Resources.Total,
	)

	return nil
}

// updateSolutionStatus updates the Solution status with retry logic
func (r *SolutionReconciler) updateSolutionStatus(ctx context.Context, solution *dotaiv1alpha1.Solution) error {
	logger := logf.FromContext(ctx)

	// Retry configuration
	maxRetries := 3
	baseDelay := 100 * time.Millisecond
	maxDelay := 1 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter
			delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt-1)))
			if delay > maxDelay {
				delay = maxDelay
			}
			// Add jitter (±25%)
			jitter := time.Duration(float64(delay) * 0.25 * (2*rand.Float64() - 1))
			delay += jitter
			time.Sleep(delay)
		}

		// Fetch fresh copy to avoid conflicts
		fresh := &dotaiv1alpha1.Solution{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(solution), fresh); err != nil {
			lastErr = fmt.Errorf("failed to fetch fresh Solution: %w", err)
			continue
		}

		// Update observedGeneration to match current generation
		fresh.Status.ObservedGeneration = fresh.Generation

		// Basic status update (Milestone 1)
		// In Milestone 2, we'll add actual resource health checking
		fresh.Status.State = "deployed"
		fresh.Status.Resources.Total = len(fresh.Spec.Resources)

		// Update Ready condition
		now := metav1.NewTime(time.Now())
		readyCondition := metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "SolutionReconciled",
			Message:            fmt.Sprintf("Solution tracking %d resources", len(fresh.Spec.Resources)),
		}

		// Update existing Ready condition or append new one
		updated := false
		for i, condition := range fresh.Status.Conditions {
			if condition.Type == "Ready" {
				fresh.Status.Conditions[i] = readyCondition
				updated = true
				break
			}
		}
		if !updated {
			fresh.Status.Conditions = append(fresh.Status.Conditions, readyCondition)
		}

		// Update status subresource with retry logic
		if err := r.Status().Update(ctx, fresh); err != nil {
			if apierrors.IsConflict(err) {
				// Resource version conflict - retry with fresh copy
				lastErr = fmt.Errorf("resource conflict (attempt %d/%d): %w", attempt+1, maxRetries+1, err)
				logger.V(1).Info("Status update conflict, retrying",
					"attempt", attempt+1,
					"maxRetries", maxRetries+1)
				continue
			}
			// Non-conflict error - don't retry
			return fmt.Errorf("failed to update status: %w", err)
		}

		// Success!
		return nil
	}

	// All retries exhausted
	return fmt.Errorf("failed to update status after %d attempts: %w", maxRetries+1, lastErr)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SolutionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotaiv1alpha1.Solution{}).
		Named("solution").
		Complete(r)
}
