// resourcesync_controller.go implements resource visibility for semantic search.
// This controller watches ResourceSyncConfig CRs and, when one exists, watches
// all resources in the cluster and syncs metadata (labels, annotations) to the
// MCP server for semantic search capabilities.
//
// Note: Status and spec are NOT synced - they are fetched on-demand from the
// Kubernetes API when needed. This reduces sync traffic since labels rarely
// change after resource creation.
//
// Key responsibilities:
// - Watch ResourceSyncConfig CRs to enable/disable resource syncing
// - Discover all resource types via the Discovery API
// - Watch CRDs for immediate detection of new/removed custom resources
// - Create dynamic informers for each discovered GVR
// - Detect changes to resources (labels only - status changes are ignored)
// - Batch and send changes to MCP endpoint
// - Periodic resync for eventual consistency
package controller

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
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

// ChangeAction represents the type of change detected for a resource
type ChangeAction int

const (
	// ActionUpsert indicates a resource was added or updated
	ActionUpsert ChangeAction = iota
	// ActionDelete indicates a resource was deleted
	ActionDelete
)

// ResourceData contains the data extracted from a Kubernetes resource for syncing to MCP
// Note: ID is not included - MCP constructs it from namespace/apiVersion/kind/name
type ResourceData struct {
	// Namespace of the resource ("_cluster" for cluster-scoped resources)
	Namespace string `json:"namespace"`
	// Name of the resource
	Name string `json:"name"`
	// Kind of the resource (e.g., "Deployment", "Pod")
	Kind string `json:"kind"`
	// APIVersion including group (e.g., "apps/v1", "v1")
	APIVersion string `json:"apiVersion"`
	// Labels from the resource
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations from the resource (selected ones, not all)
	Annotations map[string]string `json:"annotations,omitempty"`
	// CreatedAt is when the resource was created
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when this data was last updated (now)
	UpdatedAt time.Time `json:"updatedAt"`
}

// ResourceIdentifier contains the fields needed to identify a resource for deletion
// MCP uses these fields to construct the ID for the delete operation
type ResourceIdentifier struct {
	// Namespace of the resource ("_cluster" for cluster-scoped resources)
	Namespace string `json:"namespace"`
	// Name of the resource
	Name string `json:"name"`
	// Kind of the resource (e.g., "Deployment", "Pod")
	Kind string `json:"kind"`
	// APIVersion including group (e.g., "apps/v1", "v1")
	APIVersion string `json:"apiVersion"`
}

// ResourceChange represents a change to be synced to MCP
type ResourceChange struct {
	// Action is either ActionUpsert or ActionDelete
	Action ChangeAction
	// Data contains the resource data (nil for deletes)
	Data *ResourceData
	// ID is the internal resource identifier used for debounce buffer deduplication
	// Format: namespace:apiVersion:kind:name (or _cluster:apiVersion:kind:name for cluster-scoped)
	ID string
	// DeleteIdentifier contains the fields MCP needs to construct the ID for delete operations
	// Only populated for ActionDelete
	DeleteIdentifier *ResourceIdentifier
}

const (
	// changeQueueBufferSize is the buffer size for the change queue
	// Large enough to handle startup bursts and rolling updates
	changeQueueBufferSize = 10000

	// clusterScopeNamespace is used in resource IDs for cluster-scoped resources
	clusterScopeNamespace = "_cluster"
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

	// HttpClient for MCP communication
	HttpClient *http.Client

	// dynamicClient for fetching arbitrary resources
	dynamicClient dynamic.Interface

	// discoveryClient for finding all resource types
	discoveryClient discovery.DiscoveryInterface

	// activeConfigs tracks which ResourceSyncConfig CRs have active watchers
	// Key is namespace/name of the CR
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
	// changeQueue receives resource changes from informer event handlers
	// Buffered to prevent blocking informers during bursts
	changeQueue chan *ResourceChange
	// changeQueueClosed indicates the changeQueue has been closed
	changeQueueClosed bool
	changeQueueMu     sync.RWMutex
	// debounceBuffer collects and batches changes before sending to MCP
	debounceBuffer *DebounceBuffer
	// mcpClient handles communication with the MCP endpoint
	mcpClient *MCPResourceSyncClient
}

// configKey returns a unique key for a ResourceSyncConfig (namespace/name)
func configKey(config *dotaiv1alpha1.ResourceSyncConfig) string {
	return config.Namespace + "/" + config.Name
}

// parseConfigKey parses a config key back to namespace and name
func parseConfigKey(key string) (namespace, name string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
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
			r.stopWatcher(req.Namespace + "/" + req.Name)
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
		r.updateStatus(ctx, &config, false, 0, err.Error(), time.Time{})
		return ctrl.Result{}, err
	}

	// Check if we already have an active watcher for this config
	r.configsMu.RLock()
	existingState, exists := r.activeConfigs[configKey(&config)]
	r.configsMu.RUnlock()

	if exists {
		// Check if config changed (endpoint, timing, etc.)
		if r.configChanged(existingState.config, &config) {
			logger.Info("ResourceSyncConfig changed, restarting watcher")
			r.stopWatcher(configKey(&config))
		} else {
			// Config unchanged, just update status with current state
			existingState.informersMu.RLock()
			watchedCount := len(existingState.activeInformers)
			existingState.informersMu.RUnlock()

			// Check debounce buffer for sync errors and get last flush time
			lastError := ""
			var lastFlushTime time.Time
			if existingState.debounceBuffer != nil {
				metrics := existingState.debounceBuffer.GetMetrics()
				lastFlushTime = metrics.LastFlushTime
				if metrics.LastError != "" {
					lastError = metrics.LastError
					// Increment sync errors when we detect an error from debounce buffer
					r.updateSyncErrorCount(ctx, &config)
				}
			}
			r.updateStatus(ctx, &config, true, watchedCount, lastError, lastFlushTime)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// Start a new watcher for this config
	if err := r.startWatcher(ctx, &config); err != nil {
		logger.Error(err, "Failed to start resource watcher")
		r.updateStatus(ctx, &config, false, 0, err.Error(), time.Time{})
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
	if old.Spec.McpAuthSecretRef.Name != new.Spec.McpAuthSecretRef.Name ||
		old.Spec.McpAuthSecretRef.Key != new.Spec.McpAuthSecretRef.Key {
		return true
	}
	return false
}

// startWatcher starts watching all cluster resources for the given config
func (r *ResourceSyncReconciler) startWatcher(ctx context.Context, config *dotaiv1alpha1.ResourceSyncConfig) error {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	// Create a cancellable context for this watcher
	watcherCtx, cancel := context.WithCancel(context.Background())

	// Create informer factory
	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(
		r.dynamicClient,
		30*time.Minute, // Cache resync period
	)

	// Create change queue
	changeQueue := make(chan *ResourceChange, changeQueueBufferSize)

	// Create MCP client if endpoint is configured
	var mcpClient *MCPResourceSyncClient
	if config.Spec.McpEndpoint != "" {
		httpClient := r.HttpClient
		if httpClient == nil {
			httpClient = &http.Client{
				Timeout: 60 * time.Second,
			}
		}

		mcpClient = NewMCPResourceSyncClient(MCPResourceSyncClientConfig{
			Endpoint:            config.Spec.McpEndpoint,
			HTTPClient:          httpClient,
			K8sClient:           r.Client,
			AuthSecretRef:       config.Spec.McpAuthSecretRef,
			AuthSecretNamespace: config.Namespace,
		})
		logger.Info("MCP client created", "endpoint", config.Spec.McpEndpoint)
	} else {
		logger.Info("MCP endpoint not configured, resource sync will be disabled")
	}

	// Create debounce buffer
	debounceBuffer := NewDebounceBuffer(DebounceBufferConfig{
		Window:      time.Duration(config.GetDebounceWindow()) * time.Second,
		MCPClient:   mcpClient,
		ChangeQueue: changeQueue,
	})

	state := &activeConfigState{
		config:          config.DeepCopy(),
		informerFactory: informerFactory,
		activeInformers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
		stopCh:          make(chan struct{}),
		cancel:          cancel,
		changeQueue:     changeQueue,
		debounceBuffer:  debounceBuffer,
		mcpClient:       mcpClient,
	}

	// Discover existing resources and setup informers
	if err := r.discoverAndSetupInformers(ctx, state); err != nil {
		cancel()
		close(changeQueue)
		return fmt.Errorf("failed to setup informers: %w", err)
	}

	// Setup CRD watcher for immediate detection of new/removed CRDs
	if err := r.setupCRDWatcher(ctx, state); err != nil {
		cancel()
		close(changeQueue)
		return fmt.Errorf("failed to setup CRD watcher: %w", err)
	}

	// Store the state
	r.configsMu.Lock()
	if r.activeConfigs == nil {
		r.activeConfigs = make(map[string]*activeConfigState)
	}
	r.activeConfigs[configKey(config)] = state
	r.configsMu.Unlock()

	// Start informers
	informerFactory.Start(state.stopCh)

	// Start debounce buffer in background
	go func() {
		logger.Info("Starting debounce buffer", "config", config.Name, "window", config.GetDebounceWindow())
		debounceBuffer.Run(watcherCtx)
		logger.Info("Debounce buffer stopped", "config", config.Name)
	}()

	// Wait for cache sync, then perform initial sync and start periodic resync
	go func() {
		logger.Info("Waiting for informer caches to sync", "config", config.Name)
		informerFactory.WaitForCacheSync(state.stopCh)
		logger.Info("Informer caches synced", "config", config.Name)

		// Check if context is still valid
		select {
		case <-watcherCtx.Done():
			logger.Info("Context cancelled, skipping initial sync", "config", config.Name)
			return
		default:
		}

		// Perform initial sync
		r.performInitialSync(watcherCtx, state, configKey(config))

		// Start periodic resync loop
		resyncInterval := time.Duration(config.GetResyncInterval()) * time.Minute
		go r.periodicResyncLoop(watcherCtx, state, configKey(config), resyncInterval)
	}()

	// Update status
	state.informersMu.RLock()
	watchedCount := len(state.activeInformers)
	state.informersMu.RUnlock()
	r.updateStatus(ctx, config, true, watchedCount, "", time.Time{})

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

	// Mark change queue as closed first (before actually closing)
	// This prevents event handlers from trying to send to closed channel
	state.changeQueueMu.Lock()
	state.changeQueueClosed = true
	state.changeQueueMu.Unlock()

	// Stop the watcher - cancel context first to stop debounce buffer
	state.cancel()
	// Close stopCh to stop informers
	close(state.stopCh)
	// Close change queue to signal debounce buffer to exit
	close(state.changeQueue)
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

// buildResourceID creates a unique identifier for a resource
// Format: namespace:apiVersion:kind:name
// For cluster-scoped resources, namespace is "_cluster"
func buildResourceID(obj *unstructured.Unstructured) string {
	ns := obj.GetNamespace()
	if ns == "" {
		ns = clusterScopeNamespace
	}
	return fmt.Sprintf("%s:%s:%s:%s", ns, obj.GetAPIVersion(), obj.GetKind(), obj.GetName())
}

// extractResourceData extracts the relevant data from a Kubernetes resource
func extractResourceData(obj *unstructured.Unstructured) *ResourceData {
	// Get labels (make a copy to avoid mutation)
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	} else {
		labelsCopy := make(map[string]string, len(labels))
		for k, v := range labels {
			labelsCopy[k] = v
		}
		labels = labelsCopy
	}

	// Get selected annotations (skip large/internal ones)
	annotations := make(map[string]string)
	for k, v := range obj.GetAnnotations() {
		// Skip kubectl's last-applied-configuration (can be very large)
		if k == "kubectl.kubernetes.io/last-applied-configuration" {
			continue
		}
		// Skip managed fields annotation
		if strings.HasPrefix(k, "meta.helm.sh/") {
			continue
		}
		// Include description-like annotations that are useful for search
		if k == "description" || strings.HasSuffix(k, "/description") {
			annotations[k] = v
		}
	}

	// For cluster-scoped resources, use "_cluster" as the namespace
	// MCP endpoint requires namespace to be a non-empty string
	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = clusterScopeNamespace
	}

	return &ResourceData{
		Namespace:   namespace,
		Name:        obj.GetName(),
		Kind:        obj.GetKind(),
		APIVersion:  obj.GetAPIVersion(),
		Labels:      labels,
		Annotations: annotations,
		CreatedAt:   obj.GetCreationTimestamp().Time,
		UpdatedAt:   time.Now(),
	}
}

// hasRelevantChanges checks if the resource has changes worth syncing
// Only compares labels - status is fetched on-demand from K8s API
func hasRelevantChanges(oldObj, newObj *unstructured.Unstructured) bool {
	// Compare labels only - status changes don't trigger sync
	return !reflect.DeepEqual(oldObj.GetLabels(), newObj.GetLabels())
}

// Event handler factories

// trySendChange attempts to send a change to the queue, returning false if queue is closed or full
func trySendChange(state *activeConfigState, change *ResourceChange) bool {
	// Check if queue is closed first
	state.changeQueueMu.RLock()
	closed := state.changeQueueClosed
	state.changeQueueMu.RUnlock()

	if closed {
		return false
	}

	// Try to send (non-blocking)
	select {
	case state.changeQueue <- change:
		return true
	default:
		return false
	}
}

// makeOnAdd creates an OnAdd handler that queues new resources for syncing
func (r *ResourceSyncReconciler) makeOnAdd(state *activeConfigState) func(obj interface{}) {
	logger := logf.Log.WithName("resourcesync")

	return func(obj interface{}) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			logger.V(2).Info("OnAdd received non-unstructured object", "type", fmt.Sprintf("%T", obj))
			return
		}

		// Extract resource data and build internal ID for deduplication
		data := extractResourceData(u)
		id := buildResourceID(u)

		// Queue the change (non-blocking with select to handle full queue)
		change := &ResourceChange{
			Action: ActionUpsert,
			Data:   data,
			ID:     id,
		}

		if trySendChange(state, change) {
			logger.V(2).Info("Queued resource add", "id", id)
		} else {
			// Queue is full or closed - log and drop (debounce buffer will catch this on resync)
			logger.V(1).Info("Change queue full or closed, dropping add event", "id", id)
		}
	}
}

// makeOnUpdate creates an OnUpdate handler that queues changed resources for syncing
func (r *ResourceSyncReconciler) makeOnUpdate(state *activeConfigState) func(oldObj, newObj interface{}) {
	logger := logf.Log.WithName("resourcesync")

	return func(oldObj, newObj interface{}) {
		oldU, ok := oldObj.(*unstructured.Unstructured)
		if !ok {
			logger.V(2).Info("OnUpdate received non-unstructured old object", "type", fmt.Sprintf("%T", oldObj))
			return
		}

		newU, ok := newObj.(*unstructured.Unstructured)
		if !ok {
			logger.V(2).Info("OnUpdate received non-unstructured new object", "type", fmt.Sprintf("%T", newObj))
			return
		}

		// Check if there are relevant changes
		id := buildResourceID(newU)
		if !hasRelevantChanges(oldU, newU) {
			logger.V(3).Info("No relevant changes detected", "id", id)
			return
		}

		// Extract resource data
		data := extractResourceData(newU)

		// Queue the change
		change := &ResourceChange{
			Action: ActionUpsert,
			Data:   data,
			ID:     id,
		}

		if trySendChange(state, change) {
			logger.V(2).Info("Queued resource update", "id", id)
		} else {
			// Queue is full or closed - log and drop
			logger.V(1).Info("Change queue full or closed, dropping update event", "id", id)
		}
	}
}

// makeOnDelete creates an OnDelete handler that queues resource deletions for syncing
func (r *ResourceSyncReconciler) makeOnDelete(state *activeConfigState) func(obj interface{}) {
	logger := logf.Log.WithName("resourcesync")

	return func(obj interface{}) {
		var u *unstructured.Unstructured

		switch t := obj.(type) {
		case *unstructured.Unstructured:
			u = t
		case cache.DeletedFinalStateUnknown:
			// Object was deleted before we could process it
			var ok bool
			u, ok = t.Obj.(*unstructured.Unstructured)
			if !ok {
				logger.V(2).Info("OnDelete DeletedFinalStateUnknown contains non-unstructured object",
					"type", fmt.Sprintf("%T", t.Obj))
				return
			}
		default:
			logger.V(2).Info("OnDelete received unexpected object type", "type", fmt.Sprintf("%T", obj))
			return
		}

		// Build the resource ID for internal deduplication
		id := buildResourceID(u)

		// Build the identifier for MCP to construct the ID
		// For cluster-scoped resources, use "_cluster" as the namespace
		namespace := u.GetNamespace()
		if namespace == "" {
			namespace = clusterScopeNamespace
		}

		deleteIdentifier := &ResourceIdentifier{
			Namespace:  namespace,
			Name:       u.GetName(),
			Kind:       u.GetKind(),
			APIVersion: u.GetAPIVersion(),
		}

		// Queue the deletion
		change := &ResourceChange{
			Action:           ActionDelete,
			Data:             nil, // No data needed for deletes
			ID:               id,
			DeleteIdentifier: deleteIdentifier,
		}

		if trySendChange(state, change) {
			logger.V(2).Info("Queued resource delete", "id", id)
		} else {
			// Queue is full or closed - log and drop (resync will catch orphaned records)
			logger.V(1).Info("Change queue full or closed, dropping delete event", "id", id)
		}
	}
}

// updateSyncErrorCount increments the sync error count for a ResourceSyncConfig
func (r *ResourceSyncReconciler) updateSyncErrorCount(ctx context.Context, config *dotaiv1alpha1.ResourceSyncConfig) {
	logger := logf.FromContext(ctx)

	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.ResourceSyncConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: config.Name}, fresh); err != nil {
		logger.Error(err, "Failed to fetch ResourceSyncConfig for sync error count update")
		return
	}

	fresh.Status.SyncErrors++
	if err := r.Status().Update(ctx, fresh); err != nil {
		if apierrors.IsConflict(err) {
			logger.V(1).Info("Conflict updating sync error count, will retry on next reconcile")
			return
		}
		logger.V(1).Info("Failed to update sync error count", "error", err)
	}
}

// updateStatus updates the ResourceSyncConfig status
func (r *ResourceSyncReconciler) updateStatus(ctx context.Context, config *dotaiv1alpha1.ResourceSyncConfig, active bool, watchedTypes int, lastError string, lastFlushTime time.Time) {
	logger := logf.FromContext(ctx)

	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.ResourceSyncConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: config.Name}, fresh); err != nil {
		logger.Error(err, "Failed to fetch fresh ResourceSyncConfig for status update")
		return
	}

	// Update status fields
	fresh.Status.Active = active
	fresh.Status.WatchedResourceTypes = watchedTypes
	fresh.Status.LastError = lastError

	// Update LastSyncTime from debounce buffer's lastFlushTime if it's more recent
	if !lastFlushTime.IsZero() {
		flushMetaTime := metav1.NewTime(lastFlushTime)
		if fresh.Status.LastSyncTime == nil || lastFlushTime.After(fresh.Status.LastSyncTime.Time) {
			fresh.Status.LastSyncTime = &flushMetaTime
		}
	}

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

// Resync functions

// listAllResources iterates over all active informers and extracts ResourceData from cached resources
func (r *ResourceSyncReconciler) listAllResources(state *activeConfigState) []*ResourceData {
	logger := logf.Log.WithName("resourcesync")

	state.informersMu.RLock()
	informers := make(map[schema.GroupVersionResource]cache.SharedIndexInformer, len(state.activeInformers))
	for gvr, informer := range state.activeInformers {
		informers[gvr] = informer
	}
	state.informersMu.RUnlock()

	var allResources []*ResourceData

	for gvr, informer := range informers {
		// Skip CRD informer - we don't sync CRDs themselves
		if gvr == crdGVR {
			continue
		}

		// Get all items from the informer's cache
		items := informer.GetStore().List()
		logger.V(2).Info("Listing resources from informer",
			"gvr", gvr.String(),
			"count", len(items),
		)

		for _, item := range items {
			u, ok := item.(*unstructured.Unstructured)
			if !ok {
				logger.V(2).Info("Skipping non-unstructured item",
					"gvr", gvr.String(),
					"type", fmt.Sprintf("%T", item),
				)
				continue
			}

			data := extractResourceData(u)
			allResources = append(allResources, data)
		}
	}

	logger.Info("Listed all resources from informer caches",
		"totalResources", len(allResources),
		"informerCount", len(informers)-1, // -1 for CRD informer
	)

	return allResources
}

// performResync sends all current resources to MCP for reconciliation
// MCP will diff against Qdrant and handle any drift (insert new, update changed, delete missing)
// performResync syncs all resources to MCP and returns the resource count
func (r *ResourceSyncReconciler) performResync(ctx context.Context, state *activeConfigState) (int, error) {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	if state.mcpClient == nil {
		logger.V(1).Info("MCP client not configured, skipping resync")
		return 0, nil
	}

	// List all resources from informer caches (only called once)
	allResources := r.listAllResources(state)
	resourceCount := len(allResources)

	if resourceCount == 0 {
		logger.Info("No resources to resync")
		return 0, nil
	}

	logger.Info("Performing resync with MCP",
		"resourceCount", resourceCount,
	)

	// Send to MCP with IsResync flag
	resp, err := state.mcpClient.Resync(ctx, allResources)
	if err != nil {
		logger.Error(err, "Failed to resync resources to MCP")
		return resourceCount, fmt.Errorf("resync failed: %w", err)
	}

	if !resp.Success {
		errMsg := resp.GetErrorMessage()
		logger.Error(nil, "MCP resync returned error",
			"error", errMsg,
			"failures", resp.GetFailures(),
		)
		return resourceCount, fmt.Errorf("MCP resync error: %s", errMsg)
	}

	upserted, deleted := resp.GetSuccessCounts()
	logger.Info("Resync completed successfully",
		"upserted", upserted,
		"deleted", deleted,
	)

	return resourceCount, nil
}

// performInitialSync performs the initial sync after informer caches are synced
// This ensures all existing resources are sent to MCP on startup
func (r *ResourceSyncReconciler) performInitialSync(ctx context.Context, state *activeConfigState, configName string) {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	logger.Info("Performing initial sync", "config", configName)

	resourceCount, err := r.performResync(ctx, state)

	// Update status
	r.configsMu.RLock()
	currentState, exists := r.activeConfigs[configName]
	r.configsMu.RUnlock()

	if !exists || currentState != state {
		logger.Info("Config no longer active, skipping status update", "config", configName)
		return
	}

	// Fetch fresh config for status update
	configNamespace, configNameOnly := parseConfigKey(configName)
	var config dotaiv1alpha1.ResourceSyncConfig
	if getErr := r.Get(ctx, client.ObjectKey{Namespace: configNamespace, Name: configNameOnly}, &config); getErr != nil {
		logger.Error(getErr, "Failed to fetch ResourceSyncConfig for status update")
		return
	}

	now := metav1.NewTime(time.Now())
	config.Status.LastResyncTime = &now
	config.Status.LastSyncTime = &now

	if err != nil {
		config.Status.SyncErrors++
		config.Status.LastError = err.Error()
		logger.Error(err, "Initial sync failed", "config", configName)
	} else {
		config.Status.LastError = ""
		// Use count from performResync (avoids redundant listAllResources call)
		config.Status.TotalResourcesSynced = int64(resourceCount)
		logger.Info("Initial sync completed", "config", configName, "resourceCount", resourceCount)
	}

	if statusErr := r.Status().Update(ctx, &config); statusErr != nil {
		logger.Error(statusErr, "Failed to update status after initial sync")
	}
}

// periodicResyncLoop runs periodic resyncs at the configured interval
func (r *ResourceSyncReconciler) periodicResyncLoop(ctx context.Context, state *activeConfigState, configName string, interval time.Duration) {
	logger := logf.FromContext(ctx).WithName("resourcesync")

	logger.Info("Starting periodic resync loop",
		"config", configName,
		"interval", interval,
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Periodic resync loop stopping", "config", configName)
			return

		case <-ticker.C:
			// Check if this config is still active
			r.configsMu.RLock()
			currentState, exists := r.activeConfigs[configName]
			r.configsMu.RUnlock()

			if !exists || currentState != state {
				logger.Info("Config no longer active, stopping resync loop", "config", configName)
				return
			}

			logger.Info("Starting periodic resync", "config", configName)

			resourceCount, err := r.performResync(ctx, state)

			// Update status
			configNamespace, configNameOnly := parseConfigKey(configName)
			var config dotaiv1alpha1.ResourceSyncConfig
			if getErr := r.Get(ctx, client.ObjectKey{Namespace: configNamespace, Name: configNameOnly}, &config); getErr != nil {
				logger.Error(getErr, "Failed to fetch ResourceSyncConfig for status update")
				continue
			}

			now := metav1.NewTime(time.Now())
			config.Status.LastResyncTime = &now
			config.Status.LastSyncTime = &now

			if err != nil {
				config.Status.SyncErrors++
				config.Status.LastError = err.Error()
				logger.Error(err, "Periodic resync failed", "config", configName)
			} else {
				config.Status.LastError = ""
				// Use count from performResync (avoids redundant listAllResources call)
				config.Status.TotalResourcesSynced = int64(resourceCount)
				logger.Info("Periodic resync completed", "config", configName, "resourceCount", resourceCount)
			}

			if statusErr := r.Status().Update(ctx, &config); statusErr != nil {
				logger.Error(statusErr, "Failed to update status after periodic resync")
			}
		}
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
