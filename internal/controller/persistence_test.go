package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("CooldownPersistence", func() {
	Describe("Helper Functions", func() {
		Describe("getConfigMapName", func() {
			It("should append suffix to policy name", func() {
				name := getConfigMapName("my-policy")
				Expect(name).To(Equal("my-policy-cooldown-state"))
			})

			It("should handle empty policy name", func() {
				name := getConfigMapName("")
				Expect(name).To(Equal("-cooldown-state"))
			})

			It("should handle policy name with hyphens", func() {
				name := getConfigMapName("my-long-policy-name")
				Expect(name).To(Equal("my-long-policy-name-cooldown-state"))
			})
		})

		Describe("parseFullKey", func() {
			Context("with valid keys", func() {
				It("should parse a standard 4-part full key", func() {
					// Key format: policy-ns/policy-name/obj-ns/obj-identifier
					policyNs, policyName, shortKey, ok := parseFullKey("default/my-policy/app-ns/my-pod")
					Expect(ok).To(BeTrue())
					Expect(policyNs).To(Equal("default"))
					Expect(policyName).To(Equal("my-policy"))
					Expect(shortKey).To(Equal("app-ns/my-pod"))
				})

				It("should parse key with Kind:Name object identifier", func() {
					// When owner resolution includes kind prefix like Job:my-job
					policyNs, policyName, shortKey, ok := parseFullKey("ns1/policy1/ns2/Job:my-job")
					Expect(ok).To(BeTrue())
					Expect(policyNs).To(Equal("ns1"))
					Expect(policyName).To(Equal("policy1"))
					Expect(shortKey).To(Equal("ns2/Job:my-job"))
				})

				It("should handle extra slashes in object identifier", func() {
					// Full key with 5+ parts - extra parts go into shortKey
					policyNs, policyName, shortKey, ok := parseFullKey("ns/pol/objns/objname/extra")
					Expect(ok).To(BeTrue())
					Expect(policyNs).To(Equal("ns"))
					Expect(policyName).To(Equal("pol"))
					Expect(shortKey).To(Equal("objns/objname/extra"))
				})
			})

			Context("with invalid keys", func() {
				It("should return false for key with only 3 parts", func() {
					_, _, _, ok := parseFullKey("one/two/three")
					Expect(ok).To(BeFalse())
				})

				It("should return false for key with only 2 parts", func() {
					_, _, _, ok := parseFullKey("one/two")
					Expect(ok).To(BeFalse())
				})

				It("should return false for empty string", func() {
					_, _, _, ok := parseFullKey("")
					Expect(ok).To(BeFalse())
				})

				It("should return false for single part", func() {
					_, _, _, ok := parseFullKey("single")
					Expect(ok).To(BeFalse())
				})
			})
		})

		Describe("makeFullKey", func() {
			It("should create correct full key", func() {
				key := makeFullKey("default", "my-policy", "app-ns/my-pod/BackOff")
				Expect(key).To(Equal("default/my-policy/app-ns/my-pod/BackOff"))
			})

			It("should handle empty components", func() {
				key := makeFullKey("", "", "")
				Expect(key).To(Equal("//"))
			})

			It("should be inverse of parseFullKey", func() {
				original := "ns1/policy1/ns2/object/reason"
				policyNs, policyName, shortKey, ok := parseFullKey(original)
				Expect(ok).To(BeTrue())
				reconstructed := makeFullKey(policyNs, policyName, shortKey)
				Expect(reconstructed).To(Equal(original))
			})
		})
	})

	Describe("IsPolicyPersistenceEnabled", func() {
		It("should return true when persistence is nil (default)", func() {
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Persistence: nil,
				},
			}
			Expect(IsPolicyPersistenceEnabled(policy)).To(BeTrue())
		})

		It("should return true when persistence.enabled is nil (default)", func() {
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Persistence: &dotaiv1alpha1.PersistenceConfig{
						Enabled: nil,
					},
				},
			}
			Expect(IsPolicyPersistenceEnabled(policy)).To(BeTrue())
		})

		It("should return true when persistence.enabled is true", func() {
			enabled := true
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Persistence: &dotaiv1alpha1.PersistenceConfig{
						Enabled: &enabled,
					},
				},
			}
			Expect(IsPolicyPersistenceEnabled(policy)).To(BeTrue())
		})

		It("should return false when persistence.enabled is false", func() {
			enabled := false
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Persistence: &dotaiv1alpha1.PersistenceConfig{
						Enabled: &enabled,
					},
				},
			}
			Expect(IsPolicyPersistenceEnabled(policy)).To(BeFalse())
		})
	})

	Describe("MarkDirty", func() {
		var persistence *CooldownPersistence

		BeforeEach(func() {
			persistence = NewCooldownPersistence(k8sClient, scheme.Scheme)
		})

		It("should mark entry as dirty when duration exceeds minimum", func() {
			// Cooldown end time > 1 hour from now
			cooldownEnd := time.Now().Add(2 * time.Hour)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(1))
		})

		It("should not mark entry as dirty when duration is below minimum", func() {
			// Cooldown end time < 1 hour from now
			cooldownEnd := time.Now().Add(30 * time.Minute)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(0))
		})

		It("should not mark entry as dirty when cooldown is exactly at minimum", func() {
			// Exactly 1 hour - still below threshold due to time.Until calculation
			cooldownEnd := time.Now().Add(DefaultMinPersistDuration - time.Second)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(0))
		})

		It("should mark entry as dirty when cooldown is slightly above minimum", func() {
			cooldownEnd := time.Now().Add(DefaultMinPersistDuration + time.Minute)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(1))
		})

		It("should not mark entry as dirty when cooldown is in the past", func() {
			cooldownEnd := time.Now().Add(-1 * time.Hour)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(0))
		})

		It("should handle multiple entries", func() {
			cooldownEnd := time.Now().Add(2 * time.Hour)
			persistence.MarkDirty("ns/policy/objns/obj1/reason", cooldownEnd)
			persistence.MarkDirty("ns/policy/objns/obj2/reason", cooldownEnd)
			persistence.MarkDirty("ns/policy/objns/obj3/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(3))
		})

		It("should not duplicate entries when marked multiple times", func() {
			cooldownEnd := time.Now().Add(2 * time.Hour)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)
			persistence.MarkDirty("ns/policy/objns/obj/reason", cooldownEnd)

			Expect(persistence.GetDirtyCount()).To(Equal(1))
		})

		It("should be thread-safe for concurrent access", func() {
			cooldownEnd := time.Now().Add(2 * time.Hour)
			var wg sync.WaitGroup
			numGoroutines := 100

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					key := fmt.Sprintf("ns/policy/objns/obj%d/reason", idx)
					persistence.MarkDirty(key, cooldownEnd)
				}(i)
			}

			wg.Wait()
			Expect(persistence.GetDirtyCount()).To(Equal(numGoroutines))
		})
	})

	Describe("Load and Sync Integration", func() {
		var (
			persistence *CooldownPersistence
			ctx         context.Context
			testNs      string
		)

		BeforeEach(func() {
			ctx = context.Background()
			testNs = fmt.Sprintf("persist-test-%d", time.Now().UnixNano())
			persistence = NewCooldownPersistence(k8sClient, scheme.Scheme)

			// Create test namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNs},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			// Delete all RemediationPolicies in test namespace
			policies := &dotaiv1alpha1.RemediationPolicyList{}
			if err := k8sClient.List(ctx, policies, client.InNamespace(testNs)); err == nil {
				for _, p := range policies.Items {
					_ = k8sClient.Delete(ctx, &p)
				}
			}

			// Delete all ConfigMaps in test namespace
			cms := &corev1.ConfigMapList{}
			if err := k8sClient.List(ctx, cms, client.InNamespace(testNs)); err == nil {
				for _, cm := range cms.Items {
					_ = k8sClient.Delete(ctx, &cm)
				}
			}

			// Cleanup namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNs},
			}
			_ = k8sClient.Delete(ctx, ns)
		})

		Describe("Load", func() {
			It("should return empty map when no policies exist", func() {
				cooldowns := persistence.Load(ctx)
				Expect(cooldowns).To(BeEmpty())
			})

			It("should return empty map when policy exists but no ConfigMap", func() {
				// Create a RemediationPolicy without a ConfigMap
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				cooldowns := persistence.Load(ctx)
				Expect(cooldowns).To(BeEmpty())
			})

			It("should load cooldowns from existing ConfigMap", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "load-test-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create ConfigMap with cooldown data
				futureTime := time.Now().Add(24 * time.Hour)
				cooldownData := map[string]string{
					"app-ns/my-pod/BackOff": futureTime.Format(time.RFC3339),
				}
				cooldownJSON, err := json.Marshal(cooldownData)
				Expect(err).NotTo(HaveOccurred())

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "load-test-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": string(cooldownJSON),
						"version":   "1",
						"lastSync":  time.Now().Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Load should find the cooldown
				cooldowns := persistence.Load(ctx)
				Expect(cooldowns).To(HaveLen(1))

				expectedKey := makeFullKey(testNs, "load-test-policy", "app-ns/my-pod/BackOff")
				Expect(cooldowns).To(HaveKey(expectedKey))
			})

			It("should prune expired cooldowns during load", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "prune-test-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create ConfigMap with both expired and valid cooldowns
				futureTime := time.Now().Add(24 * time.Hour)
				pastTime := time.Now().Add(-1 * time.Hour)
				cooldownData := map[string]string{
					"app-ns/valid-pod/BackOff":   futureTime.Format(time.RFC3339),
					"app-ns/expired-pod/BackOff": pastTime.Format(time.RFC3339),
				}
				cooldownJSON, err := json.Marshal(cooldownData)
				Expect(err).NotTo(HaveOccurred())

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "prune-test-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": string(cooldownJSON),
						"version":   "1",
						"lastSync":  time.Now().Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Load should return the valid cooldown but not the expired one
				cooldowns := persistence.Load(ctx)

				validKey := makeFullKey(testNs, "prune-test-policy", "app-ns/valid-pod/BackOff")
				expiredKey := makeFullKey(testNs, "prune-test-policy", "app-ns/expired-pod/BackOff")
				Expect(cooldowns).To(HaveKey(validKey))
				Expect(cooldowns).NotTo(HaveKey(expiredKey))
			})

			It("should skip policies with persistence disabled", func() {
				enabled := false
				// Create a RemediationPolicy with persistence disabled
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "disabled-persistence-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						Persistence: &dotaiv1alpha1.PersistenceConfig{
							Enabled: &enabled,
						},
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create ConfigMap with cooldown data
				futureTime := time.Now().Add(24 * time.Hour)
				cooldownData := map[string]string{
					"app-ns/my-pod/BackOff": futureTime.Format(time.RFC3339),
				}
				cooldownJSON, err := json.Marshal(cooldownData)
				Expect(err).NotTo(HaveOccurred())

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "disabled-persistence-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": string(cooldownJSON),
						"version":   "1",
						"lastSync":  time.Now().Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Load should NOT include entries from the policy with persistence disabled
				cooldowns := persistence.Load(ctx)
				disabledKey := makeFullKey(testNs, "disabled-persistence-policy", "app-ns/my-pod/BackOff")
				Expect(cooldowns).NotTo(HaveKey(disabledKey))
			})

			It("should skip ConfigMaps with version mismatch", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "version-mismatch-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create ConfigMap with wrong version
				futureTime := time.Now().Add(24 * time.Hour)
				cooldownData := map[string]string{
					"app-ns/my-pod/BackOff": futureTime.Format(time.RFC3339),
				}
				cooldownJSON, err := json.Marshal(cooldownData)
				Expect(err).NotTo(HaveOccurred())

				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "version-mismatch-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": string(cooldownJSON),
						"version":   "999", // Wrong version
						"lastSync":  time.Now().Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Load should NOT include entries from ConfigMap with wrong version
				cooldowns := persistence.Load(ctx)
				mismatchKey := makeFullKey(testNs, "version-mismatch-policy", "app-ns/my-pod/BackOff")
				Expect(cooldowns).NotTo(HaveKey(mismatchKey))
			})

			It("should handle malformed JSON gracefully", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "malformed-json-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create ConfigMap with malformed JSON
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "malformed-json-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": "{invalid json",
						"version":   "1",
						"lastSync":  time.Now().Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Load should NOT include entries from malformed ConfigMap
				cooldowns := persistence.Load(ctx)
				malformedKey := makeFullKey(testNs, "malformed-json-policy", "app-ns/my-pod/BackOff")
				Expect(cooldowns).NotTo(HaveKey(malformedKey))
			})
		})

		Describe("Sync", func() {
			It("should do nothing when no dirty entries", func() {
				cooldowns := make(map[string]time.Time)
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should create ConfigMap when it does not exist", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-create-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Mark entry as dirty and sync
				fullKey := makeFullKey(testNs, "sync-create-policy", "app-ns/my-pod/BackOff")
				cooldownEnd := time.Now().Add(2 * time.Hour)
				persistence.MarkDirty(fullKey, cooldownEnd)

				cooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Verify ConfigMap was created
				cm := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "sync-create-policy-cooldown-state",
				}, cm)
				Expect(err).NotTo(HaveOccurred())
				Expect(cm.Data).To(HaveKey("cooldowns"))
				Expect(cm.Data).To(HaveKey("version"))
				Expect(cm.Data["version"]).To(Equal("1"))

				// Verify ownerReference is set
				Expect(cm.OwnerReferences).To(HaveLen(1))
				Expect(cm.OwnerReferences[0].Kind).To(Equal("RemediationPolicy"))
				Expect(cm.OwnerReferences[0].Name).To(Equal("sync-create-policy"))
			})

			It("should update existing ConfigMap", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-update-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create existing ConfigMap
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-update-policy-cooldown-state",
						Namespace: testNs,
					},
					Data: map[string]string{
						"cooldowns": "{}",
						"version":   "1",
						"lastSync":  time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
					},
				}
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())

				// Mark entry as dirty and sync
				fullKey := makeFullKey(testNs, "sync-update-policy", "app-ns/my-pod/BackOff")
				cooldownEnd := time.Now().Add(2 * time.Hour)
				persistence.MarkDirty(fullKey, cooldownEnd)

				cooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Verify ConfigMap was updated
				updatedCm := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "sync-update-policy-cooldown-state",
				}, updatedCm)
				Expect(err).NotTo(HaveOccurred())

				// Parse and verify cooldowns
				var storedCooldowns map[string]string
				err = json.Unmarshal([]byte(updatedCm.Data["cooldowns"]), &storedCooldowns)
				Expect(err).NotTo(HaveOccurred())
				Expect(storedCooldowns).To(HaveKey("app-ns/my-pod/BackOff"))
			})

			It("should skip sync for policies with persistence disabled", func() {
				enabled := false
				// Create a RemediationPolicy with persistence disabled
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-disabled-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						Persistence: &dotaiv1alpha1.PersistenceConfig{
							Enabled: &enabled,
						},
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Mark entry as dirty and sync
				fullKey := makeFullKey(testNs, "sync-disabled-policy", "app-ns/my-pod/BackOff")
				cooldownEnd := time.Now().Add(2 * time.Hour)
				persistence.MarkDirty(fullKey, cooldownEnd)

				cooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Verify ConfigMap was NOT created
				cm := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "sync-disabled-policy-cooldown-state",
				}, cm)
				Expect(err).To(HaveOccurred()) // Should be NotFound
			})

			It("should skip cooldowns below minimum duration during sync", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-min-duration-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Manually add a dirty entry (bypassing MarkDirty's duration check)
				persistence.mu.Lock()
				shortKey := makeFullKey(testNs, "sync-min-duration-policy", "app-ns/short-pod/BackOff")
				persistence.dirtyEntries[shortKey] = true
				persistence.mu.Unlock()

				// Cooldown with short duration
				cooldowns := map[string]time.Time{
					shortKey: time.Now().Add(30 * time.Minute), // Below 1 hour minimum
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// ConfigMap should not be created because cooldown is below minimum
				cm := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "sync-min-duration-policy-cooldown-state",
				}, cm)
				Expect(err).To(HaveOccurred()) // Should be NotFound
			})

			It("should clear dirty entries after sync", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sync-clear-dirty-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Mark entry as dirty
				fullKey := makeFullKey(testNs, "sync-clear-dirty-policy", "app-ns/my-pod/BackOff")
				cooldownEnd := time.Now().Add(2 * time.Hour)
				persistence.MarkDirty(fullKey, cooldownEnd)
				Expect(persistence.GetDirtyCount()).To(Equal(1))

				cooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Dirty entries should be cleared
				Expect(persistence.GetDirtyCount()).To(Equal(0))
			})

			It("should handle policy deletion gracefully", func() {
				// Mark entry as dirty for non-existent policy
				fullKey := makeFullKey(testNs, "deleted-policy", "app-ns/my-pod/BackOff")
				persistence.mu.Lock()
				persistence.dirtyEntries[fullKey] = true
				persistence.mu.Unlock()

				cooldowns := map[string]time.Time{
					fullKey: time.Now().Add(2 * time.Hour),
				}

				// Should not error - just skip the deleted policy
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should sync multiple policies", func() {
				// Create two RemediationPolicies
				policy1 := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-sync-policy-1",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy1)).To(Succeed())

				policy2 := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-sync-policy-2",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "Failed"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy2)).To(Succeed())

				// Mark entries as dirty for both policies
				cooldownEnd := time.Now().Add(2 * time.Hour)
				fullKey1 := makeFullKey(testNs, "multi-sync-policy-1", "app-ns/pod1/BackOff")
				fullKey2 := makeFullKey(testNs, "multi-sync-policy-2", "app-ns/pod2/Failed")
				persistence.MarkDirty(fullKey1, cooldownEnd)
				persistence.MarkDirty(fullKey2, cooldownEnd)

				cooldowns := map[string]time.Time{
					fullKey1: cooldownEnd,
					fullKey2: cooldownEnd,
				}
				err := persistence.Sync(ctx, cooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Verify both ConfigMaps were created
				cm1 := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "multi-sync-policy-1-cooldown-state",
				}, cm1)
				Expect(err).NotTo(HaveOccurred())

				cm2 := &corev1.ConfigMap{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Namespace: testNs,
					Name:      "multi-sync-policy-2-cooldown-state",
				}, cm2)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Describe("Load and Sync Round-Trip", func() {
			It("should restore state after sync and reload", func() {
				// Create a RemediationPolicy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "round-trip-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Create initial cooldown state
				cooldownEnd := time.Now().Add(2 * time.Hour)
				fullKey := makeFullKey(testNs, "round-trip-policy", "app-ns/my-pod/BackOff")
				persistence.MarkDirty(fullKey, cooldownEnd)

				originalCooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}

				// Sync to ConfigMap
				err := persistence.Sync(ctx, originalCooldowns)
				Expect(err).NotTo(HaveOccurred())

				// Create new persistence instance (simulating restart)
				newPersistence := NewCooldownPersistence(k8sClient, scheme.Scheme)

				// Load from ConfigMap
				loadedCooldowns := newPersistence.Load(ctx)

				// Verify the state was restored - check specific key rather than count
				Expect(loadedCooldowns).To(HaveKey(fullKey))

				// Timestamps should be close (within a second due to RFC3339 precision)
				timeDiff := loadedCooldowns[fullKey].Sub(cooldownEnd)
				Expect(timeDiff.Abs()).To(BeNumerically("<", time.Second))
			})
		})
	})

	Describe("Lifecycle", func() {
		var (
			persistence *CooldownPersistence
			ctx         context.Context
			cancel      context.CancelFunc
		)

		BeforeEach(func() {
			ctx, cancel = context.WithCancel(context.Background())
			persistence = NewCooldownPersistence(k8sClient, scheme.Scheme)
		})

		AfterEach(func() {
			cancel()
		})

		Describe("NewCooldownPersistence", func() {
			It("should create instance with initialized fields", func() {
				p := NewCooldownPersistence(k8sClient, scheme.Scheme)
				Expect(p).NotTo(BeNil())
				Expect(p.client).NotTo(BeNil())
				Expect(p.scheme).NotTo(BeNil())
				Expect(p.dirtyEntries).NotTo(BeNil())
				Expect(p.stopCh).NotTo(BeNil())
				Expect(p.doneCh).NotTo(BeNil())
				Expect(p.GetDirtyCount()).To(Equal(0))
			})
		})

		Describe("StartPeriodicSync", func() {
			It("should store getCooldowns callback", func() {
				getCooldowns := func() map[string]time.Time {
					return make(map[string]time.Time)
				}

				persistence.StartPeriodicSync(ctx, getCooldowns)

				// Callback should be stored
				Expect(persistence.getCooldowns).NotTo(BeNil())

				// Clean up
				cancel()
				Eventually(func() bool {
					select {
					case <-persistence.doneCh:
						return true
					default:
						return false
					}
				}, 5*time.Second).Should(BeTrue())
			})

			It("should stop when context is cancelled", func() {
				getCooldowns := func() map[string]time.Time {
					return make(map[string]time.Time)
				}

				persistence.StartPeriodicSync(ctx, getCooldowns)

				// Cancel context
				cancel()

				// Should exit gracefully
				Eventually(func() bool {
					select {
					case <-persistence.doneCh:
						return true
					default:
						return false
					}
				}, 5*time.Second).Should(BeTrue())
			})
		})

		Describe("Stop", func() {
			It("should handle Stop when getCooldowns is nil", func() {
				// Don't call StartPeriodicSync, so getCooldowns is nil
				// Stop should not panic
				Expect(func() {
					persistence.Stop()
				}).NotTo(Panic())
			})

			It("should perform final sync on Stop", func() {
				testNs := fmt.Sprintf("stop-test-%d", time.Now().UnixNano())

				// Create namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: testNs},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())

				// Create policy
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "stop-test-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					},
				}
				Expect(k8sClient.Create(ctx, policy)).To(Succeed())

				// Set up cooldowns
				fullKey := makeFullKey(testNs, "stop-test-policy", "app-ns/my-pod/BackOff")
				cooldownEnd := time.Now().Add(2 * time.Hour)
				cooldowns := map[string]time.Time{
					fullKey: cooldownEnd,
				}

				getCooldowns := func() map[string]time.Time {
					return cooldowns
				}

				persistence.StartPeriodicSync(ctx, getCooldowns)

				// Stop should trigger final sync
				persistence.Stop()

				// Verify ConfigMap was created during final sync
				cm := &corev1.ConfigMap{}
				err := k8sClient.Get(context.Background(), client.ObjectKey{
					Namespace: testNs,
					Name:      "stop-test-policy-cooldown-state",
				}, cm)
				Expect(err).NotTo(HaveOccurred())
				Expect(cm.Data).To(HaveKey("cooldowns"))

				// Cleanup
				_ = k8sClient.Delete(context.Background(), ns)
			})
		})
	})
})
