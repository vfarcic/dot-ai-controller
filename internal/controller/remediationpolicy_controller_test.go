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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
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
					Mode:        "manual",
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

	Describe("MCP Message Generation", func() {
		var (
			policy *dotaiv1alpha1.RemediationPolicy
			event  *corev1.Event
		)

		BeforeEach(func() {
			policy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-policy",
				},
				Spec: dotaiv1alpha1.RemediationPolicySpec{
					McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
				},
			}

			event = &corev1.Event{
				Type:   "Warning",
				Reason: "CrashLoopBackOff",
				InvolvedObject: corev1.ObjectReference{
					APIVersion: "v1", // Core Kubernetes resource
					Kind:       "Pod",
					Name:       "nginx-pod",
					Namespace:  "default",
				},
				Message: "Back-off restarting failed container",
			}
		})

		Context("Issue Description Generation", func() {
			It("should generate simple description with reason", func() {
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Pod nginx-pod in namespace default has a CrashLoopBackOff event: Back-off restarting failed container"))
			})

			It("should handle different reasons generically", func() {
				event.Reason = "OOMKilled"
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Pod nginx-pod in namespace default has a OOMKilled event: Back-off restarting failed container"))
			})

			It("should handle missing reason", func() {
				event.Reason = ""
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Pod nginx-pod in namespace default has an issue: Back-off restarting failed container"))
			})

			It("should fallback to event message when no object info", func() {
				event.InvolvedObject = corev1.ObjectReference{}
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Kubernetes event: Back-off restarting failed container"))
			})

			It("should handle missing namespace", func() {
				event.InvolvedObject.Namespace = ""
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Pod nginx-pod has a CrashLoopBackOff event: Back-off restarting failed container"))
			})

			It("should handle empty message gracefully", func() {
				event.Message = ""
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("Pod nginx-pod in namespace default has a CrashLoopBackOff event"))
			})

			It("should include API version for custom resources", func() {
				event.InvolvedObject = corev1.ObjectReference{
					APIVersion: "devopstoolkit.live/v1beta1",
					Kind:       "SQL",
					Name:       "test-db",
					Namespace:  "sql-demo",
				}
				event.Reason = "ComposeResources"
				description := reconciler.generateIssueDescription(event)
				Expect(description).To(Equal("SQL.devopstoolkit.live/v1beta1 test-db in namespace sql-demo has a ComposeResources event: Back-off restarting failed container"))
			})
		})

		Context("MCP Request Structure", func() {
			It("should generate properly formatted MCP request for manual mode", func() {
				selector := dotaiv1alpha1.EventSelector{Mode: "manual"}
				mcpRequest := reconciler.generateMcpRequest(event, policy, selector)

				Expect(mcpRequest.Issue).To(Equal("Pod nginx-pod in namespace default has a CrashLoopBackOff event: Back-off restarting failed container"))
				Expect(mcpRequest.Mode).To(Equal("manual"))
				Expect(mcpRequest.ConfidenceThreshold).To(BeNil())
				Expect(mcpRequest.MaxRiskLevel).To(BeEmpty())
			})

			It("should generate properly formatted MCP request for automatic mode", func() {
				selector := dotaiv1alpha1.EventSelector{
					Mode:                "automatic",
					ConfidenceThreshold: func(f float64) *float64 { return &f }(0.9),
					MaxRiskLevel:        "medium",
				}
				mcpRequest := reconciler.generateMcpRequest(event, policy, selector)

				Expect(mcpRequest.Issue).To(Equal("Pod nginx-pod in namespace default has a CrashLoopBackOff event: Back-off restarting failed container"))
				Expect(mcpRequest.Mode).To(Equal("automatic"))
				Expect(mcpRequest.ConfidenceThreshold).NotTo(BeNil())
				Expect(*mcpRequest.ConfidenceThreshold).To(Equal(0.9))
				Expect(mcpRequest.MaxRiskLevel).To(Equal("medium"))
			})

			It("should generate valid JSON", func() {
				selector := dotaiv1alpha1.EventSelector{Mode: "automatic"}
				mcpRequest := reconciler.generateMcpRequest(event, policy, selector)

				jsonData, err := json.Marshal(mcpRequest)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(jsonData)).To(ContainSubstring("issue"))
				Expect(string(jsonData)).To(ContainSubstring("mode"))
				Expect(string(jsonData)).To(ContainSubstring("confidenceThreshold"))
				Expect(string(jsonData)).To(ContainSubstring("maxRiskLevel"))
			})

			It("should generate different requests for different modes", func() {
				manualSelector := dotaiv1alpha1.EventSelector{Mode: "manual"}
				automaticSelector := dotaiv1alpha1.EventSelector{Mode: "automatic"}

				manualRequest := reconciler.generateMcpRequest(event, policy, manualSelector)
				automaticRequest := reconciler.generateMcpRequest(event, policy, automaticSelector)

				Expect(manualRequest.Mode).To(Equal("manual"))
				Expect(automaticRequest.Mode).To(Equal("automatic"))

				// Manual mode should not have confidence/risk fields
				Expect(manualRequest.ConfidenceThreshold).To(BeNil())
				Expect(manualRequest.MaxRiskLevel).To(BeEmpty())

				// Automatic mode should have these fields with defaults
				Expect(automaticRequest.ConfidenceThreshold).NotTo(BeNil())
				Expect(*automaticRequest.ConfidenceThreshold).To(Equal(0.8)) // Default
				Expect(automaticRequest.MaxRiskLevel).To(Equal("low"))       // Default

				// Issue should be the same
				Expect(manualRequest.Issue).To(Equal(automaticRequest.Issue))
			})
		})

		Context("MCP Request Generation with Logging", func() {
			It("should generate request and return without error", func() {
				selector := dotaiv1alpha1.EventSelector{Mode: "manual"}
				mcpRequest, err := reconciler.generateAndLogMcpRequest(ctx, event, policy, selector)

				Expect(err).NotTo(HaveOccurred())
				Expect(mcpRequest).NotTo(BeNil())
				Expect(mcpRequest.Issue).To(ContainSubstring("CrashLoopBackOff event"))
				Expect(mcpRequest.Mode).To(Equal("manual"))
			})
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
					Mode:        "manual", // Policy default mode
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
						Mode:        "automatic",
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
						Mode:        "manual",
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
						Mode:        "manual",
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
					Mode:        "manual",
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
					Mode:        "manual",
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

	Describe("Slack Notification Integration", func() {
		var (
			slackServer   *httptest.Server
			slackRequests []SlackMessage
			slackMutex    sync.RWMutex
			testPolicy    *dotaiv1alpha1.RemediationPolicy
			testEvent     *corev1.Event
			mockMcpServer *httptest.Server
		)

		BeforeEach(func() {
			// Reset Slack requests
			slackMutex.Lock()
			slackRequests = []SlackMessage{}
			slackMutex.Unlock()

			// Create mock MCP server for Slack tests
			mockMcpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Return successful MCP response
				response := createSuccessfulMcpResponse("Issue has been successfully resolved with 95% confidence", 2500.0)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(response)
			}))

			// Create mock Slack server
			slackServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message SlackMessage
				err := json.NewDecoder(r.Body).Decode(&message)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				slackMutex.Lock()
				slackRequests = append(slackRequests, message)
				slackMutex.Unlock()

				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			}))

			// Create test event
			testEvent = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "slack-test-event-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Name:      "test-pod",
					Namespace: "default",
				},
				Type:    "Warning",
				Reason:  "FailedScheduling",
				Message: "0/1 nodes are available",
			}

			// Create test policy with Slack enabled
			testPolicy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "slack-test-policy-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
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
					McpEndpoint: mockMcpServer.URL,
					Mode:        "manual",
					Notifications: dotaiv1alpha1.NotificationConfig{
						Slack: dotaiv1alpha1.SlackConfig{
							Enabled:          true,
							WebhookUrl:       slackServer.URL,
							Channel:          "#test-channel",
							NotifyOnStart:    true,
							NotifyOnComplete: true,
						},
					},
				},
			}
		})

		AfterEach(func() {
			if slackServer != nil {
				slackServer.Close()
			}
			if mockMcpServer != nil {
				mockMcpServer.Close()
			}
		})

		Context("Slack Configuration Validation", func() {
			It("should accept valid Slack configuration", func() {
				// Use a real Slack webhook URL format for this validation test
				testPolicy.Spec.Notifications.Slack.WebhookUrl = "https://hooks.slack.com/services/T00000000/B00000000/EXAMPLE-TEST-WEBHOOK-URL"
				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept disabled Slack configuration", func() {
				testPolicy.Spec.Notifications.Slack.Enabled = false
				testPolicy.Spec.Notifications.Slack.WebhookUrl = ""

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject enabled Slack without webhook URL", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = ""

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Slack webhook URL is required"))
			})

			It("should reject invalid webhook URL format", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = "http://invalid-url.com"

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid Slack webhook URL format"))
			})

			It("should accept real Slack webhook URL format", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = "https://hooks.slack.com/services/T00000000/B00000000/EXAMPLE-TEST-WEBHOOK-URL"

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Slack Notification Flow", func() {
			BeforeEach(func() {
				// Create the policy and event in the cluster
				err := k8sClient.Create(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				err = k8sClient.Create(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				err := k8sClient.Delete(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should send both start and complete notifications when enabled", func() {
				// Trigger event processing
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Verify both notifications were sent
				Eventually(func() int {
					slackMutex.RLock()
					defer slackMutex.RUnlock()
					return len(slackRequests)
				}, "5s").Should(Equal(2))

				slackMutex.RLock()
				defer slackMutex.RUnlock()

				// Verify start notification
				startNotification := slackRequests[0]
				Expect(startNotification.Attachments).To(HaveLen(1))
				Expect(startNotification.Attachments[0].Title).To(ContainSubstring("Remediation Started"))
				Expect(startNotification.Attachments[0].Color).To(Equal("warning"))
				Expect(startNotification.Channel).To(Equal("#test-channel"))
				Expect(startNotification.Username).To(Equal("dot-ai-controller"))

				// Verify complete notification
				completeNotification := slackRequests[1]
				Expect(completeNotification.Attachments).To(HaveLen(1))
				Expect(completeNotification.Attachments[0].Title).To(ContainSubstring("Remediation Completed"))
				Expect(completeNotification.Attachments[0].Color).To(Equal("good"))
			})

			It("should skip start notification when notifyOnStart is false", func() {
				testPolicy.Spec.Notifications.Slack.NotifyOnStart = false
				err := k8sClient.Update(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Trigger event processing
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Should only get complete notification
				Eventually(func() int {
					slackMutex.RLock()
					defer slackMutex.RUnlock()
					return len(slackRequests)
				}, "5s").Should(Equal(1))

				slackMutex.RLock()
				defer slackMutex.RUnlock()

				notification := slackRequests[0]
				Expect(notification.Attachments[0].Title).To(ContainSubstring("Remediation Completed"))
			})

			It("should skip all notifications when disabled", func() {
				testPolicy.Spec.Notifications.Slack.Enabled = false
				err := k8sClient.Update(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Trigger event processing
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Should get no notifications
				Consistently(func() int {
					slackMutex.RLock()
					defer slackMutex.RUnlock()
					return len(slackRequests)
				}, "2s").Should(Equal(0))
			})
		})

		Context("Slack Message Formatting", func() {
			var (
				mcpRequest  *dotaiv1alpha1.McpRequest
				mcpResponse *McpResponse
			)

			BeforeEach(func() {
				mcpRequest = &dotaiv1alpha1.McpRequest{
					Issue: "Pod test-pod in namespace default is failing to schedule: 0/1 nodes are available",
					Mode:  "automatic",
				}
			})

			It("should format start notification correctly", func() {
				message := reconciler.createSlackMessage(testPolicy, testEvent, "start", mcpRequest, nil)

				Expect(message.Username).To(Equal("dot-ai-controller"))
				Expect(message.IconEmoji).To(Equal(":robot_face:"))
				Expect(message.Channel).To(Equal("#test-channel"))
				Expect(message.Attachments).To(HaveLen(1))

				attachment := message.Attachments[0]
				Expect(attachment.Color).To(Equal("warning"))
				Expect(attachment.Title).To(Equal("🔄 Remediation Started"))
				Expect(attachment.Text).To(ContainSubstring("Started processing event"))
				Expect(attachment.Footer).To(Equal("dot-ai Kubernetes Event Controller"))

				// Verify fields
				fieldTitles := make([]string, len(attachment.Fields))
				for i, field := range attachment.Fields {
					fieldTitles[i] = field.Title
				}
				Expect(fieldTitles).To(ContainElements("Event Type", "Resource", "Namespace", "Mode", "Policy"))
			})

			It("should format successful completion notification correctly", func() {
				mcpResponse = &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						Result: map[string]interface{}{
							"message":    "Issue has been successfully resolved with 95% confidence",
							"confidence": 0.95,
							"executed":   true, // Add executed field to indicate commands were actually run
							"analysis": map[string]interface{}{
								"rootCause":  "Missing PersistentVolumeClaim for pod postgres-test",
								"confidence": 0.95,
							},
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": "kubectl apply -f - <<EOF\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: postgres-storage\n  namespace: default\nspec:\n  accessModes:\n    - ReadWriteOnce\n  resources:\n    requests:\n      storage: 10Gi\nEOF",
									},
								},
							},
							"validation": map[string]interface{}{
								"success": true,
							},
							"results": []interface{}{
								map[string]interface{}{
									"action": "Created PersistentVolumeClaim postgres-storage in namespace default",
									"output": "persistentvolumeclaim/postgres-storage created",
								},
							},
						},
						Tool:          "remediate",
						ExecutionTime: 2500.0, // 2.5 seconds
					},
				}

				message := reconciler.createSlackMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				attachment := message.Attachments[0]
				Expect(attachment.Color).To(Equal("good"))
				Expect(attachment.Title).To(Equal("✅ Remediation Completed Successfully"))
				Expect(attachment.Text).To(ContainSubstring("Issue resolved"))
				Expect(attachment.Text).To(ContainSubstring("95% confidence"))

				// Verify enhanced fields are present
				fieldTitles := make([]string, len(attachment.Fields))
				for i, field := range attachment.Fields {
					fieldTitles[i] = field.Title
				}
				Expect(fieldTitles).To(ContainElements("Confidence", "Execution Time", "Root Cause", "Commands Executed", "Validation", "Actions Taken", "Primary Action"))

				// Find and verify specific field values
				var confidenceField, executionField, validationField *SlackField
				for i := range attachment.Fields {
					switch attachment.Fields[i].Title {
					case "Confidence":
						confidenceField = &attachment.Fields[i]
					case "Execution Time":
						executionField = &attachment.Fields[i]
					case "Validation":
						validationField = &attachment.Fields[i]
					}
				}

				Expect(confidenceField).NotTo(BeNil())
				Expect(confidenceField.Value).To(Equal("95%"))
				Expect(executionField).NotTo(BeNil())
				Expect(executionField.Value).To(Equal("2.50s"))
				Expect(validationField).NotTo(BeNil())
				Expect(validationField.Value).To(Equal("✅ Passed"))
			})

			It("should extract detailed MCP response fields correctly", func() {
				// Create a comprehensive MCP response with all possible fields
				mcpResponse = &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						Result: map[string]interface{}{
							"message":    "Successfully remediated the issue",
							"confidence": 0.87,
							"executed":   true, // Add executed field to indicate commands were actually run
							"analysis": map[string]interface{}{
								"rootCause":  "Pod scheduling failed due to missing PersistentVolume",
								"confidence": 0.92,
								"factors":    []string{"storage", "resources"},
							},
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": "kubectl create -f /tmp/pv.yaml",
									},
									map[string]interface{}{
										"command": "kubectl patch pvc my-pvc -p '{\"spec\":{\"storageClassName\":\"fast-ssd\"}}'",
									},
								},
							},
							"validation": map[string]interface{}{
								"success": true,
								"message": "Pod successfully scheduled after PV creation",
							},
							"results": []interface{}{
								map[string]interface{}{
									"action": "Created PersistentVolume pv-001 with 50Gi capacity",
									"output": "persistentvolume/pv-001 created\npod/test-pod condition changed",
								},
								map[string]interface{}{
									"action": "Updated PVC storage class to fast-ssd",
									"output": "persistentvolumeclaim/my-pvc patched",
								},
							},
						},
						Tool:          "remediate",
						ExecutionTime: 3200.0, // 3.2 seconds
					},
				}

				message := reconciler.createSlackMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)
				attachment := message.Attachments[0]

				// Create map of fields for easy lookup
				fieldMap := make(map[string]string)
				for _, field := range attachment.Fields {
					fieldMap[field.Title] = field.Value
				}

				// Verify all enhanced fields are present with correct values
				Expect(fieldMap["Confidence"]).To(Equal("87%"))
				Expect(fieldMap["Analysis Confidence"]).To(Equal("92%"))
				Expect(fieldMap["Execution Time"]).To(Equal("3.20s"))
				Expect(fieldMap["Root Cause"]).To(Equal("Pod scheduling failed due to missing PersistentVolume"))
				Expect(fieldMap["Validation"]).To(Equal("✅ Passed"))
				Expect(fieldMap["Actions Taken"]).To(Equal("2 remediation actions"))
				Expect(fieldMap["Primary Action"]).To(Equal("Created PersistentVolume pv-001 with 50Gi capacity"))

				// Verify commands are present and formatted correctly
				Expect(fieldMap["Commands Executed"]).To(ContainSubstring("• kubectl create -f /tmp/pv.yaml"))
				Expect(fieldMap["Commands Executed"]).To(ContainSubstring("• kubectl patch pvc my-pvc"))
			})

			It("should format manual mode completion notification correctly", func() {
				// Create MCP response with executed=false (manual mode - recommendations only)
				mcpResponse = &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						Result: map[string]interface{}{
							"message":    "AI analysis identified the root cause with 95% confidence. 1 remediation actions are recommended.",
							"confidence": 0.95,
							"executed":   false, // Commands were NOT executed - just recommended
							"analysis": map[string]interface{}{
								"rootCause":  "The CompositeResourceDefinition (XRD) 'sqls.devopstoolkit.live' has an incorrect defaultCompositionRef",
								"confidence": 0.95,
							},
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": "kubectl patch compositeresourcedefinition/sqls.devopstoolkit.live --type=merge -p '{\"spec\":{\"defaultCompositionRef\":{\"name\":\"google-postgresql\"}}}'",
									},
								},
							},
						},
						Tool:          "remediate",
						ExecutionTime: 88680.0, // 88.68 seconds
					},
				}

				message := reconciler.createSlackMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				attachment := message.Attachments[0]
				Expect(attachment.Color).To(Equal("#3AA3E3"))
				Expect(attachment.Title).To(Equal("📋 Analysis Completed - Manual Action Required"))
				Expect(attachment.Text).To(ContainSubstring("Analysis completed"))

				// Verify that commands are labeled as "Recommended Commands" not "Commands Executed"
				fieldMap := make(map[string]string)
				for _, field := range attachment.Fields {
					fieldMap[field.Title] = field.Value
				}

				Expect(fieldMap["Recommended Commands"]).To(ContainSubstring("kubectl patch compositeresourcedefinition"))
				_, hasExecutedCommands := fieldMap["Commands Executed"]
				Expect(hasExecutedCommands).To(BeFalse()) // Should NOT have "Commands Executed" field
			})

			It("should format failed completion notification correctly", func() {
				mcpResponse = &McpResponse{
					Success: false,
					Error: &struct {
						Code    string                 `json:"code"`
						Message string                 `json:"message"`
						Details map[string]interface{} `json:"details,omitempty"`
					}{
						Code:    "insufficient_permissions",
						Message: "Unable to create PersistentVolumeClaim: insufficient permissions",
						Details: map[string]interface{}{
							"reason": "RBAC insufficient",
						},
					},
				}

				message := reconciler.createSlackMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				attachment := message.Attachments[0]
				Expect(attachment.Color).To(Equal("danger"))
				Expect(attachment.Title).To(Equal("❌ Remediation Failed"))
				Expect(attachment.Text).To(ContainSubstring("Remediation failed"))
				Expect(attachment.Text).To(ContainSubstring("insufficient permissions"))

				// Verify enhanced error fields are present
				fieldMap := make(map[string]string)
				for _, field := range attachment.Fields {
					fieldMap[field.Title] = field.Value
				}

				Expect(fieldMap["Error Code"]).To(Equal("insufficient_permissions"))
				Expect(fieldMap["Error Details"]).To(Equal("RBAC insufficient"))
			})
		})

		Context("Slack HTTP Error Handling", func() {
			var failingSlackServer *httptest.Server

			BeforeEach(func() {
				// Create a server that always returns 500
				failingSlackServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("Internal Server Error"))
				}))

				testPolicy.Spec.Notifications.Slack.WebhookUrl = failingSlackServer.URL
				testPolicy.Spec.McpEndpoint = mockMcpServer.URL

				err := k8sClient.Create(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				err = k8sClient.Create(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				failingSlackServer.Close()
				err := k8sClient.Delete(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				err = k8sClient.Delete(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should continue event processing even when Slack notifications fail", func() {
				// Event processing should succeed even with Slack failures
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Verify the policy status was still updated (MCP processing succeeded)
				Eventually(func() int64 {
					var updatedPolicy dotaiv1alpha1.RemediationPolicy
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      testPolicy.Name,
						Namespace: testPolicy.Namespace,
					}, &updatedPolicy)
					if err != nil {
						return 0
					}
					return updatedPolicy.Status.TotalEventsProcessed
				}, "5s").Should(BeNumerically(">", 0))
			})
		})
	})
})
