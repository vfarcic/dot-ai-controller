// capabilityscan_controller.go implements autonomous capability scanning.
// This controller watches CapabilityScanConfig CRs and, when one exists,
// watches CRD events and triggers capability scans via the MCP server.
//
// Key responsibilities:
// - Watch CapabilityScanConfig CRs to enable/disable capability scanning
// - On startup, check if capabilities exist; if not, trigger full scan
// - Watch CRD create/delete events and trigger targeted scans
// - Apply include/exclude filters to determine which resources to scan
package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// CapabilityScanReconciler reconciles CapabilityScanConfig objects
type CapabilityScanReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// RestConfig for creating discovery client
	RestConfig *rest.Config

	// discoveryClient for finding all resource types
	discoveryClient discovery.DiscoveryInterface

	// activeConfigs tracks which CapabilityScanConfig CRs have active state
	// Key is namespace/name of the CR
	activeConfigs map[string]*capabilityScanState
	configsMu     sync.RWMutex
}

// capabilityScanState holds the state for an active CapabilityScanConfig
type capabilityScanState struct {
	config       *dotaiv1alpha1.CapabilityScanConfig
	mcpClient    *MCPCapabilityScanClient
	buffer       *CapabilityScanBuffer
	bufferCancel context.CancelFunc
}

// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=capabilityscanconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=capabilityscanconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dot-ai.devopstoolkit.live,resources=capabilityscanconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile handles CapabilityScanConfig CR changes
func (r *CapabilityScanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx).WithValues("capabilityscanconfig", req.Name)

	// Fetch the CapabilityScanConfig
	var config dotaiv1alpha1.CapabilityScanConfig
	if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
		if apierrors.IsNotFound(err) {
			// CR was deleted - remove from active configs
			logger.Info("CapabilityScanConfig deleted, removing from active configs")
			r.removeConfig(req.Namespace + "/" + req.Name)
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get CapabilityScanConfig")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling CapabilityScanConfig",
		"mcpEndpoint", config.Spec.MCP.Endpoint,
		"collection", config.GetCollection(),
	)

	key := req.Namespace + "/" + req.Name

	// Check if we already have this config active
	// Initialize discovery client if not already set
	if r.discoveryClient == nil && r.RestConfig != nil {
		var err error
		r.discoveryClient, err = discovery.NewDiscoveryClientForConfig(r.RestConfig)
		if err != nil {
			logger.Error(err, "Failed to create discovery client")
			return ctrl.Result{}, err
		}
	}

	r.configsMu.RLock()
	existingState, exists := r.activeConfigs[key]
	r.configsMu.RUnlock()

	if exists {
		// Check if config changed
		if r.configChanged(existingState.config, &config) {
			logger.Info("CapabilityScanConfig changed, updating state")
			r.removeConfig(key)
		} else {
			// Config unchanged, nothing to do
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}
	}

	// Create MCP client and add to active configs
	mcpClient := NewMCPCapabilityScanClient(MCPCapabilityScanClientConfig{
		Endpoint:            config.Spec.MCP.Endpoint,
		Collection:          config.GetCollection(),
		K8sClient:           r.Client,
		AuthSecretRef:       config.Spec.MCP.AuthSecretRef,
		AuthSecretNamespace: config.Namespace,
		MaxRetries:          ptr.To(config.GetMaxAttempts()),
		InitialBackoff:      time.Duration(config.GetBackoffSeconds()) * time.Second,
		MaxBackoff:          time.Duration(config.GetMaxBackoffSeconds()) * time.Second,
	})

	// Create debounce buffer for batching CRD events
	debounceWindow := time.Duration(config.GetDebounceWindowSeconds()) * time.Second
	buffer := NewCapabilityScanBuffer(CapabilityScanBufferConfig{
		Window:    debounceWindow,
		MCPClient: mcpClient,
		OnFlush: func(scans int, deletes int, err error) {
			if err != nil {
				r.updateStatusByKey(context.Background(), key, false, err.Error())
			} else if scans > 0 || deletes > 0 {
				r.updateLastScanTime(context.Background(), key)
			}
		},
	})

	// Start the debounce buffer goroutine
	bufferCtx, bufferCancel := context.WithCancel(context.Background())
	go buffer.Run(bufferCtx)

	state := &capabilityScanState{
		config:       config.DeepCopy(),
		mcpClient:    mcpClient,
		buffer:       buffer,
		bufferCancel: bufferCancel,
	}

	r.configsMu.Lock()
	if r.activeConfigs == nil {
		r.activeConfigs = make(map[string]*capabilityScanState)
	}
	r.activeConfigs[key] = state
	r.configsMu.Unlock()

	// Always perform startup reconciliation to sync cluster CRDs with MCP capabilities
	// This handles: fresh installs, pod restarts, and missed CRD events during downtime
	go r.performStartupReconciliation(context.Background(), state, key)

	r.updateStatus(ctx, &config, true, "")
	r.Recorder.Event(&config, corev1.EventTypeNormal, "ConfigActivated",
		"CapabilityScanConfig is now active")

	logger.Info("✅ CapabilityScanConfig reconciled successfully")
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// configChanged checks if the relevant config fields have changed
func (r *CapabilityScanReconciler) configChanged(old, new *dotaiv1alpha1.CapabilityScanConfig) bool {
	if old.Spec.MCP.Endpoint != new.Spec.MCP.Endpoint {
		return true
	}
	if old.Spec.MCP.Collection != new.Spec.MCP.Collection {
		return true
	}
	if old.Spec.MCP.AuthSecretRef.Name != new.Spec.MCP.AuthSecretRef.Name ||
		old.Spec.MCP.AuthSecretRef.Key != new.Spec.MCP.AuthSecretRef.Key {
		return true
	}
	if !stringSlicesEqual(old.Spec.IncludeResources, new.Spec.IncludeResources) {
		return true
	}
	if !stringSlicesEqual(old.Spec.ExcludeResources, new.Spec.ExcludeResources) {
		return true
	}
	if old.GetDebounceWindowSeconds() != new.GetDebounceWindowSeconds() {
		return true
	}
	return false
}

// stringSlicesEqual checks if two string slices are equal
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// removeConfig removes a config from active configs and stops its buffer
func (r *CapabilityScanReconciler) removeConfig(key string) {
	r.configsMu.Lock()
	defer r.configsMu.Unlock()

	if state, exists := r.activeConfigs[key]; exists {
		// Cancel the buffer context to stop the goroutine
		if state.bufferCancel != nil {
			state.bufferCancel()
		}
	}
	delete(r.activeConfigs, key)
}

// performStartupReconciliation reconciles cluster resources with MCP capabilities on startup
// It computes the diff between cluster state and MCP state, then:
// - Scans missing resources and deletes orphaned capabilities
// Uses Discovery API to find ALL resources (core + CRDs) with filters applied
func (r *CapabilityScanReconciler) performStartupReconciliation(ctx context.Context, state *capabilityScanState, configKey string) {
	logger := logf.Log.WithName("capabilityscan")

	logger.Info("Starting reconciliation", "config", configKey)

	// List all resources in cluster using Discovery API (with filters applied)
	clusterResources, err := r.listAllResourceIDs(ctx, state.config)
	if err != nil {
		logger.Error(err, "❌ Failed to list cluster resources")
		r.updateStatusByKey(ctx, configKey, false, err.Error())
		return
	}
	logger.Info("Found cluster resources", "count", len(clusterResources))

	// List all capability IDs from MCP
	mcpCapabilities, err := state.mcpClient.ListCapabilityIDs(ctx)
	if err != nil {
		logger.Error(err, "❌ Failed to list MCP capabilities")
		r.updateStatusByKey(ctx, configKey, false, err.Error())
		return
	}
	logger.Info("Found MCP capabilities", "count", len(mcpCapabilities))

	// Compute diff
	toScan, toDelete := computeCapabilityDiff(clusterResources, mcpCapabilities)

	logger.Info("Computed capability diff",
		"toScan", len(toScan),
		"toDelete", len(toDelete),
	)

	// Scan missing resources (if any)
	if len(toScan) > 0 {
		resourceList := strings.Join(toScan, ",")
		logger.Info("Scanning missing resources", "count", len(toScan), "resources", resourceList)
		if err := state.mcpClient.TriggerScan(ctx, resourceList); err != nil {
			logger.Error(err, "❌ Failed to trigger targeted scan")
			r.updateStatusByKey(ctx, configKey, false, err.Error())
			// Continue to delete orphaned capabilities even if scan fails
		} else {
			logger.Info("✅ Targeted scan triggered successfully")
		}
	}

	// Delete orphaned capabilities (if any)
	if len(toDelete) > 0 {
		logger.Info("Deleting orphaned capabilities", "count", len(toDelete))
		for _, id := range toDelete {
			if err := state.mcpClient.DeleteCapability(ctx, id); err != nil {
				logger.Error(err, "❌ Failed to delete orphaned capability", "id", id)
				// Continue deleting others even if one fails
			} else {
				logger.V(1).Info("Deleted orphaned capability", "id", id)
			}
		}
		logger.Info("✅ Orphaned capabilities cleanup complete")
	}

	if len(toScan) == 0 && len(toDelete) == 0 {
		logger.Info("✅ Cluster and MCP are in sync, no action needed")
	}

	r.updateLastScanTime(ctx, configKey)
}

// computeCapabilityDiff computes the difference between cluster resources and MCP capabilities
// Returns: (toScan, toDelete) where:
// - toScan: resources in cluster but not in MCP (need to be scanned)
// - toDelete: capabilities in MCP but not in cluster (need to be deleted)
func computeCapabilityDiff(clusterCRDs, mcpCapabilities []string) (toScan, toDelete []string) {
	clusterSet := make(map[string]struct{}, len(clusterCRDs))
	for _, id := range clusterCRDs {
		clusterSet[id] = struct{}{}
	}

	mcpSet := make(map[string]struct{}, len(mcpCapabilities))
	for _, id := range mcpCapabilities {
		mcpSet[id] = struct{}{}
	}

	// CRDs in cluster but not in MCP → need to scan
	for _, id := range clusterCRDs {
		if _, exists := mcpSet[id]; !exists {
			toScan = append(toScan, id)
		}
	}

	// Capabilities in MCP but not in cluster → need to delete
	for _, id := range mcpCapabilities {
		if _, exists := clusterSet[id]; !exists {
			toDelete = append(toDelete, id)
		}
	}

	return toScan, toDelete
}

// HandleCRDEvent processes CRD create/delete events by queuing them to the debounce buffer
func (r *CapabilityScanReconciler) HandleCRDEvent(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition, isDelete bool) {
	logger := logf.FromContext(ctx).WithName("capabilityscan")

	// Get the resource identifier (Kind.group)
	resourceID := buildCapabilityID(crd)

	if isDelete {
		logger.Info("CRD deleted", "crd", crd.Name, "resourceID", resourceID)
	} else {
		logger.Info("CRD created/updated", "crd", crd.Name, "resourceID", resourceID)
	}

	// Process for all active configs
	r.configsMu.RLock()
	configs := make(map[string]*capabilityScanState, len(r.activeConfigs))
	for k, v := range r.activeConfigs {
		configs[k] = v
	}
	r.configsMu.RUnlock()

	if len(configs) == 0 {
		logger.V(1).Info("No active configs, ignoring CRD event")
		return
	}

	for key, state := range configs {
		// Check if this CRD matches include/exclude filters
		if !r.shouldProcessResource(state.config, resourceID) {
			logger.V(1).Info("CRD excluded by filters", "crd", crd.Name, "config", key)
			continue
		}

		// Queue the change to the debounce buffer
		change := &CRDChange{
			ResourceID: resourceID,
			IsDelete:   isDelete,
		}

		select {
		case state.buffer.ChangeQueue() <- change:
			logger.V(1).Info("Queued CRD change", "crd", crd.Name, "isDelete", isDelete, "config", key)
		default:
			// Buffer is full, log warning but don't block
			logger.Info("⚠️ Debounce buffer full, dropping CRD event", "crd", crd.Name, "config", key)
		}
	}
}

// buildCapabilityID builds the capability ID from a CRD
// Format: Kind.group for grouped resources
func buildCapabilityID(crd *apiextensionsv1.CustomResourceDefinition) string {
	kind := crd.Spec.Names.Kind
	group := crd.Spec.Group
	if group == "" {
		return kind
	}
	return kind + "." + group
}

// listClusterCRDIDs lists all CRDs in the cluster and returns their capability IDs
func (r *CapabilityScanReconciler) listClusterCRDIDs(ctx context.Context, config *dotaiv1alpha1.CapabilityScanConfig) ([]string, error) {
	var crdList apiextensionsv1.CustomResourceDefinitionList
	if err := r.List(ctx, &crdList); err != nil {
		return nil, fmt.Errorf("failed to list CRDs: %w", err)
	}

	var ids []string
	for _, crd := range crdList.Items {
		id := buildCapabilityID(&crd)
		// Apply include/exclude filters
		if r.shouldProcessResource(config, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// listAllResourceIDs lists ALL resource types (core + CRDs) using Discovery API
// and returns their capability IDs with filters applied
func (r *CapabilityScanReconciler) listAllResourceIDs(ctx context.Context, config *dotaiv1alpha1.CapabilityScanConfig) ([]string, error) {
	logger := logf.FromContext(ctx).WithName("capabilityscan")

	if r.discoveryClient == nil {
		return nil, fmt.Errorf("discovery client not initialized")
	}

	// Get all API resources using Discovery API
	_, resources, err := r.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		// Discovery can return partial results with errors for unavailable API groups
		if !discovery.IsGroupDiscoveryFailedError(err) {
			return nil, fmt.Errorf("failed to discover API resources: %w", err)
		}
		logger.V(1).Info("Partial discovery failure (some API groups unavailable)", "error", err)
	}

	var ids []string
	seen := make(map[string]bool) // Deduplicate resources across versions

	for _, resourceList := range resources {
		if resourceList == nil {
			continue
		}

		// Parse the group/version from the API list
		gv := resourceList.GroupVersion
		parts := strings.Split(gv, "/")
		var group string
		if len(parts) == 2 {
			group = parts[0] // e.g., "apps" from "apps/v1"
		} else {
			group = "" // core API (v1)
		}

		for _, resource := range resourceList.APIResources {
			// Skip subresources (e.g., pods/log, pods/exec)
			if strings.Contains(resource.Name, "/") {
				continue
			}

			// Build capability ID: Kind for core, Kind.group for others
			var id string
			if group == "" {
				id = resource.Kind
			} else {
				id = resource.Kind + "." + group
			}

			// Deduplicate (same resource may appear in multiple versions)
			if seen[id] {
				continue
			}
			seen[id] = true

			// Apply include/exclude filters
			if r.shouldProcessResource(config, id) {
				ids = append(ids, id)
			}
		}
	}

	return ids, nil
}

// shouldProcessResource checks if a resource matches include/exclude filters
func (r *CapabilityScanReconciler) shouldProcessResource(config *dotaiv1alpha1.CapabilityScanConfig, resourceID string) bool {
	// If include list is specified, resource must match at least one pattern
	if len(config.Spec.IncludeResources) > 0 {
		matched := false
		for _, pattern := range config.Spec.IncludeResources {
			if matchesPattern(resourceID, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Check exclude list
	for _, pattern := range config.Spec.ExcludeResources {
		if matchesPattern(resourceID, pattern) {
			return false
		}
	}

	return true
}

// matchesPattern checks if a resource ID matches a pattern with wildcard support
// Pattern format: "Kind.group" or "*.group" or "Kind.*" or "*"
func matchesPattern(resourceID, pattern string) bool {
	// Exact match
	if resourceID == pattern {
		return true
	}

	// Wildcard match all
	if pattern == "*" {
		return true
	}

	// Split into parts
	resourceParts := strings.SplitN(resourceID, ".", 2)
	patternParts := strings.SplitN(pattern, ".", 2)

	// Handle Kind-only patterns (core resources)
	if len(patternParts) == 1 {
		if len(resourceParts) == 1 {
			return patternParts[0] == "*" || patternParts[0] == resourceParts[0]
		}
		return false
	}

	// Handle Kind.group patterns
	if len(resourceParts) != 2 {
		return false
	}

	kindMatch := patternParts[0] == "*" || patternParts[0] == resourceParts[0]
	groupMatch := patternParts[1] == "*" || matchesGroupPattern(resourceParts[1], patternParts[1])

	return kindMatch && groupMatch
}

// matchesGroupPattern checks if a group matches a pattern
// Supports patterns like "*.crossplane.io" matching "database.aws.crossplane.io"
func matchesGroupPattern(group, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == group {
		return true
	}
	// Check suffix match for patterns like "*.crossplane.io"
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".crossplane.io"
		return strings.HasSuffix(group, suffix)
	}
	// Also support suffix matching without explicit "*." prefix
	// Pattern "crossplane.io" matches "database.aws.crossplane.io"
	// This handles cases where Kind.* pattern becomes Kind=*, Group=suffix
	if strings.HasSuffix(group, "."+pattern) {
		return true
	}
	return false
}

// updateStatus updates the CapabilityScanConfig status
func (r *CapabilityScanReconciler) updateStatus(ctx context.Context, config *dotaiv1alpha1.CapabilityScanConfig, ready bool, lastError string) {
	logger := logf.FromContext(ctx)

	// Fetch fresh copy to avoid conflicts
	fresh := &dotaiv1alpha1.CapabilityScanConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: config.Namespace, Name: config.Name}, fresh); err != nil {
		logger.Error(err, "Failed to fetch CapabilityScanConfig for status update")
		return
	}

	fresh.Status.LastError = lastError

	// Update Ready condition
	now := metav1.NewTime(time.Now())
	var readyCondition metav1.Condition

	if ready && lastError == "" {
		readyCondition = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "ConfigActive",
			Message:            "CRD watcher is active",
		}
	} else {
		reason := "Error"
		message := lastError
		if message == "" {
			message = "Configuration error"
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

	if err := r.Status().Update(ctx, fresh); err != nil {
		if apierrors.IsConflict(err) {
			logger.V(1).Info("Conflict updating status, will retry on next reconcile")
			return
		}
		logger.Error(err, "Failed to update CapabilityScanConfig status")
	}
}

// updateStatusByKey updates status using the config key
func (r *CapabilityScanReconciler) updateStatusByKey(ctx context.Context, key string, ready bool, lastError string) {
	namespace, name := parseConfigKey(key)
	var config dotaiv1alpha1.CapabilityScanConfig
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &config); err != nil {
		return
	}
	r.updateStatus(ctx, &config, ready, lastError)
}

// updateLastScanTime updates the last scan time
func (r *CapabilityScanReconciler) updateLastScanTime(ctx context.Context, key string) {
	logger := logf.Log.WithName("capabilityscan")

	namespace, name := parseConfigKey(key)
	var config dotaiv1alpha1.CapabilityScanConfig
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &config); err != nil {
		logger.Error(err, "Failed to fetch CapabilityScanConfig for last scan time update")
		return
	}

	now := metav1.NewTime(time.Now())
	config.Status.LastScanTime = &now
	config.Status.LastError = ""

	if err := r.Status().Update(ctx, &config); err != nil {
		if !apierrors.IsConflict(err) {
			logger.Error(err, "Failed to update last scan time")
		}
	}
}

// capabilityScanConfigKey returns a unique key for a CapabilityScanConfig (namespace/name)
func capabilityScanConfigKey(config *dotaiv1alpha1.CapabilityScanConfig) string {
	return config.Namespace + "/" + config.Name
}

// SetupWithManager sets up the controller with the Manager
func (r *CapabilityScanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dotaiv1alpha1.CapabilityScanConfig{}).
		Watches(
			&apiextensionsv1.CustomResourceDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.mapCRDToRequests),
		).
		Named("capabilityscan").
		Complete(r)
}

// mapCRDToRequests handles CRD events and triggers scans
func (r *CapabilityScanReconciler) mapCRDToRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := logf.FromContext(ctx).WithName("capabilityscan")

	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil
	}

	// Check if any active configs exist
	r.configsMu.RLock()
	hasActiveConfigs := len(r.activeConfigs) > 0
	r.configsMu.RUnlock()

	if !hasActiveConfigs {
		return nil
	}

	// Determine if this is a delete event by checking DeletionTimestamp
	isDelete := !crd.DeletionTimestamp.IsZero()

	logger.Info("Processing CRD event", "crd", crd.Name, "isDelete", isDelete)

	// Handle the CRD event (trigger scan or delete)
	r.HandleCRDEvent(ctx, crd, isDelete)

	// Don't enqueue any reconcile requests - we handle CRD events directly
	return nil
}
