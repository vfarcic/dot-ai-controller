package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

const (
	// DefaultCooldownSyncInterval is the default interval for syncing cooldown state
	DefaultCooldownSyncInterval = 60 * time.Second

	// DefaultMinPersistDuration is the minimum cooldown duration to persist
	DefaultMinPersistDuration = 1 * time.Hour

	// configMapSuffix is appended to CR name for the ConfigMap
	configMapSuffix = "-cooldown-state"

	// configMapDataKey is the key used for cooldown data in the ConfigMap
	configMapDataKey = "cooldowns"

	// configMapLastSyncKey is the key used for last sync timestamp
	configMapLastSyncKey = "lastSync"

	// configMapVersionKey is the key used for format version
	configMapVersionKey = "version"

	// currentVersion is the current persistence format version
	currentVersion = "1"
)

// CooldownPersistence handles per-CR ConfigMap-based state persistence.
// Each RemediationPolicy CR gets its own ConfigMap with ownerReference
// for automatic cleanup when the CR is deleted.
//
// Persistence is enabled by default for all policies. Individual policies
// can opt out by setting spec.persistence.enabled=false.
type CooldownPersistence struct {
	client client.Client
	scheme *runtime.Scheme

	mu           sync.RWMutex
	dirtyEntries map[string]bool // Full keys that need sync

	// getCooldowns is stored during StartPeriodicSync for use during Stop
	getCooldowns func() map[string]time.Time

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewCooldownPersistence creates a new CooldownPersistence instance
func NewCooldownPersistence(c client.Client, scheme *runtime.Scheme) *CooldownPersistence {
	return &CooldownPersistence{
		client:       c,
		scheme:       scheme,
		dirtyEntries: make(map[string]bool),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// getConfigMapName returns the ConfigMap name for a policy
func getConfigMapName(policyName string) string {
	return policyName + configMapSuffix
}

// parseFullKey parses a full cooldown key into its components
// Full key format: policy-ns/policy-name/obj-ns/obj-name/reason
// Returns: policyNs, policyName, shortKey (obj-ns/obj-name/reason)
func parseFullKey(fullKey string) (policyNs, policyName, shortKey string, ok bool) {
	parts := strings.SplitN(fullKey, "/", 5)
	if len(parts) < 5 {
		return "", "", "", false
	}
	return parts[0], parts[1], strings.Join(parts[2:], "/"), true
}

// makeFullKey creates a full key from components
func makeFullKey(policyNs, policyName, shortKey string) string {
	return fmt.Sprintf("%s/%s/%s", policyNs, policyName, shortKey)
}

// IsPolicyPersistenceEnabled checks if persistence is enabled for a policy.
// Returns true if persistence is not explicitly disabled (default is enabled).
func IsPolicyPersistenceEnabled(policy *dotaiv1alpha1.RemediationPolicy) bool {
	if policy.Spec.Persistence == nil {
		return true // Default: enabled
	}
	if policy.Spec.Persistence.Enabled == nil {
		return true // Default: enabled
	}
	return *policy.Spec.Persistence.Enabled
}

// Load restores cooldown state from all RemediationPolicy ConfigMaps.
// Only loads state for policies that have persistence enabled.
// Returns a map with full keys (policy-ns/policy-name/obj-ns/obj-name/reason).
func (p *CooldownPersistence) Load(ctx context.Context) map[string]time.Time {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	// List all RemediationPolicies
	var policies dotaiv1alpha1.RemediationPolicyList
	if err := p.client.List(ctx, &policies); err != nil {
		logger.Error(err, "Failed to list RemediationPolicies, starting with empty state")
		return make(map[string]time.Time)
	}

	cooldowns := make(map[string]time.Time)
	now := time.Now()
	totalLoaded := 0
	totalPruned := 0
	skippedPolicies := 0

	for _, policy := range policies.Items {
		if !IsPolicyPersistenceEnabled(&policy) {
			skippedPolicies++
			continue
		}
		loaded, pruned := p.loadPolicyState(ctx, &policy, cooldowns, now)
		totalLoaded += loaded
		totalPruned += pruned
	}

	logger.Info("Loaded cooldown state from ConfigMaps",
		"policies", len(policies.Items),
		"skipped", skippedPolicies,
		"loaded", totalLoaded,
		"pruned", totalPruned)

	return cooldowns
}

// loadPolicyState loads state for a single policy's ConfigMap
func (p *CooldownPersistence) loadPolicyState(ctx context.Context, policy *dotaiv1alpha1.RemediationPolicy, cooldowns map[string]time.Time, now time.Time) (loaded, pruned int) {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{
		Namespace: policy.Namespace,
		Name:      getConfigMapName(policy.Name),
	}

	if err := p.client.Get(ctx, key, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to load ConfigMap",
				"policy", policy.Name,
				"namespace", policy.Namespace)
		}
		return 0, 0
	}

	// Check version
	if v, ok := cm.Data[configMapVersionKey]; ok && v != currentVersion {
		logger.Info("ConfigMap version mismatch, skipping",
			"policy", policy.Name,
			"expected", currentVersion,
			"found", v)
		return 0, 0
	}

	// Parse cooldown data
	cooldownsJSON, ok := cm.Data[configMapDataKey]
	if !ok || cooldownsJSON == "" {
		return 0, 0
	}

	var rawCooldowns map[string]string
	if err := json.Unmarshal([]byte(cooldownsJSON), &rawCooldowns); err != nil {
		logger.Error(err, "Failed to parse cooldown JSON",
			"policy", policy.Name)
		return 0, 0
	}

	// Convert to time.Time with full keys, pruning expired
	for shortKey, timestampStr := range rawCooldowns {
		timestamp, err := time.Parse(time.RFC3339, timestampStr)
		if err != nil {
			continue
		}

		if timestamp.Before(now) {
			pruned++
			continue
		}

		fullKey := makeFullKey(policy.Namespace, policy.Name, shortKey)
		cooldowns[fullKey] = timestamp
		loaded++
	}

	return loaded, pruned
}

// MarkDirty flags a cooldown entry for persistence on the next sync.
// The caller (controller) should check IsPolicyPersistenceEnabled before calling this.
func (p *CooldownPersistence) MarkDirty(fullKey string, cooldownEnd time.Time) {
	// Only persist cooldowns with sufficient remaining duration
	if time.Until(cooldownEnd) < DefaultMinPersistDuration {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirtyEntries[fullKey] = true
}

// Sync writes dirty cooldown entries to per-policy ConfigMaps.
// Only syncs policies that have persistence enabled.
func (p *CooldownPersistence) Sync(ctx context.Context, cooldowns map[string]time.Time) error {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	p.mu.RLock()
	hasDirty := len(p.dirtyEntries) > 0
	p.mu.RUnlock()

	if !hasDirty {
		logger.V(1).Info("No dirty entries to sync")
		return nil
	}

	// Group cooldowns by policy
	byPolicy := make(map[string]map[string]time.Time) // policy-ns/policy-name -> shortKey -> time
	now := time.Now()

	for fullKey, cooldownEnd := range cooldowns {
		// Skip expired
		if cooldownEnd.Before(now) {
			continue
		}
		// Only persist long cooldowns
		if cooldownEnd.Sub(now) < DefaultMinPersistDuration {
			continue
		}

		policyNs, policyName, shortKey, ok := parseFullKey(fullKey)
		if !ok {
			continue
		}

		policyKey := policyNs + "/" + policyName
		if byPolicy[policyKey] == nil {
			byPolicy[policyKey] = make(map[string]time.Time)
		}
		byPolicy[policyKey][shortKey] = cooldownEnd
	}

	// Sync each policy's ConfigMap
	var syncErrors []error
	for policyKey, entries := range byPolicy {
		parts := strings.SplitN(policyKey, "/", 2)
		if len(parts) != 2 {
			continue
		}
		policyNs, policyName := parts[0], parts[1]

		if err := p.syncPolicyConfigMap(ctx, policyNs, policyName, entries, now); err != nil {
			syncErrors = append(syncErrors, err)
		}
	}

	// Clear dirty entries after sync attempt
	p.mu.Lock()
	p.dirtyEntries = make(map[string]bool)
	p.mu.Unlock()

	if len(syncErrors) > 0 {
		logger.Error(syncErrors[0], "Some ConfigMap syncs failed",
			"failedCount", len(syncErrors))
		return syncErrors[0]
	}

	return nil
}

// syncPolicyConfigMap syncs state for a single policy
func (p *CooldownPersistence) syncPolicyConfigMap(ctx context.Context, policyNs, policyName string, entries map[string]time.Time, now time.Time) error {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	// Get the policy for ownerReference and persistence check
	policy := &dotaiv1alpha1.RemediationPolicy{}
	if err := p.client.Get(ctx, client.ObjectKey{Namespace: policyNs, Name: policyName}, policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Policy deleted, skip
			return nil
		}
		return fmt.Errorf("failed to get policy %s/%s: %w", policyNs, policyName, err)
	}

	// Skip if persistence is disabled for this policy
	if !IsPolicyPersistenceEnabled(policy) {
		logger.V(1).Info("Persistence disabled for policy, skipping sync",
			"policy", policyName,
			"namespace", policyNs)
		return nil
	}

	// Convert to JSON-serializable format
	serializable := make(map[string]string)
	for shortKey, cooldownEnd := range entries {
		serializable[shortKey] = cooldownEnd.Format(time.RFC3339)
	}

	cooldownsJSON, err := json.Marshal(serializable)
	if err != nil {
		return fmt.Errorf("failed to serialize cooldowns: %w", err)
	}

	// Get or create ConfigMap
	cmName := getConfigMapName(policyName)
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: policyNs, Name: cmName}

	err = p.client.Get(ctx, key, cm)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get ConfigMap: %w", err)
		}

		// Create new ConfigMap
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: policyNs,
				Labels: map[string]string{
					"app.kubernetes.io/component":  "cooldown-state",
					"app.kubernetes.io/managed-by": "dot-ai-controller",
				},
			},
			Data: map[string]string{
				configMapDataKey:     string(cooldownsJSON),
				configMapLastSyncKey: now.Format(time.RFC3339),
				configMapVersionKey:  currentVersion,
			},
		}

		// Set ownerReference for automatic cleanup
		if err := controllerutil.SetControllerReference(policy, cm, p.scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		if err := p.client.Create(ctx, cm); err != nil {
			return fmt.Errorf("failed to create ConfigMap: %w", err)
		}

		logger.Info("Created cooldown ConfigMap",
			"configmap", cmName,
			"namespace", policyNs,
			"entries", len(entries))
		return nil
	}

	// Update existing ConfigMap
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[configMapDataKey] = string(cooldownsJSON)
	cm.Data[configMapLastSyncKey] = now.Format(time.RFC3339)
	cm.Data[configMapVersionKey] = currentVersion

	if err := p.client.Update(ctx, cm); err != nil {
		if apierrors.IsConflict(err) {
			// Retry on next sync
			return nil
		}
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	logger.V(1).Info("Synced cooldown ConfigMap",
		"configmap", cmName,
		"namespace", policyNs,
		"entries", len(entries))

	return nil
}

// StartPeriodicSync starts background periodic syncing
func (p *CooldownPersistence) StartPeriodicSync(ctx context.Context, getCooldowns func() map[string]time.Time) {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	// Store callback for use during Stop
	p.getCooldowns = getCooldowns

	logger.Info("Starting periodic cooldown sync",
		"interval", DefaultCooldownSyncInterval,
		"minDuration", DefaultMinPersistDuration)

	go func() {
		defer close(p.doneCh)

		ticker := time.NewTicker(DefaultCooldownSyncInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stopCh:
				return
			case <-ticker.C:
				if err := p.Sync(ctx, getCooldowns()); err != nil {
					logger.Error(err, "Periodic sync failed")
				}
			}
		}
	}()
}

// Stop stops periodic sync and performs a final sync.
// Uses the getCooldowns callback stored during StartPeriodicSync.
// Creates a fresh context with timeout since the manager context may be cancelled.
func (p *CooldownPersistence) Stop() {
	logger := logf.Log.WithName("cooldown-persistence")

	logger.Info("Stopping cooldown persistence, performing final sync")

	// Signal the periodic sync goroutine to stop
	close(p.stopCh)

	// Wait for goroutine to exit
	select {
	case <-p.doneCh:
	case <-time.After(5 * time.Second):
		logger.Info("Timeout waiting for periodic sync to stop")
	}

	// Get current cooldowns using stored callback
	if p.getCooldowns == nil {
		logger.Info("No getCooldowns callback stored, skipping final sync")
		return
	}
	cooldowns := p.getCooldowns()

	if len(cooldowns) == 0 {
		logger.Info("No cooldowns to sync")
		return
	}

	// Mark all for final sync
	p.mu.Lock()
	for key := range cooldowns {
		p.dirtyEntries[key] = true
	}
	p.mu.Unlock()

	// Create fresh context with timeout for final sync
	// (manager context is likely cancelled at this point)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p.Sync(ctx, cooldowns); err != nil {
		logger.Error(err, "Final sync failed")
	} else {
		logger.Info("Final sync completed", "entries", len(cooldowns))
	}
}

// GetDirtyCount returns pending dirty entries count (for testing)
func (p *CooldownPersistence) GetDirtyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.dirtyEntries)
}
