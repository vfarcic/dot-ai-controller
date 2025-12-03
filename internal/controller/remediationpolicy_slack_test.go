package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("RemediationPolicy Slack Notifications", func() {
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
				Expect(err.Error()).To(ContainSubstring("Slack webhook URL or webhookUrlSecretRef is required"))
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

			It("should accept valid Secret reference configuration", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = ""
				testPolicy.Spec.Notifications.Slack.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "slack-url",
				}

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept both plain text and Secret reference (for migration scenarios)", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = "https://hooks.slack.com/services/T00000000/B00000000/EXAMPLE-TEST-WEBHOOK-URL"
				testPolicy.Spec.Notifications.Slack.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "slack-url",
				}

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject Secret reference with empty name", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = ""
				testPolicy.Spec.Notifications.Slack.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "",
					Key:  "slack-url",
				}

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("name cannot be empty"))
			})

			It("should reject Secret reference with empty key", func() {
				testPolicy.Spec.Notifications.Slack.WebhookUrl = ""
				testPolicy.Spec.Notifications.Slack.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "",
				}

				err := reconciler.validateSlackConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("key cannot be empty"))
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
				Expect(startNotification.Attachments[0].Color).To(Equal("#f2994a")) // Orange for start
				Expect(startNotification.Attachments[0].Blocks).To(Not(BeEmpty()))
				Expect(startNotification.Attachments[0].Blocks[0].Type).To(Equal("header"))
				Expect(startNotification.Attachments[0].Blocks[0].Text.Text).To(ContainSubstring("Remediation Started"))
				Expect(startNotification.Channel).To(Equal("#test-channel"))
				Expect(startNotification.Username).To(Equal("dot-ai-controller"))

				// Verify complete notification
				completeNotification := slackRequests[1]
				Expect(completeNotification.Attachments).To(HaveLen(1))
				Expect(completeNotification.Attachments[0].Blocks).To(Not(BeEmpty()))
				Expect(completeNotification.Attachments[0].Blocks[0].Type).To(Equal("header"))
				Expect(completeNotification.Attachments[0].Blocks[0].Text.Text).To(ContainSubstring("Remediation Completed"))
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
				Expect(notification.Attachments).To(HaveLen(1))
				Expect(notification.Attachments[0].Blocks).To(Not(BeEmpty()))
				Expect(notification.Attachments[0].Blocks[0].Type).To(Equal("header"))
				Expect(notification.Attachments[0].Blocks[0].Text.Text).To(ContainSubstring("Remediation Completed"))
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
				Expect(message.Attachments[0].Color).To(Equal("#f2994a")) // Orange for start
				Expect(message.Attachments[0].Blocks).To(Not(BeEmpty()))

				blocks := message.Attachments[0].Blocks

				// Verify header block
				Expect(blocks[0].Type).To(Equal("header"))
				Expect(blocks[0].Text.Text).To(Equal("🔄 Remediation Started"))

				// Verify has section blocks with fields
				hasFieldSection := false
				for _, block := range blocks {
					if block.Type == "section" && len(block.Fields) > 0 {
						hasFieldSection = true
						break
					}
				}
				Expect(hasFieldSection).To(BeTrue())

				// Verify context block (footer)
				lastBlock := blocks[len(blocks)-1]
				Expect(lastBlock.Type).To(Equal("context"))
				Expect(lastBlock.Elements[0].Text).To(ContainSubstring("dot-ai Kubernetes Event Controller"))
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

				Expect(message.Attachments).To(HaveLen(1))
				Expect(message.Attachments[0].Color).To(Equal("#2eb67d")) // Green for success
				Expect(message.Attachments[0].Blocks).To(Not(BeEmpty()))

				blocks := message.Attachments[0].Blocks

				// Verify header block
				Expect(blocks[0].Type).To(Equal("header"))
				Expect(blocks[0].Text.Text).To(Equal("✅ Remediation Completed Successfully"))

				// Find result section
				var resultText string
				for _, block := range blocks {
					if block.Type == "section" && block.Text != nil && block.Text.Type == "mrkdwn" {
						if len(resultText) == 0 {
							resultText = block.Text.Text
						}
					}
				}
				Expect(resultText).To(ContainSubstring("Issue"))
				Expect(resultText).To(ContainSubstring("95% confidence"))

				// Verify sections contain expected information
				var hasConfidence, hasExecutionTime, hasRootCause, hasCommands, hasValidation bool
				for _, block := range blocks {
					if block.Type == "section" {
						if block.Text != nil {
							text := block.Text.Text
							if len(text) > 0 {
								if matchString(text, "95%") {
									hasConfidence = true
								}
								if matchString(text, "Root Cause") {
									hasRootCause = true
								}
								if matchString(text, "Commands Executed") || matchString(text, "kubectl apply") {
									hasCommands = true
								}
								if matchString(text, "Validation") {
									hasValidation = true
								}
							}
						}
						if len(block.Fields) > 0 {
							for _, field := range block.Fields {
								if matchString(field.Text, "2.50s") {
									hasExecutionTime = true
								}
								if matchString(field.Text, "95%") {
									hasConfidence = true
								}
							}
						}
					}
				}

				Expect(hasConfidence).To(BeTrue(), "Should have confidence information")
				Expect(hasExecutionTime).To(BeTrue(), "Should have execution time")
				Expect(hasRootCause).To(BeTrue(), "Should have root cause")
				Expect(hasCommands).To(BeTrue(), "Should have commands")
				Expect(hasValidation).To(BeTrue(), "Should have validation")
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

				Expect(message.Attachments).To(HaveLen(1))
				Expect(message.Attachments[0].Blocks).To(Not(BeEmpty()))

				blocks := message.Attachments[0].Blocks

				// Collect all text content from blocks
				var allText string
				for _, block := range blocks {
					if block.Text != nil {
						allText += block.Text.Text + " "
					}
					for _, field := range block.Fields {
						allText += field.Text + " "
					}
				}

				// Verify all enhanced fields are present with correct values
				Expect(allText).To(ContainSubstring("87%"), "Should contain confidence 87%")
				Expect(allText).To(ContainSubstring("92%"), "Should contain analysis confidence 92%")
				Expect(allText).To(ContainSubstring("3.20s"), "Should contain execution time")
				Expect(allText).To(ContainSubstring("Pod scheduling failed due to missing PersistentVolume"), "Should contain root cause")
				Expect(allText).To(ContainSubstring("Passed"), "Should contain validation status")
				Expect(allText).To(ContainSubstring("2 remediation actions"), "Should contain action count")

				// Verify commands are present in code blocks (NOT TRUNCATED!)
				Expect(allText).To(ContainSubstring("kubectl create -f /tmp/pv.yaml"), "Should contain first command")
				Expect(allText).To(ContainSubstring("kubectl patch pvc my-pvc"), "Should contain second command")
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

				Expect(message.Attachments).To(HaveLen(1))
				Expect(message.Attachments[0].Color).To(Equal("#0073e6")) // Blue for manual mode
				Expect(message.Attachments[0].Blocks).To(Not(BeEmpty()))

				blocks := message.Attachments[0].Blocks

				// Verify header block
				Expect(blocks[0].Type).To(Equal("header"))
				Expect(blocks[0].Text.Text).To(Equal("📋 Analysis Completed - Manual Action Required"))

				// Collect all text
				var allText string
				for _, block := range blocks {
					if block.Text != nil {
						allText += block.Text.Text + " "
					}
				}

				// Verify that commands are labeled as "Recommended Commands" not "Commands Executed"
				Expect(allText).To(ContainSubstring("Recommended Commands"))
				Expect(allText).To(ContainSubstring("kubectl patch compositeresourcedefinition"))
				Expect(allText).NotTo(ContainSubstring("Commands Executed")) // Should NOT have "Commands Executed"
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

				Expect(message.Attachments).To(HaveLen(1))
				Expect(message.Attachments[0].Color).To(Equal("#e01e5a")) // Red for failure
				Expect(message.Attachments[0].Blocks).To(Not(BeEmpty()))

				blocks := message.Attachments[0].Blocks

				// Verify header block
				Expect(blocks[0].Type).To(Equal("header"))
				Expect(blocks[0].Text.Text).To(Equal("❌ Remediation Failed"))

				// Collect all text
				var allText string
				for _, block := range blocks {
					if block.Text != nil {
						allText += block.Text.Text + " "
					}
				}

				Expect(allText).To(Or(ContainSubstring("Remediation failed"), ContainSubstring("Unable to create")))
				Expect(allText).To(ContainSubstring("insufficient permissions"))

				// Verify enhanced error fields are present
				Expect(allText).To(ContainSubstring("insufficient_permissions"))
				Expect(allText).To(ContainSubstring("RBAC insufficient"))
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
