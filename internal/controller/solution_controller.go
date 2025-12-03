package controller

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
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
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;update;patch
// NOTE: Wildcard RBAC allows tracking any resource type out-of-the-box.
// For production, consider restricting to specific resource types in your deployment.

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

	// Phase 1: Ensure all resources have ownerReferences
	logger.Info("Ensuring resource ownership")
	if err := r.ensureResourceOwnership(ctx, &solution); err != nil {
		logger.Error(err, "failed to ensure resource ownership")
		// Don't fail reconciliation - we'll report issues in status
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

	// Requeue periodically for status updates (every 30 seconds)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
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

		// Phase 2: Check resource health and update status
		totalResources := len(fresh.Spec.Resources)
		readyCount := 0
		failedCount := 0

		for _, ref := range fresh.Spec.Resources {
			// Use Solution's namespace if resource namespace not specified
			if ref.Namespace == "" {
				ref.Namespace = fresh.Namespace
			}

			// Fetch the resource
			resource, err := r.getResource(ctx, ref)
			if err != nil {
				if apierrors.IsNotFound(err) {
					logger.V(1).Info("Resource not found, counting as failed",
						"kind", ref.Kind,
						"name", ref.Name)
					failedCount++
					continue
				}
				// Other errors (permissions, etc) - don't count as failed, just skip
				logger.V(1).Info("Skipping resource health check",
					"kind", ref.Kind,
					"name", ref.Name,
					"error", err.Error())
				continue
			}

			// Check resource health
			ready, reason := r.checkResourceHealth(resource)
			if ready {
				readyCount++
				logger.V(1).Info("Resource is ready",
					"kind", ref.Kind,
					"name", ref.Name,
					"reason", reason)
			} else {
				failedCount++
				logger.V(1).Info("Resource is not ready",
					"kind", ref.Kind,
					"name", ref.Name,
					"reason", reason)
			}
		}

		// Update resource summary
		fresh.Status.Resources.Total = totalResources
		fresh.Status.Resources.Ready = readyCount
		fresh.Status.Resources.Failed = failedCount

		// Determine overall state
		if failedCount == 0 && readyCount == totalResources {
			fresh.Status.State = "deployed"
		} else if failedCount > 0 {
			fresh.Status.State = "degraded"
		} else {
			fresh.Status.State = "pending"
		}

		// Update Ready condition
		now := metav1.NewTime(time.Now())
		var readyCondition metav1.Condition

		if fresh.Status.State == "deployed" {
			readyCondition = metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             "AllResourcesReady",
				Message:            fmt.Sprintf("All %d resources are ready", readyCount),
			}
		} else {
			readyCondition = metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				LastTransitionTime: now,
				Reason:             "ResourcesNotReady",
				Message:            fmt.Sprintf("Ready: %d/%d, Failed: %d", readyCount, totalResources, failedCount),
			}
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

// getResource fetches an arbitrary Kubernetes resource using unstructured client
func (r *SolutionReconciler) getResource(ctx context.Context, ref dotaiv1alpha1.ResourceReference) (*unstructured.Unstructured, error) {
	// Parse APIVersion into GroupVersion
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid apiVersion %q: %w", ref.APIVersion, err)
	}

	// Create GroupVersionKind
	gvk := schema.GroupVersionKind{
		Group:   gv.Group,
		Version: gv.Version,
		Kind:    ref.Kind,
	}

	// Create unstructured object with GVK
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	// Create object key
	key := client.ObjectKey{
		Name:      ref.Name,
		Namespace: ref.Namespace,
	}

	// Fetch the resource
	if err := r.Get(ctx, key, obj); err != nil {
		return nil, err
	}

	return obj, nil
}

// ensureOwnerReference adds an ownerReference to a child resource if not already present
func (r *SolutionReconciler) ensureOwnerReference(ctx context.Context, solution *dotaiv1alpha1.Solution, resource *unstructured.Unstructured) error {
	// Check if ownerReference already exists
	owners := resource.GetOwnerReferences()
	for _, owner := range owners {
		if owner.UID == solution.UID {
			// Already owned by this Solution
			return nil
		}
	}

	// Create ownerReference
	ownerRef := metav1.OwnerReference{
		APIVersion:         "dot-ai.devopstoolkit.live/v1alpha1",
		Kind:               "Solution",
		Name:               solution.Name,
		UID:                solution.UID,
		Controller:         ptr.To(false),
		BlockOwnerDeletion: ptr.To(true),
	}

	// Add ownerReference
	resource.SetOwnerReferences(append(owners, ownerRef))

	// Update the resource
	if err := r.Update(ctx, resource); err != nil {
		return fmt.Errorf("failed to update resource with ownerReference: %w", err)
	}

	return nil
}

// checkResourceHealth determines if a resource is ready by checking common status patterns
// It tries multiple strategies in order:
// 1. Check status.conditions for Ready/Available/Healthy = True
// 2. Check replica counts (readyReplicas vs replicas)
// 3. Fallback: resource exists = ready
func (r *SolutionReconciler) checkResourceHealth(resource *unstructured.Unstructured) (ready bool, reason string) {
	// Strategy 1: Check status.conditions
	if ready, reason, found := r.checkStatusConditions(resource); found {
		return ready, reason
	}

	// Strategy 2: Check replica counts
	if ready, reason, found := r.checkReplicaCounts(resource); found {
		return ready, reason
	}

	// Strategy 3: Fallback - resource exists
	return true, "ResourceExists"
}

// checkStatusConditions looks for common condition types indicating readiness
func (r *SolutionReconciler) checkStatusConditions(resource *unstructured.Unstructured) (ready bool, reason string, found bool) {
	conditions, found, err := unstructured.NestedSlice(resource.Object, "status", "conditions")
	if err != nil || !found || len(conditions) == 0 {
		return false, "", false
	}

	// Common condition types that indicate readiness (in priority order)
	readyConditionTypes := []string{"Ready", "Available", "Healthy", "Synced"}

	for _, condType := range readyConditionTypes {
		for _, cond := range conditions {
			condMap, ok := cond.(map[string]interface{})
			if !ok {
				continue
			}

			conditionType, _, _ := unstructured.NestedString(condMap, "type")
			conditionStatus, _, _ := unstructured.NestedString(condMap, "status")

			if conditionType == condType {
				if conditionStatus == "True" {
					return true, condType, true
				}
				// Found the condition but it's not True
				return false, fmt.Sprintf("%s=%s", condType, conditionStatus), true
			}
		}
	}

	// Conditions exist but none of the ready types found - consider not ready
	return false, "NoReadyCondition", true
}

// checkReplicaCounts checks if readyReplicas matches desired replicas
func (r *SolutionReconciler) checkReplicaCounts(resource *unstructured.Unstructured) (ready bool, reason string, found bool) {
	// Try status.readyReplicas (Deployment, ReplicaSet, StatefulSet in newer K8s)
	readyReplicas, foundReady, err := unstructured.NestedInt64(resource.Object, "status", "readyReplicas")
	if err != nil || !foundReady {
		// Try status.availableReplicas (StatefulSet in older K8s)
		readyReplicas, foundReady, err = unstructured.NestedInt64(resource.Object, "status", "availableReplicas")
		if err != nil || !foundReady {
			// Try status.numberReady (DaemonSet)
			readyReplicas, foundReady, err = unstructured.NestedInt64(resource.Object, "status", "numberReady")
			if err != nil || !foundReady {
				return false, "", false
			}
		}
	}

	// Try spec.replicas
	replicas, foundReplicas, err := unstructured.NestedInt64(resource.Object, "spec", "replicas")
	if err != nil || !foundReplicas {
		// Try status.desiredNumberScheduled (DaemonSet)
		replicas, foundReplicas, err = unstructured.NestedInt64(resource.Object, "status", "desiredNumberScheduled")
		if err != nil || !foundReplicas {
			// Default to 1 if not specified
			replicas = 1
		}
	}

	if readyReplicas >= replicas {
		return true, fmt.Sprintf("Replicas: %d/%d", readyReplicas, replicas), true
	}

	return false, fmt.Sprintf("Replicas: %d/%d", readyReplicas, replicas), true
}

// ensureResourceOwnership ensures all resources in spec.resources have ownerReferences pointing to the Solution
func (r *SolutionReconciler) ensureResourceOwnership(ctx context.Context, solution *dotaiv1alpha1.Solution) error {
	logger := logf.FromContext(ctx)

	for _, ref := range solution.Spec.Resources {
		// Use Solution's namespace if resource namespace not specified
		if ref.Namespace == "" {
			ref.Namespace = solution.Namespace
		}

		// Fetch the resource
		resource, err := r.getResource(ctx, ref)
		if err != nil {
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("Resource not found, skipping ownerReference",
					"kind", ref.Kind,
					"name", ref.Name,
					"namespace", ref.Namespace)
				continue
			}
			if apierrors.IsForbidden(err) {
				logger.Info("⚠️ Insufficient RBAC permissions for resource",
					"kind", ref.Kind,
					"name", ref.Name,
					"namespace", ref.Namespace,
					"error", err.Error())
				r.Recorder.Eventf(solution, "Warning", "InsufficientPermissions",
					"Cannot access %s/%s: %v", ref.Kind, ref.Name, err)
				continue
			}
			// Other errors
			logger.Error(err, "Failed to fetch resource",
				"kind", ref.Kind,
				"name", ref.Name,
				"namespace", ref.Namespace)
			continue
		}

		// Ensure ownerReference
		if err := r.ensureOwnerReference(ctx, solution, resource); err != nil {
			logger.Error(err, "Failed to add ownerReference",
				"kind", ref.Kind,
				"name", ref.Name,
				"namespace", ref.Namespace)
			r.Recorder.Eventf(solution, "Warning", "OwnerReferenceFailed",
				"Failed to add ownerReference to %s/%s: %v", ref.Kind, ref.Name, err)
			continue
		}

		logger.V(1).Info("✅ ownerReference ensured",
			"kind", ref.Kind,
			"name", ref.Name,
			"namespace", ref.Namespace)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SolutionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotaiv1alpha1.Solution{}).
		Named("solution").
		Complete(r)
}
