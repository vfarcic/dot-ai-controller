package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

func intPtr(i int) *int { return &i }

var _ = Describe("RemediationPolicy Occurrence Threshold", func() {
	var (
		reconciler *RemediationPolicyReconciler
		ctx        context.Context
		testNs     string
	)

	BeforeEach(func() {
		ctx = context.Background()
		testNs = fmt.Sprintf("occurrence-test-%d", time.Now().UnixNano())
		reconciler = &RemediationPolicyReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			Recorder:   record.NewFakeRecorder(100),
			HttpClient: &http.Client{Timeout: 30 * time.Second},
		}

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNs}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	})

	AfterEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNs}}
		_ = k8sClient.Delete(ctx, ns)
	})

	Describe("getEffectiveMinOccurrences", func() {
		It("returns selector value when set", func() {
			selector := dotaiv1alpha1.EventSelector{MinOccurrences: intPtr(3)}
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{MinOccurrences: intPtr(5)},
			}
			Expect(reconciler.getEffectiveMinOccurrences(selector, policy)).To(Equal(3))
		})

		It("falls back to policy value when selector is unset", func() {
			selector := dotaiv1alpha1.EventSelector{}
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{MinOccurrences: intPtr(5)},
			}
			Expect(reconciler.getEffectiveMinOccurrences(selector, policy)).To(Equal(5))
		})

		It("returns 0 when neither is set", func() {
			selector := dotaiv1alpha1.EventSelector{}
			policy := &dotaiv1alpha1.RemediationPolicy{}
			Expect(reconciler.getEffectiveMinOccurrences(selector, policy)).To(Equal(0))
		})

		It("respects an explicit selector zero overriding a non-zero policy default", func() {
			selector := dotaiv1alpha1.EventSelector{MinOccurrences: intPtr(0)}
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{MinOccurrences: intPtr(5)},
			}
			Expect(reconciler.getEffectiveMinOccurrences(selector, policy)).To(Equal(0),
				"explicit selector 0 should disable filtering even when policy default is set")
		})
	})

	Describe("getEffectiveOccurrenceWindow", func() {
		It("uses selector window when set", func() {
			selector := dotaiv1alpha1.EventSelector{OccurrenceWindowSeconds: intPtr(60)}
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{OccurrenceWindowSeconds: intPtr(600)},
			}
			Expect(reconciler.getEffectiveOccurrenceWindow(selector, policy)).To(Equal(60 * time.Second))
		})

		It("falls back to policy window when selector is unset", func() {
			selector := dotaiv1alpha1.EventSelector{}
			policy := &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{OccurrenceWindowSeconds: intPtr(600)},
			}
			Expect(reconciler.getEffectiveOccurrenceWindow(selector, policy)).To(Equal(600 * time.Second))
		})

		It("uses default of 5 minutes when neither is set", func() {
			selector := dotaiv1alpha1.EventSelector{}
			policy := &dotaiv1alpha1.RemediationPolicy{}
			Expect(reconciler.getEffectiveOccurrenceWindow(selector, policy)).
				To(Equal(time.Duration(DefaultOccurrenceWindowSeconds) * time.Second))
		})
	})

	Describe("recordOccurrenceAndCheckThreshold", func() {
		It("blocks until threshold reached", func() {
			key := "test-key-1"

			allowed, count := reconciler.recordOccurrenceAndCheckThreshold(key, 3, time.Minute)
			Expect(allowed).To(BeFalse())
			Expect(count).To(Equal(1))

			allowed, count = reconciler.recordOccurrenceAndCheckThreshold(key, 3, time.Minute)
			Expect(allowed).To(BeFalse())
			Expect(count).To(Equal(2))

			allowed, count = reconciler.recordOccurrenceAndCheckThreshold(key, 3, time.Minute)
			Expect(allowed).To(BeTrue())
			Expect(count).To(Equal(3))
		})

		It("prunes occurrences outside the window", func() {
			key := "test-key-2"

			reconciler.occurrenceMu.Lock()
			if reconciler.occurrenceTracking == nil {
				reconciler.occurrenceTracking = make(map[string][]time.Time)
			}
			reconciler.occurrenceTracking[key] = []time.Time{time.Now().Add(-1 * time.Hour)}
			reconciler.occurrenceMu.Unlock()

			allowed, count := reconciler.recordOccurrenceAndCheckThreshold(key, 2, 5*time.Minute)
			Expect(allowed).To(BeFalse())
			Expect(count).To(Equal(1))
		})

		It("tracks different keys independently", func() {
			allowed1, count1 := reconciler.recordOccurrenceAndCheckThreshold("key-A", 2, time.Minute)
			Expect(allowed1).To(BeFalse())
			Expect(count1).To(Equal(1))

			allowed2, count2 := reconciler.recordOccurrenceAndCheckThreshold("key-B", 2, time.Minute)
			Expect(allowed2).To(BeFalse())
			Expect(count2).To(Equal(1))
		})
	})

	Describe("resetOccurrenceCount", func() {
		It("clears the counter for a key", func() {
			key := "test-reset-key"
			reconciler.recordOccurrenceAndCheckThreshold(key, 5, time.Minute)
			reconciler.recordOccurrenceAndCheckThreshold(key, 5, time.Minute)

			reconciler.resetOccurrenceCount(key)

			allowed, count := reconciler.recordOccurrenceAndCheckThreshold(key, 5, time.Minute)
			Expect(allowed).To(BeFalse())
			Expect(count).To(Equal(1))
		})

		It("is safe to call when nothing has been tracked", func() {
			Expect(func() { reconciler.resetOccurrenceCount("never-seen") }).NotTo(Panic())
		})
	})

	Describe("cleanupOccurrenceTracking", func() {
		It("removes entries with no remaining timestamps", func() {
			key := "stale-key"
			reconciler.occurrenceMu.Lock()
			reconciler.occurrenceTracking = map[string][]time.Time{
				key: {time.Now().Add(-2 * time.Hour)},
			}
			reconciler.occurrenceMu.Unlock()

			reconciler.cleanupOccurrenceTracking(1 * time.Hour)

			reconciler.occurrenceMu.Lock()
			_, exists := reconciler.occurrenceTracking[key]
			reconciler.occurrenceMu.Unlock()
			Expect(exists).To(BeFalse())
		})

		It("preserves entries inside the window", func() {
			key := "fresh-key"
			reconciler.occurrenceMu.Lock()
			reconciler.occurrenceTracking = map[string][]time.Time{
				key: {time.Now().Add(-30 * time.Second)},
			}
			reconciler.occurrenceMu.Unlock()

			reconciler.cleanupOccurrenceTracking(1 * time.Hour)

			reconciler.occurrenceMu.Lock()
			_, exists := reconciler.occurrenceTracking[key]
			reconciler.occurrenceMu.Unlock()
			Expect(exists).To(BeTrue())
		})
	})

	Describe("maxOccurrenceWindow", func() {
		It("returns default window when no policies are configured", func() {
			result := reconciler.maxOccurrenceWindow(ctx)
			expected := time.Duration(DefaultOccurrenceWindowSeconds) * time.Second * 11 / 10
			Expect(result).To(Equal(expected))
		})

		It("picks the largest configured window across selector and policy levels", func() {
			policy := &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "max-win-policy", Namespace: testNs},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					EventSelectors: []dotaiv1alpha1.EventSelector{
						{Type: "Warning", Reason: "X", OccurrenceWindowSeconds: intPtr(7200)},
						{Type: "Warning", Reason: "Y", OccurrenceWindowSeconds: intPtr(3600)},
					},
					McpEndpoint: "http://test/mcp",
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					OccurrenceWindowSeconds: intPtr(1800),
				},
			}
			Expect(k8sClient.Create(ctx, policy)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, policy) }()

			result := reconciler.maxOccurrenceWindow(ctx)
			expected := time.Duration(7200) * time.Second * 11 / 10
			Expect(result).To(Equal(expected))
		})
	})

	Describe("getOccurrenceKey", func() {
		It("includes reason and message hash so different failures track separately", func() {
			policy := &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: testNs},
			}
			eventBase := corev1.ObjectReference{
				Kind:      "Pod",
				Name:      "my-pod",
				Namespace: testNs,
			}

			eventA := &corev1.Event{
				InvolvedObject: eventBase,
				Reason:         "Unhealthy",
				Message:        "Readiness probe failed: HTTP 503",
			}
			eventB := &corev1.Event{
				InvolvedObject: eventBase,
				Reason:         "Unhealthy",
				Message:        "Readiness probe failed: HTTP 500 (real failure)",
			}
			eventC := &corev1.Event{
				InvolvedObject: eventBase,
				Reason:         "FailedScheduling",
				Message:        "Readiness probe failed: HTTP 503",
			}

			keyA := reconciler.getOccurrenceKey(ctx, policy, eventA)
			keyB := reconciler.getOccurrenceKey(ctx, policy, eventB)
			keyC := reconciler.getOccurrenceKey(ctx, policy, eventC)

			Expect(keyA).NotTo(Equal(keyB), "different messages should produce different keys")
			Expect(keyA).NotTo(Equal(keyC), "different reasons should produce different keys")

			keyA2 := reconciler.getOccurrenceKey(ctx, policy, eventA)
			Expect(keyA).To(Equal(keyA2))
		})
	})

	Describe("shouldFilterByOccurrence", func() {
		var policy *dotaiv1alpha1.RemediationPolicy
		var event *corev1.Event

		BeforeEach(func() {
			policy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: testNs},
			}
			event = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{Name: "evt", Namespace: testNs},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Name:      "my-pod",
					Namespace: testNs,
				},
				Reason:  "Unhealthy",
				Message: "Readiness probe failed: HTTP 503",
			}
		})

		It("does not filter when threshold is unset (<=1)", func() {
			selector := dotaiv1alpha1.EventSelector{}
			filter, _, threshold, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter).To(BeFalse())
			Expect(threshold).To(Equal(0))
		})

		It("does not filter when threshold is exactly 1 (no filtering desired)", func() {
			selector := dotaiv1alpha1.EventSelector{MinOccurrences: intPtr(1)}
			filter, _, _, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter).To(BeFalse())
		})

		It("filters first occurrence when threshold is 2", func() {
			selector := dotaiv1alpha1.EventSelector{
				MinOccurrences:          intPtr(2),
				OccurrenceWindowSeconds: intPtr(60),
			}

			filter, count, threshold, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter).To(BeTrue())
			Expect(count).To(Equal(1))
			Expect(threshold).To(Equal(2))
		})

		It("allows second occurrence within window", func() {
			selector := dotaiv1alpha1.EventSelector{
				MinOccurrences:          intPtr(2),
				OccurrenceWindowSeconds: intPtr(60),
			}

			filter1, _, _, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter1).To(BeTrue())

			filter2, count2, _, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter2).To(BeFalse())
			Expect(count2).To(Equal(2))
		})

		It("respects per-selector override over policy default", func() {
			policy.Spec.MinOccurrences = intPtr(5)
			selector := dotaiv1alpha1.EventSelector{
				MinOccurrences:          intPtr(2),
				OccurrenceWindowSeconds: intPtr(60),
			}

			reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			filter, _, threshold, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)

			Expect(threshold).To(Equal(2), "selector value should override policy default")
			Expect(filter).To(BeFalse(), "should allow on second call with selector threshold of 2")
		})

		It("uses policy default when selector value is unset", func() {
			policy.Spec.MinOccurrences = intPtr(3)
			policy.Spec.OccurrenceWindowSeconds = intPtr(60)
			selector := dotaiv1alpha1.EventSelector{}

			f1, _, _, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			f2, _, _, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			f3, c3, threshold, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)

			Expect(f1).To(BeTrue())
			Expect(f2).To(BeTrue())
			Expect(f3).To(BeFalse())
			Expect(c3).To(Equal(3))
			Expect(threshold).To(Equal(3))
		})

		It("allows explicit selector zero to override a non-zero policy default", func() {
			policy.Spec.MinOccurrences = intPtr(5)
			selector := dotaiv1alpha1.EventSelector{MinOccurrences: intPtr(0)}

			filter, _, threshold, _ := reconciler.shouldFilterByOccurrence(ctx, policy, selector, event)
			Expect(filter).To(BeFalse(), "explicit zero should disable filtering even with policy default of 5")
			Expect(threshold).To(Equal(0))
		})
	})
})
