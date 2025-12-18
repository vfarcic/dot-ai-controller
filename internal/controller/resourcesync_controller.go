// resourcesync_controller.go implements resource visibility and status tracking.
// This controller watches ResourceSyncConfig CRs and, when one exists, watches
// all resources in the cluster and syncs metadata + status to the MCP server
// for semantic search capabilities.
//
// Key responsibilities:
// - Watch ResourceSyncConfig CRs to enable/disable resource syncing
// - Discover all resource types via the Discovery API
// - Watch CRDs for immediate detection of new/removed custom resources
// - Create dynamic informers for each discovered GVR
// - Detect changes to resources (labels, status)
// - Batch and send changes to MCP endpoint
// - Periodic resync for eventual consistency
package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var (
	// crdGVR is the GVR for CustomResourceDefinitions
	crdGVR = schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
)

// ResourceSyncReconciler reconciles ResourceSyncConfig objects and manages
// dynamic resource watching based on their configuration
type ResourceSyncReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   record.EventRecorder
	RestConfig *rest.Config

	// dynamicClient for fetching arbitrary resources
	dynamicClient dynamic.Interface

	// discoveryClient for finding all resource types
	discoveryClient discovery.DiscoveryInterface

	// activeConfigs tracks which ResourceSyncConfig CRs have active watchers
	// Key is the CR name (cluster-scoped, so no namespace)
	activeConfigs map[string]*activeConfigState
	configsMu     sync.RWMutex
}

// activeConfigState holds the state for an active ResourceSyncConfig
type activeConfigState struct {
	config          *dotaiv1alpha1.ResourceSyncConfig
	informerFactory dynamicinformer.DynamicSharedInformerFactory
	activeInformers map[schema.GroupVersionResource]cache.SharedIndexInformer
	informersMu     sync.RWMutex // protects activeInformers
	stopCh          chan struct{}
	cancel          context.CancelFunc
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=resourcesyncconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=resourcesyncconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=resourcesyncconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile handles ResourceSyncConfig CR changes
func (r *ResourceSyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("resourcesyncconfig", req.Name)

	// Fetch the ResourceSyncConfig
	var config dotaiv1alpha1.ResourceSyncConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if apierrors.IsNotFound(err) {
			// CR was deleted - stop watching resources for this config
			logger.Info("ResourceSyncConfig deleted, stopping resource watcher")
			r.stopWatcher(req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get ResourceSyncConfig")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling ResourceSyncConfig",
		"mcpEndpoint", config.Spec.McpEndpoint,
		"debounceWindow", config.GetDebounceWindow(),
		"resyncInterval", config.GetResyncInterval(),
	)

	// Initialize clients if not already done
	if err := r.ensureClients(); err != nil {
		logger.Error(err, "Failed to initialize clients")
		r.updateStatus(ctx, &config, false, 0, err.Error())
		return ctrl.Result{}, err
	}

	// Check if we already have an active watcher for this config
	r.configsMu.RLock()
	existingState, exists := r.activeConfigs[config.Name]
	r.configsMu.RUnlock()

	if exists {
		// Check if config changed (endpoint, timing, etc.)
		if r.configChanged(existingState.config, &config) {
			logger.Info("ResourceSyncConfig changed, restarting watcher")
			r.stopWatcher(config.Name)
		} else {
			// Config unchanged, just update status with current state
			watchedCount := len(existingState.activeInformers)
			r.updateStatus(ctx, &config, true, watchedCount, "")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// Start a new watcher for this config
	if err := r.startWatcher(ctx, &config); err != nil {
		logger.Error(err, "Failed to start resource watcher")
		r.updateStatus(ctx, &config, false, 0, err.Error())
		r.Recorder.Eventf(&config, corev1.EventTypeWarning, "WatcherFailed",
			"Failed to start resource watcher: %v", err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	logger.Info("ResourceSyncConfig reconciled successfully")
	r.Recorder.Event(&config, corev1.EventTypeNormal, "WatcherStarted",
		"Resource watcher started successfully")

	// Requeue periodically to update status
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// ensureClients initializes the dynamic and discovery clients if needed
func (r *ResourceSyncReconciler) ensureClients() error {
	if r.dynamicClient != nil && r.discoveryClient != nil {
		return nil
	}

	var err error
	r.dynamicClient, err = dynamic.NewForConfig(r.RestConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	r.discoveryClient, err = discovery.NewDiscoveryClientForConfig(r.RestConfig)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	return nil
}

// configChanged checks if the relevant config fields have changed
func (r *ResourceSyncReconciler) configChanged(old, new *dotaiv1alpha1.ResourceSyncConfig) bool {
	if old.Spec.McpEndpoint != new.Spec.McpEndpoint {
		return true
	}
	if old.GetDebounceWindow() != new.GetDebounceWindow() {
		return true
	}
	if old.GetResyncInterval() != new.GetResyncInterval() {
		return true
	}
	// Check auth secret ref changes
	if (old.Spec.McpAuthSecretRef == nil) != (new.Spec.McpAuthSecretRef == nil) {
		return true
	}
	if old.Spec.McpAuthSecretRef != nil && new.Spec.McpAuthSecretRef != nil {
		if old.Spec.McpAuthSecretRef.Name != new.Spec.McpAuthSecretRef.Name ||
			old.Spec.McpAuthSecretRef.Key != new.Spec.McpAuthSecretRef.Key {
			return true
		}
	}
	return false
}

// startWatcher starts watching all cluster resources for the given config
func (r *ResourceSyncReconciler) startWatcher(ctx context.Context, config *dotaiv1alpha1.ResourceSyncConfig) error {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	// Create a cancellable context for this watcher
	_, cancel := context.WithCancel(context.Background())

	// Create informer factory
	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(
		r.dynamicClient,
		30*time.Minute, // Cache resync period
	)

	state := &activeConfigState{
		config:          config.DeepCopy(),
		informerFactory: informerFactory,
		activeInformers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
		stopCh:          make(chan struct{}),
		cancel:          cancel,
	}

	// Discover existing resources and setup informers
	if err := r.discoverAndSetupInformers(ctx, state); err != nil {
		cancel()
		return fmt.Errorf("failed to setup informers: %w", err)
	}

	// Setup CRD watcher for immediate detection of new/removed CRDs
	if err := r.setupCRDWatcher(ctx, state); err != nil {
		cancel()
		return fmt.Errorf("failed to setup CRD watcher: %w", err)
	}

	// Store the state
	r.configsMu.Lock()
	if r.activeConfigs == nil {
		r.activeConfigs = make(map[string]*activeConfigState)
	}
	r.activeConfigs[config.Name] = state
	r.configsMu.Unlock()

	// Start informers
	informerFactory.Start(state.stopCh)

	// Wait for cache sync in background
	go func() {
		logger.Info("Waiting for informer caches to sync", "config", config.Name)
		informerFactory.WaitForCacheSync(state.stopCh)
		logger.Info("Informer caches synced", "config", config.Name)
	}()

	// Update status
	state.informersMu.RLock()
	watchedCount := len(state.activeInformers)
	state.informersMu.RUnlock()
	r.updateStatus(ctx, config, true, watchedCount, "")

	return nil
}

// setupCRDWatcher creates an informer specifically for CRDs to detect new/removed custom resources
func (r *ResourceSyncReconciler) setupCRDWatcher(ctx context.Context, state *activeConfigState) error {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	crdInformer := state.informerFactory.ForResource(crdGVR).Informer()

	_, err := crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			r.onCRDAdd(state, obj)
		},
		DeleteFunc: func(obj interface{}) {
			r.onCRDDelete(state, obj)
		},
		// We don't need UpdateFunc - CRD spec changes don't affect our GVR watching
	})
	if err != nil {
		return fmt.Errorf("failed to add CRD event handler: %w", err)
	}

	state.informersMu.Lock()
	state.activeInformers[crdGVR] = crdInformer
	state.informersMu.Unlock()

	logger.Info("CRD watcher setup complete")
	return nil
}

// onCRDAdd handles new CRD installations
func (r *ResourceSyncReconciler) onCRDAdd(state *activeConfigState, obj interface{}) {
	logger := logf.Log.WithName("resourcesync")

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}

	// Extract GVR from CRD
	gvr, err := r.gvrFromCRD(u)
	if err != nil {
		logger.V(1).Info("Failed to extract GVR from CRD", "crd", u.GetName(), "error", err)
		return
	}

	// Check if we should skip this resource
	if r.shouldSkipResource(gvr.Group, gvr.Resource) {
		logger.V(2).Info("Skipping CRD resource", "gvr", gvr.String())
		return
	}

	// Check if we already have an informer for this GVR
	state.informersMu.RLock()
	_, exists := state.activeInformers[gvr]
	state.informersMu.RUnlock()

	if exists {
		return // Already watching this resource type
	}

	// Create informer for the new CRD
	logger.Info("New CRD detected, creating informer", "crd", u.GetName(), "gvr", gvr.String())

	informer := state.informerFactory.ForResource(gvr).Informer()

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.makeOnAdd(state),
		UpdateFunc: r.makeOnUpdate(state),
		DeleteFunc: r.makeOnDelete(state),
	})
	if err != nil {
		logger.Error(err, "Failed to add event handler for new CRD", "gvr", gvr.String())
		return
	}

	state.informersMu.Lock()
	state.activeInformers[gvr] = informer
	state.informersMu.Unlock()

	// Start the informer (the factory handles this for new informers)
	go informer.Run(state.stopCh)

	logger.Info("Informer created for new CRD", "gvr", gvr.String())
}

// onCRDDelete handles CRD removals
func (r *ResourceSyncReconciler) onCRDDelete(state *activeConfigState, obj interface{}) {
	logger := logf.Log.WithName("resourcesync")

	var u *unstructured.Unstructured
	switch t := obj.(type) {
	case *unstructured.Unstructured:
		u = t
	case cache.DeletedFinalStateUnknown:
		var ok bool
		u, ok = t.Obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
	default:
		return
	}

	// Extract GVR from CRD
	gvr, err := r.gvrFromCRD(u)
	if err != nil {
		logger.V(1).Info("Failed to extract GVR from deleted CRD", "crd", u.GetName(), "error", err)
		return
	}

	// Remove the informer for this GVR
	state.informersMu.Lock()
	if _, exists := state.activeInformers[gvr]; exists {
		delete(state.activeInformers, gvr)
		logger.Info("CRD deleted, removed informer", "crd", u.GetName(), "gvr", gvr.String())
	}
	state.informersMu.Unlock()
}

// gvrFromCRD extracts the GroupVersionResource from a CRD unstructured object
func (r *ResourceSyncReconciler) gvrFromCRD(u *unstructured.Unstructured) (schema.GroupVersionResource, error) {
	// Get spec.group
	group, found, err := unstructured.NestedString(u.Object, "spec", "group")
	if err != nil || !found {
		return schema.GroupVersionResource{}, fmt.Errorf("spec.group not found")
	}

	// Get spec.names.plural (this is the resource name)
	resource, found, err := unstructured.NestedString(u.Object, "spec", "names", "plural")
	if err != nil || !found {
		return schema.GroupVersionResource{}, fmt.Errorf("spec.names.plural not found")
	}

	// Get the served version - prefer the storage version, otherwise first served version
	versions, found, err := unstructured.NestedSlice(u.Object, "spec", "versions")
	if err != nil || !found || len(versions) == 0 {
		return schema.GroupVersionResource{}, fmt.Errorf("spec.versions not found or empty")
	}

	var version string
	for _, v := range versions {
		vMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		served, _, _ := unstructured.NestedBool(vMap, "served")
		if !served {
			continue
		}
		vName, _, _ := unstructured.NestedString(vMap, "name")
		if vName == "" {
			continue
		}
		// Use storage version if found, otherwise keep first served version
		storage, _, _ := unstructured.NestedBool(vMap, "storage")
		if storage {
			version = vName
			break
		}
		if version == "" {
			version = vName
		}
	}

	if version == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("no served version found")
	}

	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}, nil
}

// stopWatcher stops the resource watcher for the given config name
func (r *ResourceSyncReconciler) stopWatcher(name string) {
	r.configsMu.Lock()
	defer r.configsMu.Unlock()

	state, exists := r.activeConfigs[name]
	if !exists {
		return
	}

	// Stop the watcher
	close(state.stopCh)
	state.cancel()
	delete(r.activeConfigs, name)
}

// discoverAndSetupInformers discovers all built-in resource types and creates informers
// CRDs are handled separately by the CRD watcher for immediate detection
func (r *ResourceSyncReconciler) discoverAndSetupInformers(ctx context.Context, state *activeConfigState) error {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	// Discover all resource types (built-in + existing CRDs)
	gvrs, err := r.discoverResources(ctx)
	if err != nil {
		return fmt.Errorf("failed to discover resources: %w", err)
	}

	logger.Info("Discovered resource types", "count", len(gvrs))

	state.informersMu.Lock()
	defer state.informersMu.Unlock()

	// Add informers for discovered GVRs
	for _, gvr := range gvrs {
		if _, exists := state.activeInformers[gvr]; exists {
			continue // Already have an informer for this GVR
		}

		// Create informer for this GVR
		informer := state.informerFactory.ForResource(gvr).Informer()

		// Add event handlers (will be fully implemented in M2)
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    r.makeOnAdd(state),
			UpdateFunc: r.makeOnUpdate(state),
			DeleteFunc: r.makeOnDelete(state),
		})
		if err != nil {
			logger.Error(err, "Failed to add event handler", "gvr", gvr.String())
			continue
		}

		state.activeInformers[gvr] = informer
		logger.V(1).Info("Created informer", "gvr", gvr.String())
	}

	logger.Info("Informer setup complete", "activeInformers", len(state.activeInformers))
	return nil
}

// discoverResources discovers all watchable resource types in the cluster
func (r *ResourceSyncReconciler) discoverResources(ctx context.Context) ([]schema.GroupVersionResource, error) {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	// Get all API resources
	_, resources, err := r.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Discovery can return partial results with errors for unavailable API groups
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return nil, fmt.Errorf("failed to discover API resources: %w", err)
		}
		logger.V(1).Info("Partial discovery failure (some API groups unavailable)", "error", err)
	}

	var gvrs []schema.GroupVersionResource
	for _, resourceList := range resources {
		if resourceList == nil {
			continue
		}

		gv, err := schema.ParseGroupVersion(resourceList.GroupVersion)
		if err != nil {
			logger.V(1).Info("Failed to parse group version", "groupVersion", resourceList.GroupVersion, "error", err)
			continue
		}

		for _, resource := range resourceList.APIResources {
			// Skip subresources (e.g., pods/log, pods/exec)
			if strings.Contains(resource.Name, "/") {
				continue
			}

			// Skip resources that don't support list/watch
			if !containsVerb(resource.Verbs, "list") || !containsVerb(resource.Verbs, "watch") {
				continue
			}

			// Skip certain high-volume, low-value resources
			if r.shouldSkipResource(gv.Group, resource.Name) {
				logger.V(2).Info("Skipping resource", "group", gv.Group, "resource", resource.Name)
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    gv.Group,
				Version:  gv.Version,
				Resource: resource.Name,
			}
			gvrs = append(gvrs, gvr)
		}
	}

	return gvrs, nil
}

// shouldSkipResource returns true if the resource should not be watched
func (r *ResourceSyncReconciler) shouldSkipResource(group, resource string) bool {
	// Skip Kubernetes Events (high volume, low signal for resource visibility)
	if group == "" && resource == "events" {
		return true
	}
	if group == "events.k8s.io" && resource == "events" {
		return true
	}

	// Skip controller-internal resources
	if group == "coordination.k8s.io" && resource == "leases" {
		return true
	}

	// Skip endpoint slices (high churn)
	if group == "discovery.k8s.io" && resource == "endpointslices" {
		return true
	}

	return false
}

// Event handler factories (placeholders for M2)

func (r *ResourceSyncReconciler) makeOnAdd(state *activeConfigState) func(obj interface{}) {
	return func(obj interface{}) {
		// Will be implemented in M2
		_ = obj.(*unstructured.Unstructured)
	}
}

func (r *ResourceSyncReconciler) makeOnUpdate(state *activeConfigState) func(oldObj, newObj interface{}) {
	return func(oldObj, newObj interface{}) {
		// Will be implemented in M2
		_ = oldObj.(*unstructured.Unstructured)
		_ = newObj.(*unstructured.Unstructured)
	}
}

func (r *ResourceSyncReconciler) makeOnDelete(state *activeConfigState) func(obj interface{}) {
	return func(obj interface{}) {
		// Will be implemented in M2
		switch t := obj.(type) {
		case *unstructured.Unstructured:
			_ = t
		case cache.DeletedFinalStateUnknown:
			_ = t.Obj.(*unstructured.Unstructured)
		}
	}
}

// updateStatus updates the ResourceSyncConfig status
func (r *ResourceSyncReconciler) updateStatus(ctx context.Context, config *dotaiv1alpha1.ResourceSyncConfig, active bool, watchedTypes int, lastError string) {
	logger := logf.FromContext(ctx)

	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.ResourceSyncConfig{}
	if err := r.Get(ctx, client.ObjectKey{Name: config.Name}, fresh); err != nil {
		logger.Error(err, "Failed to fetch fresh ResourceSyncConfig for status update")
		return
	}

	// Update status fields
	fresh.Status.Active = active
	fresh.Status.WatchedResourceTypes = watchedTypes
	fresh.Status.LastError = lastError

	// Update Ready condition
	now := metav1.NewTime(time.Now())
	var readyCondition metav1.Condition

	if active {
		readyCondition = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "WatcherActive",
			Message:            fmt.Sprintf("Watching %d resource types", watchedTypes),
		}
	} else {
		reason := "WatcherInactive"
		message := "Resource watcher is not running"
		if lastError != "" {
			reason = "WatcherError"
			message = lastError
		}
		readyCondition = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             reason,
			Message:            message,
		}
	}

	// Update or add condition
	updated := false
	for i, cond := range fresh.Status.Conditions {
		if cond.Type == "Ready" {
			fresh.Status.Conditions[i] = readyCondition
			updated = true
			break
		}
	}
	if !updated {
		fresh.Status.Conditions = append(fresh.Status.Conditions, readyCondition)
	}

	// Update status subresource
	if err := r.Status().Update(ctx, fresh); err != nil {
		logger.Error(err, "Failed to update ResourceSyncConfig status")
	}
}

// Helper functions

func containsVerb(verbs []string, verb string) bool {
	for _, v := range verbs {
		if v == verb {
			return true
		}
	}
	return false
}

// GetActiveConfigCount returns the number of active configs (for testing)
func (r *ResourceSyncReconciler) GetActiveConfigCount() int {
	r.configsMu.RLock()
	defer r.configsMu.RUnlock()
	return len(r.activeConfigs)
}

// GetWatchedGVRs returns the list of watched GVRs for a config (for testing)
func (r *ResourceSyncReconciler) GetWatchedGVRs(configName string) []schema.GroupVersionResource {
	r.configsMu.RLock()
	state, exists := r.activeConfigs[configName]
	r.configsMu.RUnlock()

	if !exists {
		return nil
	}

	state.informersMu.RLock()
	defer state.informersMu.RUnlock()

	gvrs := make([]schema.GroupVersionResource, 0, len(state.activeInformers))
	for gvr := range state.activeInformers {
		gvrs = append(gvrs, gvr)
	}
	return gvrs
}

// SetupWithManager sets up the controller with the Manager
func (r *ResourceSyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotaiv1alpha1.ResourceSyncConfig{}).
		Named("resourcesync").
		Complete(r)
}
