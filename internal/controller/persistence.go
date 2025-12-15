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
type CooldownPersistence struct {
	client       client.Client
	scheme       *runtime.Scheme
	syncInterval time.Duration
	minDuration  time.Duration
	enabled      bool

	mu           sync.RWMutex
	dirtyEntries map[string]bool // Full keys that need sync

	stopCh chan struct{}
	doneCh chan struct{}
}

// CooldownPersistenceConfig holds configuration for CooldownPersistence
type CooldownPersistenceConfig struct {
	Enabled      bool
	SyncInterval time.Duration
	MinDuration  time.Duration
}

// NewCooldownPersistence creates a new CooldownPersistence instance
func NewCooldownPersistence(c client.Client, scheme *runtime.Scheme, config CooldownPersistenceConfig) *CooldownPersistence {
	if config.SyncInterval == 0 {
		config.SyncInterval = DefaultCooldownSyncInterval
	}
	if config.MinDuration == 0 {
		config.MinDuration = DefaultMinPersistDuration
	}

	return &CooldownPersistence{
		client:       c,
		scheme:       scheme,
		syncInterval: config.SyncInterval,
		minDuration:  config.MinDuration,
		enabled:      config.Enabled,
		dirtyEntries: make(map[string]bool),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// IsEnabled returns whether persistence is enabled
func (p *CooldownPersistence) IsEnabled() bool {
	return p.enabled
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

// Load restores cooldown state from all RemediationPolicy ConfigMaps.
// Returns a map with full keys (policy-ns/policy-name/obj-ns/obj-name/reason).
func (p *CooldownPersistence) Load(ctx context.Context) map[string]time.Time {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	if !p.enabled {
		logger.V(1).Info("Cooldown persistence disabled, skipping load")
		return make(map[string]time.Time)
	}

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

	for _, policy := range policies.Items {
		loaded, pruned := p.loadPolicyState(ctx, &policy, cooldowns, now)
		totalLoaded += loaded
		totalPruned += pruned
	}

	logger.Info("Loaded cooldown state from ConfigMaps",
		"policies", len(policies.Items),
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
func (p *CooldownPersistence) MarkDirty(fullKey string, cooldownEnd time.Time) {
	if !p.enabled {
		return
	}

	// Only persist cooldowns with sufficient remaining duration
	if time.Until(cooldownEnd) < p.minDuration {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.dirtyEntries[fullKey] = true
}

// Sync writes dirty cooldown entries to per-policy ConfigMaps.
func (p *CooldownPersistence) Sync(ctx context.Context, cooldowns map[string]time.Time) error {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	if !p.enabled {
		return nil
	}

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
		if cooldownEnd.Sub(now) < p.minDuration {
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

	// Get the policy for ownerReference
	policy := &dotaiv1alpha1.RemediationPolicy{}
	if err := p.client.Get(ctx, client.ObjectKey{Namespace: policyNs, Name: policyName}, policy); err != nil {
		if apierrors.IsNotFound(err) {
			// Policy deleted, skip
			return nil
		}
		return fmt.Errorf("failed to get policy %s/%s: %w", policyNs, policyName, err)
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

	if !p.enabled {
		logger.Info("Cooldown persistence disabled, not starting periodic sync")
		close(p.doneCh)
		return
	}

	logger.Info("Starting periodic cooldown sync",
		"interval", p.syncInterval,
		"minDuration", p.minDuration)

	go func() {
		defer close(p.doneCh)

		ticker := time.NewTicker(p.syncInterval)
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

// Stop stops periodic sync and performs a final sync
func (p *CooldownPersistence) Stop(ctx context.Context, cooldowns map[string]time.Time) {
	logger := logf.FromContext(ctx).WithName("cooldown-persistence")

	if !p.enabled {
		return
	}

	logger.Info("Stopping cooldown persistence, performing final sync")

	close(p.stopCh)

	select {
	case <-p.doneCh:
	case <-time.After(5 * time.Second):
		logger.Info("Timeout waiting for periodic sync to stop")
	}

	// Mark all for final sync
	p.mu.Lock()
	for key := range cooldowns {
		p.dirtyEntries[key] = true
	}
	p.mu.Unlock()

	if err := p.Sync(ctx, cooldowns); err != nil {
		logger.Error(err, "Final sync failed")
	}
}

// GetDirtyCount returns pending dirty entries count (for testing)
func (p *CooldownPersistence) GetDirtyCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.dirtyEntries)
}
