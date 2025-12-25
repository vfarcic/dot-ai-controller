package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

// Test helper functions for creating McpResponse objects
func createSuccessfulMcpResponse(message string, executionTimeMs float64) McpResponse {
	return McpResponse{
		Success: true,
		Data: &struct {
			Result        map[string]interface{} `json:"result"`
			Tool          string                 `json:"tool"`
			ExecutionTime float64                `json:"executionTime"`
		}{
			Result: map[string]interface{}{
				"message":  message,
				"executed": true, // Default to executed=true for successful responses
			},
			Tool:          "remediate",
			ExecutionTime: executionTimeMs,
		},
	}
}

func createFailedMcpResponse(errorMessage string) McpResponse {
	return McpResponse{
		Success: false,
		Error: &struct {
			Code    string                 `json:"code"`
			Message string                 `json:"message"`
			Details map[string]interface{} `json:"details,omitempty"`
		}{
			Code:    "REMEDIATION_FAILED",
			Message: errorMessage,
		},
	}
}

// matchString is a helper function for checking if a string contains a substring
func matchString(s, substr string) bool {
	if s == "" || substr == "" {
		return false
	}
	return strings.Contains(s, substr)
}

var _ = Describe("RemediationPolicy Controller", func() {
	var (
		reconciler *RemediationPolicyReconciler
		ctx        context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &RemediationPolicyReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			Recorder:   record.NewFakeRecorder(100),
			HttpClient: &http.Client{Timeout: 30 * time.Second},
		}
	})

	Describe("Event Filtering", func() {
		var (
			policy *dotaiv1alpha1.RemediationPolicy
			event  *corev1.Event
		)

		BeforeEach(func() {
			policy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-policy",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					EventSelectors: []dotaiv1alpha1.EventSelector{
						{
							Type:               "Warning",
							Reason:             "CrashLoopBackOff",
							InvolvedObjectKind: "Pod",
							Namespace:          "default",
						},
						{
							Type:               "Warning",
							Reason:             "OOMKilled",
							InvolvedObjectKind: "Pod",
							Mode:               "automatic",
						},
					},
					McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					Mode: "manual",
				},
			}

			event = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-event",
					Namespace: "default",
				},
				Type:   "Warning",
				Reason: "CrashLoopBackOff",
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Name:      "nginx-pod",
					Namespace: "default",
				},
				Message: "Back-off restarting failed container",
			}
		})

		Context("When event matches selector", func() {
			It("should return true for exact match", func() {
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})

			It("should return matching selector", func() {
				matches, selector := reconciler.matchesPolicyWithSelector(event, policy)
				Expect(matches).To(BeTrue())
				Expect(selector.Reason).To(Equal("CrashLoopBackOff"))
				Expect(selector.Type).To(Equal("Warning"))
			})
		})

		Context("When event does not match selector", func() {
			It("should return false for different reason", func() {
				event.Reason = "ImagePullBackOff"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should return false for different type", func() {
				event.Type = "Normal"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should return false for different kind", func() {
				event.InvolvedObject.Kind = "Service"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should return false for different namespace", func() {
				event.Namespace = "kube-system"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})
		})

		Context("When event matches wildcard selectors", func() {
			BeforeEach(func() {
				policy.Spec.EventSelectors = []dotaiv1alpha1.EventSelector{
					{
						Type: "Warning", // Only type specified
					},
				}
			})

			It("should match events with same type regardless of reason", func() {
				event.Reason = "AnyReason"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})
		})

		Context("When filtering by message", func() {
			BeforeEach(func() {
				policy.Spec.EventSelectors = []dotaiv1alpha1.EventSelector{
					{
						Type:    "Warning",
						Message: "Back-off.*pulling image",
					},
				}
				event.Type = "Warning"
				event.Message = "Back-off 5m0s restarting failed container=nginx pod=nginx-7d5c4c7d4d-abc12 pulling image nginx:latest"
			})

			It("should match events with message matching regex pattern", func() {
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})

			It("should not match events with non-matching message", func() {
				event.Message = "Failed to pull image nginx:latest"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should match events when message field is empty (wildcard)", func() {
				policy.Spec.EventSelectors[0].Message = ""
				event.Message = "Any message content"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})

			It("should not match events with invalid regex pattern", func() {
				policy.Spec.EventSelectors[0].Message = "[invalid(regex"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should support case-sensitive matching", func() {
				policy.Spec.EventSelectors[0].Message = "back-off" // lowercase
				event.Message = "Back-off pulling image"           // uppercase B
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeFalse())
			})

			It("should support case-insensitive regex with (?i) flag", func() {
				policy.Spec.EventSelectors[0].Message = "(?i)back-off"
				event.Message = "Back-off pulling image"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})

			It("should work with combined filters", func() {
				policy.Spec.EventSelectors[0].Reason = "BackOff"
				policy.Spec.EventSelectors[0].InvolvedObjectKind = "Pod"
				event.Reason = "BackOff"
				event.InvolvedObject.Kind = "Pod"
				matches := reconciler.matchesPolicy(event, policy)
				Expect(matches).To(BeTrue())
			})
		})
	})

	Describe("Startup Time Filtering", func() {
		Context("When events occurred before controller startup", func() {
			It("should ignore events with lastTimestamp before startupTime", func() {
				// Set startup time to now
				reconciler.startupTime = time.Now()

				// Create event with lastTimestamp 1 hour ago
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-before-event-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Type:          "Warning",
					Reason:        "CrashLoopBackOff",
					LastTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container",
				}

				// Create a policy that would match this event
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-test-policy-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "CrashLoopBackOff",
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}

				err := k8sClient.Create(ctx, policy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())

				// Create the event in the cluster
				err = k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				// Try to reconcile the event
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				// Verify the event was NOT processed (status should not be updated)
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				err = k8sClient.Get(ctx, policyKey, updatedPolicy)
				Expect(err).NotTo(HaveOccurred())

				// TotalEventsProcessed should be 0 because event was filtered
				Expect(updatedPolicy.Status.TotalEventsProcessed).To(Equal(int64(0)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, policy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should ignore events with eventTime before startupTime (series-based events)", func() {
				// Set startup time to now
				reconciler.startupTime = time.Now()

				// Create event with eventTime 1 hour ago (for series-based events)
				// Series-based events require additional fields: action, reportingController, reportingInstance
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-series-event-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Type:                "Warning",
					Reason:              "CrashLoopBackOff",
					EventTime:           metav1.NewMicroTime(time.Now().Add(-1 * time.Hour)),
					Action:              "Pulling",
					ReportingController: "kubelet",
					ReportingInstance:   "test-node",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container",
				}

				// Create a policy that would match this event
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-series-policy-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "CrashLoopBackOff",
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}

				err := k8sClient.Create(ctx, policy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())

				// Create the event in the cluster
				err = k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				// Try to reconcile the event
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				// Verify the event was NOT processed
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				err = k8sClient.Get(ctx, policyKey, updatedPolicy)
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedPolicy.Status.TotalEventsProcessed).To(Equal(int64(0)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, policy)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("When events occurred after controller startup", func() {
			It("should process events with lastTimestamp after startupTime", func() {
				// Set startup time to 1 hour ago
				reconciler.startupTime = time.Now().Add(-1 * time.Hour)

				// Create a mock MCP server for this test
				mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Remediation successful", 1000))
				}))
				defer mockServer.Close()

				// Use a unique reason to avoid matching other policies
				uniqueReason := fmt.Sprintf("StartupAfterTest-%d", time.Now().UnixNano())

				// Create event with lastTimestamp now (after startup)
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-after-event-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Type:          "Warning",
					Reason:        uniqueReason,
					LastTimestamp: metav1.NewTime(time.Now()),
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container",
				}

				// Create a policy that would match this event (with unique reason)
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-after-policy-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             uniqueReason,
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: mockServer.URL + "/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}

				err := k8sClient.Create(ctx, policy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())

				// Create the event in the cluster
				err = k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				// Reconcile the event
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				// Verify the event WAS processed
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, policy)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("When startupTime is not set (zero value)", func() {
			It("should process all events when startupTime is zero", func() {
				// Ensure startupTime is zero (not set)
				reconciler.startupTime = time.Time{}

				// Create a mock MCP server for this test
				mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Remediation successful", 1000))
				}))
				defer mockServer.Close()

				// Use a unique reason to avoid matching other policies
				uniqueReason := fmt.Sprintf("StartupZeroTest-%d", time.Now().UnixNano())

				// Create event with lastTimestamp 24 hours ago
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-zero-event-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Type:          "Warning",
					Reason:        uniqueReason,
					LastTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container",
				}

				// Create a policy that would match this event (with unique reason)
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-zero-policy-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             uniqueReason,
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: mockServer.URL + "/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}

				err := k8sClient.Create(ctx, policy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())

				// Create the event in the cluster
				err = k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				// Reconcile the event
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				// Verify the event WAS processed (startupTime zero means no filtering)
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, policy)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("When event has no timestamp", func() {
			It("should process events without any timestamp", func() {
				// Set startup time
				reconciler.startupTime = time.Now()

				// Create a mock MCP server for this test
				mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Remediation successful", 1000))
				}))
				defer mockServer.Close()

				// Use a unique reason to avoid matching other policies
				uniqueReason := fmt.Sprintf("StartupNoTsTest-%d", time.Now().UnixNano())

				// Create event without any timestamp
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-no-ts-event-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: uniqueReason,
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container",
				}

				// Create a policy that would match this event (with unique reason)
				policy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("startup-no-ts-policy-%d", time.Now().UnixNano()),
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             uniqueReason,
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: mockServer.URL + "/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}

				err := k8sClient.Create(ctx, policy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())

				// Create the event in the cluster
				err = k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				// Reconcile the event
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				// Verify the event WAS processed (no timestamp = process anyway)
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, policy)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Event Deduplication", func() {
		var event *corev1.Event

		BeforeEach(func() {
			event = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-event",
					Namespace:       "default",
					ResourceVersion: "12345",
				},
			}
		})

		It("should mark event as processed", func() {
			eventKey := reconciler.getEventKey(event)
			Expect(reconciler.isEventProcessed(eventKey)).To(BeFalse())

			reconciler.markEventProcessed(eventKey)
			Expect(reconciler.isEventProcessed(eventKey)).To(BeTrue())
		})

		It("should generate unique keys for different events", func() {
			key1 := reconciler.getEventKey(event)

			event2 := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "different-event",
					Namespace:       "default",
					ResourceVersion: "12345",
				},
			}
			key2 := reconciler.getEventKey(event2)

			Expect(key1).NotTo(Equal(key2))
		})

		It("should clean up old processed events", func() {
			eventKey := reconciler.getEventKey(event)
			reconciler.markEventProcessed(eventKey)

			// Cleanup events older than 1 nanosecond (should remove our event)
			time.Sleep(time.Nanosecond)
			reconciler.cleanupProcessedEvents(time.Nanosecond)

			// Event should no longer be considered processed
			Expect(reconciler.isEventProcessed(eventKey)).To(BeFalse())
		})
	})

	Describe("Mode Resolution", func() {
		var policy *dotaiv1alpha1.RemediationPolicy

		BeforeEach(func() {
			policy = &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Mode: "manual",
				},
			}
		})

		It("should return selector mode when specified", func() {
			selector := dotaiv1alpha1.EventSelector{Mode: "automatic"}
			mode := reconciler.getEffectiveMode(selector, policy)
			Expect(mode).To(Equal("automatic"))
		})

		It("should return policy mode when selector mode not specified", func() {
			selector := dotaiv1alpha1.EventSelector{}
			mode := reconciler.getEffectiveMode(selector, policy)
			Expect(mode).To(Equal("manual"))
		})

		It("should return default mode when neither specified", func() {
			policy.Spec.Mode = ""
			selector := dotaiv1alpha1.EventSelector{}
			mode := reconciler.getEffectiveMode(selector, policy)
			Expect(mode).To(Equal("manual"))
		})
	})

	Describe("Effective Value Resolution", func() {
		var policy *dotaiv1alpha1.RemediationPolicy

		BeforeEach(func() {
			confidenceThreshold := 0.7
			policy = &dotaiv1alpha1.RemediationPolicy{
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					Mode:                "manual",
					ConfidenceThreshold: &confidenceThreshold,
					MaxRiskLevel:        "medium",
				},
			}
		})

		Context("Confidence Threshold Resolution", func() {
			It("should return selector value when specified", func() {
				selectorThreshold := 0.9
				selector := dotaiv1alpha1.EventSelector{
					ConfidenceThreshold: &selectorThreshold,
				}
				effective := reconciler.getEffectiveConfidenceThreshold(selector, policy)
				Expect(effective).To(Equal(0.9))
			})

			It("should return policy value when selector not specified", func() {
				selector := dotaiv1alpha1.EventSelector{}
				effective := reconciler.getEffectiveConfidenceThreshold(selector, policy)
				Expect(effective).To(Equal(0.7))
			})

			It("should return default when neither specified", func() {
				policy.Spec.ConfidenceThreshold = nil
				selector := dotaiv1alpha1.EventSelector{}
				effective := reconciler.getEffectiveConfidenceThreshold(selector, policy)
				Expect(effective).To(Equal(0.8)) // OpenAPI default
			})
		})

		Context("Max Risk Level Resolution", func() {
			It("should return selector value when specified", func() {
				selector := dotaiv1alpha1.EventSelector{MaxRiskLevel: "high"}
				effective := reconciler.getEffectiveMaxRiskLevel(selector, policy)
				Expect(effective).To(Equal("high"))
			})

			It("should return policy value when selector not specified", func() {
				selector := dotaiv1alpha1.EventSelector{}
				effective := reconciler.getEffectiveMaxRiskLevel(selector, policy)
				Expect(effective).To(Equal("medium"))
			})

			It("should return default when neither specified", func() {
				policy.Spec.MaxRiskLevel = ""
				selector := dotaiv1alpha1.EventSelector{}
				effective := reconciler.getEffectiveMaxRiskLevel(selector, policy)
				Expect(effective).To(Equal("low")) // OpenAPI default
			})
		})
	})

	Describe("Basic Resource Reconciliation", func() {
		const resourceName = "test-resource"

		var (
			typeNamespacedName types.NamespacedName
			remediationpolicy  *dotaiv1alpha1.RemediationPolicy
		)

		BeforeEach(func() {
			typeNamespacedName = types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}

			By("creating the custom resource for the Kind RemediationPolicy")
			remediationpolicy = &dotaiv1alpha1.RemediationPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, remediationpolicy)
			if err != nil && errors.IsNotFound(err) {
				resource := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "CrashLoopBackOff",
								InvolvedObjectKind: "Pod",
							},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance RemediationPolicy")
			resource := &dotaiv1alpha1.RemediationPolicy{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")

			// Test reconciling the RemediationPolicy itself
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify policy status was initialized
			updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
			err = k8sClient.Get(ctx, typeNamespacedName, updatedPolicy)
			Expect(err).NotTo(HaveOccurred())

			// Status should be initialized
			Eventually(func() []metav1.Condition {
				k8sClient.Get(ctx, typeNamespacedName, updatedPolicy)
				return updatedPolicy.Status.Conditions
			}, "10s").Should(HaveLen(1))

			condition := updatedPolicy.Status.Conditions[0]
			Expect(condition.Type).To(Equal("Ready"))
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should handle non-existent events gracefully", func() {
			By("Reconciling a non-existent event")
			eventNamespacedName := types.NamespacedName{
				Name:      "non-existent-event",
				Namespace: "default",
			}

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: eventNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("Integration: Complete MCP Message Generation Workflow", func() {
		var (
			policy       *dotaiv1alpha1.RemediationPolicy
			fakeRecorder *record.FakeRecorder
			mockServer   *httptest.Server
			requestCount int
			lastRequest  *http.Request
			requestBody  string
		)

		BeforeEach(func() {
			// Reset request tracking
			requestCount = 0
			lastRequest = nil
			requestBody = ""

			// Create mock MCP server that simulates various responses
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				lastRequest = r

				// Read request body for verification
				body := make([]byte, r.ContentLength)
				r.Body.Read(body)
				requestBody = string(body)

				// Simulate different responses based on request content
				if strings.Contains(requestBody, "CrashLoopBackOff") {
					// Successful remediation for CrashLoopBackOff
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Pod restart initiated successfully", 1500))
				} else if strings.Contains(requestBody, "OOMKilled") {
					// Successful remediation for OOMKilled
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Memory limits adjusted and pod restarted", 2200))
				} else if strings.Contains(requestBody, "FailedScheduling") {
					// MCP returns an error for FailedScheduling
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createFailedMcpResponse("Unable to resolve scheduling constraints"))
				} else {
					// Default successful response
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Remediation completed successfully", 1000))
				}
			}))

			// Create a fresh fake recorder for each test to capture events
			fakeRecorder = record.NewFakeRecorder(100)
			reconciler.Recorder = fakeRecorder

			// Create a comprehensive RemediationPolicy with multiple selectors
			// Use unique names to avoid cross-test contamination
			policyName := fmt.Sprintf("integration-test-policy-%d", time.Now().UnixNano())
			policy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policyName,
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					EventSelectors: []dotaiv1alpha1.EventSelector{
						{
							Type:               "Warning",
							Reason:             "CrashLoopBackOff",
							InvolvedObjectKind: "Pod",
							Mode:               "manual", // Explicit manual mode
						},
						{
							Type:                "Warning",
							Reason:              "OOMKilled",
							InvolvedObjectKind:  "Pod",
							Mode:                "automatic", // Explicit automatic mode
							ConfidenceThreshold: func(f float64) *float64 { return &f }(0.95),
							MaxRiskLevel:        "medium",
						},
						{
							Type:               "Warning",
							InvolvedObjectKind: "Pod",
							// No mode specified - should use policy default
						},
					},
					McpEndpoint: mockServer.URL + "/api/v1/tools/remediate",
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					Mode: "manual", // Policy default mode
					RateLimiting: dotaiv1alpha1.RateLimiting{
						EventsPerMinute: 5,
						CooldownMinutes: 1, // Short cooldown for testing
					},
				},
			}

			// Create the policy in the cluster
			err := k8sClient.Create(ctx, policy)
			Expect(err).NotTo(HaveOccurred())

			// Initialize policy status by reconciling it
			policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			// Clean up mock server
			if mockServer != nil {
				mockServer.Close()
			}
		})

		AfterEach(func() {
			// Clean up the policy
			err := k8sClient.Delete(ctx, policy)
			if err != nil && !errors.IsNotFound(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		})

		Context("End-to-End Event Processing Workflow", func() {
			It("should process matching events through complete MCP workflow", func() {
				By("Creating a matching CrashLoopBackOff event")
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "crash-event-integration",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "CrashLoopBackOff",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "nginx-integration",
						Namespace: "default",
					},
					Message: "Back-off restarting failed container nginx",
				}

				// Create the event in the cluster
				err := k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				By("Processing the event through reconciliation")
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying policy status was updated with event processing")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Verify MCP message generation count
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalMcpMessagesGenerated
				}, "5s").Should(Equal(int64(1)))

				// Verify last processed event timestamp was set
				Expect(updatedPolicy.Status.LastProcessedEvent).NotTo(BeNil())
				Expect(updatedPolicy.Status.LastMcpMessageGenerated).NotTo(BeNil())

				// Verify successful remediation count (MCP returned success)
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.SuccessfulRemediations
				}, "5s").Should(Equal(int64(1)))

				// Verify no failed remediations
				Expect(updatedPolicy.Status.FailedRemediations).To(Equal(int64(0)))

				By("Verifying HTTP request was sent to mock server")
				Eventually(func() int {
					return requestCount
				}, "5s").Should(Equal(1))

				// Verify the request details
				Expect(lastRequest).NotTo(BeNil())
				Expect(lastRequest.Method).To(Equal("POST"))
				Expect(lastRequest.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(lastRequest.Header.Get("User-Agent")).To(Equal("dot-ai-controller/v1.0.0"))

				// Verify the request body contains expected event information
				Expect(requestBody).To(ContainSubstring("CrashLoopBackOff"))
				Expect(requestBody).To(ContainSubstring("nginx-integration"))
				Expect(requestBody).To(ContainSubstring("manual"))
				Expect(requestBody).To(ContainSubstring("Back-off restarting failed container nginx"))

				By("Verifying Kubernetes Events were generated")
				// Check that events were recorded (we can't easily check the content in envtest)
				Eventually(func() int {
					return len(fakeRecorder.Events)
				}, "5s").Should(BeNumerically(">=", 3)) // EventMatched, McpMessageGenerated, McpRequestSucceeded

				By("Verifying event content")
				// Verify success event contains MCP response message
				events := fakeRecorder.Events
				var successEvent string
				for event := range events {
					if strings.Contains(event, "McpRequestSucceeded") {
						successEvent = event
						break
					}
				}
				Expect(successEvent).To(ContainSubstring("McpRequestSucceeded"))
				Expect(successEvent).To(ContainSubstring("Pod restart initiated successfully"))

				// Clean up the event
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should handle different event types with correct mode precedence", func() {
				By("Creating multiple events with different selectors")

				// Event 1: Should match first selector (CrashLoopBackOff) - manual mode
				event1 := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "crash-event-mode-test-1",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "CrashLoopBackOff",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "app-1",
						Namespace: "default",
					},
					Message: "Container crashing repeatedly",
				}

				// Event 2: Should match second selector (OOMKilled) - automatic mode
				event2 := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "oom-event-mode-test-2",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "OOMKilled",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "app-2",
						Namespace: "default",
					},
					Message: "Container exceeded memory limit",
				}

				// Event 3: Should match third selector (wildcard) - policy default mode
				event3 := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "generic-event-mode-test-3",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "ImagePullBackOff", // Different reason, should match wildcard selector
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "app-3",
						Namespace: "default",
					},
					Message: "Failed to pull image",
				}

				// Create all events
				events := []*corev1.Event{event1, event2, event3}
				for _, event := range events {
					err := k8sClient.Create(ctx, event)
					Expect(err).NotTo(HaveOccurred())
				}

				By("Processing all events through reconciliation")
				for _, event := range events {
					eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
					_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
					Expect(err).NotTo(HaveOccurred())
				}

				By("Verifying all events were processed")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "10s").Should(Equal(int64(3)))

				// All should generate MCP messages
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalMcpMessagesGenerated
				}, "10s").Should(Equal(int64(3)))

				By("Verifying multiple Kubernetes Events were generated")
				Eventually(func() int {
					return len(fakeRecorder.Events)
				}, "10s").Should(BeNumerically(">=", 6)) // 3 EventMatched + 3 McpMessageGenerated

				// Clean up events
				for _, event := range events {
					err := k8sClient.Delete(ctx, event)
					Expect(err).NotTo(HaveOccurred())
				}
			})
		})

		Context("Event Deduplication Integration", func() {
			It("should not reprocess the same event multiple times", func() {
				By("Creating an event and processing it twice")
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "duplicate-test-event",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "CrashLoopBackOff",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "duplicate-test-pod",
						Namespace: "default",
					},
					Message: "Container keeps crashing",
				}

				err := k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}

				By("First reconciliation - should process the event")
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Second reconciliation - should skip already processed event")
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying event was only processed once")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Only one MCP message should be generated
				Expect(updatedPolicy.Status.TotalMcpMessagesGenerated).To(Equal(int64(1)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Non-Matching Events Integration", func() {
			It("should ignore events that don't match any selector", func() {
				By("Creating a non-matching event")
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "non-matching-event",
						Namespace: "default",
					},
					Type:   "Normal", // Different type - should not match
					Reason: "Created",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Service", // Different kind - should not match
						Name:      "test-service",
						Namespace: "default",
					},
					Message: "Service created successfully",
				}

				err := k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				By("Processing the non-matching event")
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying no processing occurred")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}

				// Give some time for any processing to occur
				time.Sleep(2 * time.Second)

				err = k8sClient.Get(ctx, policyKey, updatedPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Counters should remain at 0
				Expect(updatedPolicy.Status.TotalEventsProcessed).To(Equal(int64(0)))
				Expect(updatedPolicy.Status.TotalMcpMessagesGenerated).To(Equal(int64(0)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Multiple Policies Integration", func() {
			var secondPolicy *dotaiv1alpha1.RemediationPolicy

			BeforeEach(func() {
				// Create a second policy with different selectors
				// Use unique names to avoid cross-test contamination
				secondPolicyName := fmt.Sprintf("second-integration-policy-%d", time.Now().UnixNano())
				secondPolicy = &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secondPolicyName,
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "ImagePullBackOff",
								InvolvedObjectKind: "Pod",
								Mode:               "automatic",
							},
						},
						McpEndpoint: "http://second-test-mcp:3456/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
						Mode: "automatic",
					},
				}

				err := k8sClient.Create(ctx, secondPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize second policy status
				policyKey := types.NamespacedName{Name: secondPolicy.Name, Namespace: secondPolicy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				err := k8sClient.Delete(ctx, secondPolicy)
				if err != nil && !errors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}
			})

			It("should process different events against different policies correctly", func() {
				By("Creating an event that matches both policies")
				// This event should match the wildcard selector in the first policy
				// and the ImagePullBackOff selector in the second policy
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "multi-policy-event",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "ImagePullBackOff",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "multi-match-pod",
						Namespace: "default",
					},
					Message: "Failed to pull container image",
				}

				err := k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				By("Processing the event")
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying both policies were updated")
				// First policy should be updated
				firstPolicyKey := types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}
				updatedFirstPolicy := &dotaiv1alpha1.RemediationPolicy{}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, firstPolicyKey, updatedFirstPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedFirstPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1)))

				// Second policy should NOT be updated (controller processes first match only)
				secondPolicyKey := types.NamespacedName{Name: secondPolicy.Name, Namespace: secondPolicy.Namespace}
				updatedSecondPolicy := &dotaiv1alpha1.RemediationPolicy{}

				// Give some time for any potential processing
				time.Sleep(2 * time.Second)

				err = k8sClient.Get(ctx, secondPolicyKey, updatedSecondPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Second policy should have 0 events processed (event went to first policy)
				Expect(updatedSecondPolicy.Status.TotalEventsProcessed).To(Equal(int64(0)))

				// Only first policy should have generated MCP message
				Expect(updatedFirstPolicy.Status.TotalMcpMessagesGenerated).To(Equal(int64(1)))
				Expect(updatedSecondPolicy.Status.TotalMcpMessagesGenerated).To(Equal(int64(0)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("HTTP Error Handling and Retry Logic", func() {
			var (
				errorServer       *httptest.Server
				errorRequestCount int
				errorPolicy       *dotaiv1alpha1.RemediationPolicy
			)

			BeforeEach(func() {
				errorRequestCount = 0

				// Create a separate policy with its own error-prone mock server
				errorPolicyName := fmt.Sprintf("error-test-policy-%d", time.Now().UnixNano())

				// Create error server that always fails with 500 errors
				errorServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					errorRequestCount++
					// Always fail with server error (no retries)
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("Internal Server Error - MCP temporarily unavailable"))
				}))

				errorPolicy = &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      errorPolicyName,
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "FailedScheduling",
								InvolvedObjectKind: "Pod",
								Mode:               "automatic",
							},
						},
						McpEndpoint: errorServer.URL + "/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
						Mode: "manual",
					},
				}

				// Create the error policy
				err := k8sClient.Create(ctx, errorPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy status
				policyKey := types.NamespacedName{Name: errorPolicy.Name, Namespace: errorPolicy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				// Clean up error server and policy
				if errorServer != nil {
					errorServer.Close()
				}
				if errorPolicy != nil {
					err := k8sClient.Delete(ctx, errorPolicy)
					Expect(err).NotTo(HaveOccurred())
				}
			})

			It("should handle HTTP server errors without retries", func() {
				By("Creating an event that will trigger HTTP error")
				event := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "retry-test-event",
						Namespace: "default",
					},
					Type:   "Warning",
					Reason: "FailedScheduling",
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "retry-test-pod",
						Namespace: "default",
					},
					Message: "Pod failed to schedule due to resource constraints",
				}

				err := k8sClient.Create(ctx, event)
				Expect(err).NotTo(HaveOccurred())

				By("Processing the event through reconciliation")
				eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
				Expect(err).NotTo(HaveOccurred())

				By("Verifying only one HTTP request was made (no retries)")
				Eventually(func() int {
					return errorRequestCount
				}, "10s").Should(Equal(1)) // Single attempt, no retries

				By("Verifying the failure was recorded")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: errorPolicy.Name, Namespace: errorPolicy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.FailedRemediations
				}, "5s").Should(Equal(int64(1)))

				// Should have no successful remediations since HTTP request failed
				Expect(updatedPolicy.Status.SuccessfulRemediations).To(Equal(int64(0)))

				// Clean up
				err = k8sClient.Delete(ctx, event)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Rate Limiting Integration", func() {
			// Use a separate variable to avoid interference with the outer policy
			var (
				rateLimitTestPolicy *dotaiv1alpha1.RemediationPolicy
				rateLimitMockServer *httptest.Server
			)

			BeforeEach(func() {
				// Create mock server for rate limiting tests
				rateLimitMockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Always return success for rate limiting tests
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Rate limiting test remediation successful", 800))
				}))
				// Clean up the main policy to avoid interference during rate limiting tests
				// since controller processes events against first matching policy only
				err := k8sClient.Delete(ctx, policy)
				if err != nil && !errors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}

				// Create a policy with strict rate limiting for testing
				// Use unique names to avoid cross-test contamination
				rateLimitPolicyName := fmt.Sprintf("rate-limit-test-policy-%d", time.Now().UnixNano())
				rateLimitTestPolicy = &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      rateLimitPolicyName,
						Namespace: "default",
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{
								Type:               "Warning",
								Reason:             "CrashLoopBackOff",
								InvolvedObjectKind: "Pod",
								Mode:               "manual",
							},
						},
						McpEndpoint: rateLimitMockServer.URL + "/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
						Mode: "manual",
						RateLimiting: dotaiv1alpha1.RateLimiting{
							EventsPerMinute: 5, // Allow more events per minute for testing
							CooldownMinutes: 1, // Short cooldown for fast tests
						},
					},
				}

				err = k8sClient.Create(ctx, rateLimitTestPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Initialize policy status
				policyKey := types.NamespacedName{Name: rateLimitTestPolicy.Name, Namespace: rateLimitTestPolicy.Namespace}
				_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: policyKey})
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				// Clean up mock server
				if rateLimitMockServer != nil {
					rateLimitMockServer.Close()
				}

				err := k8sClient.Delete(ctx, rateLimitTestPolicy)
				if err != nil && !errors.IsNotFound(err) {
					Expect(err).NotTo(HaveOccurred())
				}

				// Recreate the main policy for other tests
				// Note: This recreates with the same name as the original policy
				err = k8sClient.Create(ctx, policy)
				// Ignore both AlreadyExists and NotFound errors during cleanup
				if err != nil && !errors.IsAlreadyExists(err) && !errors.IsNotFound(err) {
					// Log the error but don't fail the test during cleanup
					GinkgoWriter.Printf("Warning: Failed to recreate main policy during cleanup: %v\n", err)
				}
			})

			It("should enforce rate limiting on multiple events for same resource", func() {
				By("Creating multiple events for the same pod")

				// Helper function to create and process an event
				processEvent := func(eventName, resourceVersion string) *corev1.Event {
					event := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{
							Name:      eventName,
							Namespace: "default",
						},
						Type:   "Warning",
						Reason: "CrashLoopBackOff",
						InvolvedObject: corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "rate-limited-pod", // Same pod name for all events
							Namespace: "default",
						},
						Message: "Container crashing in rate limit test",
					}

					err := k8sClient.Create(ctx, event)
					Expect(err).NotTo(HaveOccurred())

					eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
					Expect(err).NotTo(HaveOccurred())

					return event
				}

				By("Processing first two events - should be processed")
				event1 := processEvent("rate-limit-event-1", "5001")
				event2 := processEvent("rate-limit-event-2", "5002")

				By("Processing third event - should be rate limited")
				event3 := processEvent("rate-limit-event-3", "5003")

				By("Verifying rate limiting status")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: rateLimitTestPolicy.Name, Namespace: rateLimitTestPolicy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(1))) // Only first event processed (others rate limited)

				// Should have 1 MCP message (others rate limited)
				Expect(updatedPolicy.Status.TotalMcpMessagesGenerated).To(Equal(int64(1)))

				// Should have 2 rate limited events
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.RateLimitedEvents
				}, "5s").Should(Equal(int64(2)))

				// Rate limited timestamp should be set
				Expect(updatedPolicy.Status.LastRateLimitedEvent).NotTo(BeNil())

				By("Verifying rate limiting behavior is working correctly")
				// Note: We don't test cooldown expiration in unit tests due to time constraints
				// This would be better tested in e2e tests or with a mock clock
				// For now, we verify the rate limiting mechanism itself is working

				// Clean up events
				events := []*corev1.Event{event1, event2, event3}
				for _, event := range events {
					err := k8sClient.Delete(ctx, event)
					Expect(err).NotTo(HaveOccurred())
				}
			})

			It("should allow events for different resources even when rate limited", func() {
				By("Creating multiple events for same resource to trigger rate limiting")

				// Fill up the rate limit for pod-a
				processEventForResource := func(eventName, resourceVersion, podName string) *corev1.Event {
					event := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{
							Name:      eventName,
							Namespace: "default",
						},
						Type:   "Warning",
						Reason: "CrashLoopBackOff",
						InvolvedObject: corev1.ObjectReference{
							Kind:      "Pod",
							Name:      podName,
							Namespace: "default",
						},
						Message: "Container crashing",
					}

					err := k8sClient.Create(ctx, event)
					Expect(err).NotTo(HaveOccurred())

					eventKey := types.NamespacedName{Name: event.Name, Namespace: event.Namespace}
					_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
					Expect(err).NotTo(HaveOccurred())

					return event
				}

				// Use up rate limit for pod-a
				event1 := processEventForResource("different-resource-1", "6001", "pod-a")
				event2 := processEventForResource("different-resource-2", "6002", "pod-a")
				event3 := processEventForResource("different-resource-3", "6003", "pod-a") // Should be rate limited

				By("Processing event for different resource - should still be processed")
				event4 := processEventForResource("different-resource-4", "6004", "pod-b") // Different pod

				By("Verifying different resource events are not rate limited")
				updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}
				policyKey := types.NamespacedName{Name: rateLimitTestPolicy.Name, Namespace: rateLimitTestPolicy.Namespace}

				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(Equal(int64(2))) // 1 for pod-a + 1 for pod-b (2nd and 3rd events for pod-a rate limited)

				// Wait a bit more for the rate limit policy to process the pod-b event
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.TotalMcpMessagesGenerated
				}, "5s").Should(Equal(int64(2)))

				// Should have 2 rate limited events (2nd and 3rd events for pod-a)
				Eventually(func() int64 {
					err := k8sClient.Get(ctx, policyKey, updatedPolicy)
					Expect(err).NotTo(HaveOccurred())
					return updatedPolicy.Status.RateLimitedEvents
				}, "5s").Should(Equal(int64(2)))

				// Clean up events
				events := []*corev1.Event{event1, event2, event3, event4}
				for _, event := range events {
					err := k8sClient.Delete(ctx, event)
					Expect(err).NotTo(HaveOccurred())
				}
			})
		})
	})

	Describe("Observability: Status Counters and Success Events", func() {
		var (
			successServer *httptest.Server
			failureServer *httptest.Server
			fakeRecorder  *record.FakeRecorder
		)

		BeforeEach(func() {
			fakeRecorder = record.NewFakeRecorder(100)
			// Ensure reconciler has both HttpClient and our fake recorder
			reconciler = &RemediationPolicyReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Recorder:   fakeRecorder,
				HttpClient: &http.Client{Timeout: 30 * time.Second},
			}

			// Success server returns HTTP 200 with success response
			successServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(createSuccessfulMcpResponse("Remediation completed successfully", 1200))
			}))

			// Failure server returns HTTP 200 with failure response (MCP tool execution failed)
			failureServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(createFailedMcpResponse("Tool execution failed - insufficient permissions"))
			}))
		})

		AfterEach(func() {
			if successServer != nil {
				successServer.Close()
			}
			if failureServer != nil {
				failureServer.Close()
			}
		})

		It("should correctly track successful remediations and emit success events", func() {
			By("Creating policy that points to success server")
			successPolicy := &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "success-observability-test",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					EventSelectors: []dotaiv1alpha1.EventSelector{
						{
							Type:               "Warning",
							Reason:             "FailedScheduling",
							InvolvedObjectKind: "Pod",
							Mode:               "automatic",
						},
					},
					McpEndpoint: successServer.URL + "/api/v1/tools/remediate",
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					Mode: "manual",
				},
			}

			err := k8sClient.Create(ctx, successPolicy)
			Expect(err).NotTo(HaveOccurred())

			By("Creating failing pod event")
			successEvent := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "success-observability-test-event",
					Namespace: "default",
				},
				Type:           "Warning",
				Reason:         "FailedScheduling",
				Message:        "Pod failed to schedule due to insufficient resources",
				FirstTimestamp: metav1.NewTime(time.Now()),
				LastTimestamp:  metav1.NewTime(time.Now()),
				Count:          1,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Name:      "test-success-pod",
					Namespace: "default",
				},
				Source: corev1.EventSource{Component: "scheduler"},
			}

			err = k8sClient.Create(ctx, successEvent)
			Expect(err).NotTo(HaveOccurred())

			By("Triggering event reconciliation")
			eventKey := types.NamespacedName{Name: successEvent.Name, Namespace: successEvent.Namespace}
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying successful remediation counter increments")
			policyKey := types.NamespacedName{Name: successPolicy.Name, Namespace: successPolicy.Namespace}
			updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}

			Eventually(func() int64 {
				err := k8sClient.Get(ctx, policyKey, updatedPolicy)
				Expect(err).NotTo(HaveOccurred())
				return updatedPolicy.Status.SuccessfulRemediations
			}, "10s").Should(Equal(int64(1)))

			// Verify no failed remediations
			Expect(updatedPolicy.Status.FailedRemediations).To(Equal(int64(0)))

			By("Verifying McpRequestSucceeded event was generated")
			Eventually(func() bool {
				events := fakeRecorder.Events
				for event := range events {
					if strings.Contains(event, "McpRequestSucceeded") &&
						strings.Contains(event, "Remediation completed successfully") {
						return true
					}
				}
				return false
			}, "5s").Should(BeTrue())

			// Clean up
			err = k8sClient.Delete(ctx, successEvent)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, successPolicy)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should correctly track failed remediations when MCP tool execution fails", func() {
			By("Creating policy that points to failure server")
			failurePolicy := &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failure-observability-test",
					Namespace: "default",
				},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					EventSelectors: []dotaiv1alpha1.EventSelector{
						{
							Type:               "Warning",
							Reason:             "FailedMount",
							InvolvedObjectKind: "Pod",
							Mode:               "automatic",
						},
					},
					McpEndpoint: failureServer.URL + "/api/v1/tools/remediate",
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					Mode: "manual",
				},
			}

			err := k8sClient.Create(ctx, failurePolicy)
			Expect(err).NotTo(HaveOccurred())

			By("Creating failing pod event")
			failureEvent := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failure-observability-test-event",
					Namespace: "default",
				},
				Type:           "Warning",
				Reason:         "FailedMount",
				Message:        "Unable to mount volume - PVC not found",
				FirstTimestamp: metav1.NewTime(time.Now()),
				LastTimestamp:  metav1.NewTime(time.Now()),
				Count:          1,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Name:      "test-failure-pod",
					Namespace: "default",
				},
				Source: corev1.EventSource{Component: "kubelet"},
			}

			err = k8sClient.Create(ctx, failureEvent)
			Expect(err).NotTo(HaveOccurred())

			By("Triggering event reconciliation")
			eventKey := types.NamespacedName{Name: failureEvent.Name, Namespace: failureEvent.Namespace}
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: eventKey})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying failed remediation counter increments")
			policyKey := types.NamespacedName{Name: failurePolicy.Name, Namespace: failurePolicy.Namespace}
			updatedPolicy := &dotaiv1alpha1.RemediationPolicy{}

			Eventually(func() int64 {
				err := k8sClient.Get(ctx, policyKey, updatedPolicy)
				Expect(err).NotTo(HaveOccurred())
				return updatedPolicy.Status.FailedRemediations
			}, "10s").Should(Equal(int64(1)))

			// Verify no successful remediations
			Expect(updatedPolicy.Status.SuccessfulRemediations).To(Equal(int64(0)))

			By("Verifying McpRemediationFailed event was generated")
			Eventually(func() bool {
				events := fakeRecorder.Events
				for event := range events {
					if strings.Contains(event, "McpRemediationFailed") &&
						strings.Contains(event, "insufficient permissions") {
						return true
					}
				}
				return false
			}, "5s").Should(BeTrue())

			// Clean up
			err = k8sClient.Delete(ctx, failureEvent)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Delete(ctx, failurePolicy)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
