package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("RemediationPolicy MCP", func() {
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
})
