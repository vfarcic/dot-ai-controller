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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("RemediationPolicy Google Chat Notifications", func() {
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

	Describe("Google Chat Notification Integration", func() {
		var (
			googleChatServer   *httptest.Server
			googleChatRequests []GoogleChatMessage
			googleChatMutex    sync.RWMutex
			testPolicy         *dotaiv1alpha1.RemediationPolicy
			testEvent          *corev1.Event
			mockMcpServer      *httptest.Server
		)

		BeforeEach(func() {
			// Reset Google Chat requests
			googleChatMutex.Lock()
			googleChatRequests = []GoogleChatMessage{}
			googleChatMutex.Unlock()

			// Create mock MCP server for Google Chat tests
			mockMcpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Return successful MCP response
				response := createSuccessfulMcpResponse("Issue has been successfully resolved with 95% confidence", 2500.0)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(response)
			}))

			// Create mock Google Chat server
			googleChatServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message GoogleChatMessage
				err := json.NewDecoder(r.Body).Decode(&message)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				googleChatMutex.Lock()
				googleChatRequests = append(googleChatRequests, message)
				googleChatMutex.Unlock()

				w.WriteHeader(http.StatusOK)
				w.Write([]byte("{}"))
			}))

			// Create test event
			testEvent = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gchat-test-event-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
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

			// Create test policy with Google Chat enabled
			testPolicy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gchat-test-policy-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
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
					McpAuthSecretRef: dotaiv1alpha1.SecretReference{
						Name: "mcp-auth-secret",
						Key:  "api-key",
					},
					Mode: "manual",
					Notifications: dotaiv1alpha1.NotificationConfig{
						GoogleChat: dotaiv1alpha1.GoogleChatConfig{
							Enabled:          true,
							WebhookUrl:       googleChatServer.URL,
							NotifyOnStart:    true,
							NotifyOnComplete: true,
						},
					},
				},
			}
		})

		AfterEach(func() {
			if googleChatServer != nil {
				googleChatServer.Close()
			}
			if mockMcpServer != nil {
				mockMcpServer.Close()
			}
		})

		Context("Google Chat Configuration Validation", func() {
			It("should accept valid Google Chat configuration", func() {
				// Use a real Google Chat webhook URL format for this validation test
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=test&token=test"
				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept disabled Google Chat configuration", func() {
				testPolicy.Spec.Notifications.GoogleChat.Enabled = false
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = ""

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject enabled Google Chat without webhook URL", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = ""

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Google Chat webhook URL or webhookUrlSecretRef is required"))
			})

			It("should reject invalid webhook URL format", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = "http://invalid-url.com"

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid Google Chat webhook URL format"))
			})

			It("should accept real Google Chat webhook URL format", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=test&token=test"

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept valid Secret reference configuration", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = ""
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "gchat-url",
				}

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should accept both plain text and Secret reference (for migration scenarios)", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=test&token=test"
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "gchat-url",
				}

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject Secret reference with empty name", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = ""
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "",
					Key:  "gchat-url",
				}

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("name cannot be empty"))
			})

			It("should reject Secret reference with empty key", func() {
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrl = ""
				testPolicy.Spec.Notifications.GoogleChat.WebhookUrlSecretRef = &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "",
				}

				err := reconciler.validateGoogleChatConfiguration(testPolicy)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("key cannot be empty"))
			})
		})

		Context("Google Chat Notification Flow", func() {
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
					googleChatMutex.RLock()
					defer googleChatMutex.RUnlock()
					return len(googleChatRequests)
				}, "5s").Should(Equal(2))

				googleChatMutex.RLock()
				defer googleChatMutex.RUnlock()

				// Verify start notification
				startNotification := googleChatRequests[0]
				Expect(startNotification.CardsV2).To(HaveLen(1))
				Expect(startNotification.CardsV2[0].Card.Header.Title).To(ContainSubstring("Remediation Started"))
				Expect(startNotification.CardsV2[0].Card.Sections).To(Not(BeEmpty()))

				// Verify complete notification
				completeNotification := googleChatRequests[1]
				Expect(completeNotification.CardsV2).To(HaveLen(1))
				Expect(completeNotification.CardsV2[0].Card.Header.Title).To(ContainSubstring("Remediation Completed"))
			})

			It("should skip start notification when notifyOnStart is false", func() {
				// Update policy to disable start notifications
				// This works because NotifyOnStart default is false, so when omitempty
				// omits the false value, the API server applies default=false
				testPolicy.Spec.Notifications.GoogleChat.NotifyOnStart = false
				err := k8sClient.Update(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Trigger event processing
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Should only get complete notification
				Eventually(func() int {
					googleChatMutex.RLock()
					defer googleChatMutex.RUnlock()
					return len(googleChatRequests)
				}, "5s").Should(Equal(1))

				googleChatMutex.RLock()
				defer googleChatMutex.RUnlock()

				// Should be the complete notification
				Expect(googleChatRequests[0].CardsV2[0].Card.Header.Title).To(ContainSubstring("Completed"))
			})

			// Note: "skip complete notification when notifyOnComplete is false" test is not included
			// because it cannot work with current CRD defaults + omitempty behavior.
			// When NotifyOnComplete=false is set, JSON omits it, and CRD default (true) is applied.
			// This is the same limitation as Slack tests - only "skip start" is tested.

			It("should not send any notification when Google Chat is disabled", func() {
				// Update policy to disable Google Chat notifications
				// This works because Enabled default is false
				testPolicy.Spec.Notifications.GoogleChat.Enabled = false
				err := k8sClient.Update(ctx, testPolicy)
				Expect(err).NotTo(HaveOccurred())

				// Trigger event processing
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Verify no notifications were sent
				Consistently(func() int {
					googleChatMutex.RLock()
					defer googleChatMutex.RUnlock()
					return len(googleChatRequests)
				}, "2s").Should(Equal(0))
			})
		})

		Context("HTML Special Character Escaping (Issue #37)", func() {
			// These tests verify that commands and text containing HTML special characters
			// like <, >, & are properly escaped to prevent truncation in Google Chat.
			// See: https://github.com/vfarcic/dot-ai-controller/issues/37
			//
			// Note: When testing JSON output, html.EscapeString() converts:
			//   < -> &lt;
			//   > -> &gt;
			//   & -> &amp;
			// But json.Marshal() then encodes & as \u0026, so in JSON:
			//   &lt; -> \u0026lt;
			//   &gt; -> \u0026gt;
			//   &amp; -> \u0026amp;
			// We verify the struct content directly to avoid JSON encoding complexity.

			It("should escape < characters in commands to prevent truncation", func() {
				// Create MCP response with commands containing < characters
				mcpResponse := &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						ExecutionTime: 1500,
						Result: map[string]interface{}{
							"message":    "Remediation completed",
							"confidence": 0.95,
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": `kubectl patch pvc my-pvc --type='json' -p '[{"op":"replace","path":"<spec>","value":"test"}]'`,
									},
								},
								"executed": false,
							},
						},
					},
				}

				mcpRequest := &dotaiv1alpha1.McpRequest{
					Mode:  "manual",
					Issue: "PVC storage issue with <important> data",
				}

				// Create message
				message := reconciler.createGoogleChatMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				// Verify the struct content directly
				// Find the "Recommended Commands" section and check the command widget
				var commandSection *GoogleChatSection
				var issueSection *GoogleChatSection
				for i := range message.CardsV2[0].Card.Sections {
					if message.CardsV2[0].Card.Sections[i].Header == "Recommended Commands" {
						commandSection = &message.CardsV2[0].Card.Sections[i]
					}
					if message.CardsV2[0].Card.Sections[i].Header == "Original Issue" {
						issueSection = &message.CardsV2[0].Card.Sections[i]
					}
				}

				Expect(commandSection).NotTo(BeNil(), "Should have Recommended Commands section")
				Expect(commandSection.Widgets).NotTo(BeEmpty())

				// Verify the command text contains escaped < and >
				commandText := commandSection.Widgets[0].TextParagraph.Text
				Expect(commandText).To(ContainSubstring("&lt;spec&gt;"))
				Expect(commandText).NotTo(ContainSubstring(`"<spec>"`))

				// Verify the issue text is also escaped
				Expect(issueSection).NotTo(BeNil(), "Should have Original Issue section")
				issueText := issueSection.Widgets[0].TextParagraph.Text
				Expect(issueText).To(ContainSubstring("&lt;important&gt;"))
			})

			It("should escape > and & characters in commands", func() {
				mcpResponse := &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						ExecutionTime: 1500,
						Result: map[string]interface{}{
							"message":    "Remediation completed",
							"confidence": 0.95,
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": `echo "value > 10 && value < 20" | grep test`,
									},
								},
								"executed": false,
							},
						},
					},
				}

				mcpRequest := &dotaiv1alpha1.McpRequest{
					Mode:  "manual",
					Issue: "Check if value > threshold && retry",
				}

				message := reconciler.createGoogleChatMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				// Find the command section
				var commandSection *GoogleChatSection
				var issueSection *GoogleChatSection
				for i := range message.CardsV2[0].Card.Sections {
					if message.CardsV2[0].Card.Sections[i].Header == "Recommended Commands" {
						commandSection = &message.CardsV2[0].Card.Sections[i]
					}
					if message.CardsV2[0].Card.Sections[i].Header == "Original Issue" {
						issueSection = &message.CardsV2[0].Card.Sections[i]
					}
				}

				Expect(commandSection).NotTo(BeNil())
				commandText := commandSection.Widgets[0].TextParagraph.Text

				// Verify all special characters are escaped
				Expect(commandText).To(ContainSubstring("&gt;"))
				Expect(commandText).To(ContainSubstring("&lt;"))
				Expect(commandText).To(ContainSubstring("&amp;"))

				// Verify issue text is also escaped
				Expect(issueSection).NotTo(BeNil())
				issueText := issueSection.Widgets[0].TextParagraph.Text
				Expect(issueText).To(ContainSubstring("&gt;"))
				Expect(issueText).To(ContainSubstring("&amp;"))
			})

			It("should escape special characters in root cause analysis", func() {
				mcpResponse := &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						ExecutionTime: 1500,
						Result: map[string]interface{}{
							"message":    "Analysis completed",
							"confidence": 0.90,
							"analysis": map[string]interface{}{
								"rootCause":  "Pod failed because memory < 512MB && CPU > 90%",
								"confidence": 0.85,
							},
						},
					},
				}

				mcpRequest := &dotaiv1alpha1.McpRequest{
					Mode:  "manual",
					Issue: "Pod OOM killed",
				}

				message := reconciler.createGoogleChatMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				// Find the Analysis section
				var analysisSection *GoogleChatSection
				for i := range message.CardsV2[0].Card.Sections {
					if message.CardsV2[0].Card.Sections[i].Header == "Analysis" {
						analysisSection = &message.CardsV2[0].Card.Sections[i]
						break
					}
				}

				Expect(analysisSection).NotTo(BeNil(), "Should have Analysis section")
				Expect(analysisSection.Widgets).NotTo(BeEmpty())

				// Find the text paragraph with root cause
				var rootCauseText string
				for _, widget := range analysisSection.Widgets {
					if widget.TextParagraph != nil {
						rootCauseText = widget.TextParagraph.Text
						break
					}
				}

				// Verify root cause special characters are escaped
				Expect(rootCauseText).To(ContainSubstring("&lt;"))
				Expect(rootCauseText).To(ContainSubstring("&gt;"))
				Expect(rootCauseText).To(ContainSubstring("&amp;"))
			})

			It("should escape special characters in error details", func() {
				mcpResponse := &McpResponse{
					Success: false,
					Error: &struct {
						Code    string                 `json:"code"`
						Message string                 `json:"message"`
						Details map[string]interface{} `json:"details,omitempty"`
					}{
						Code:    "REMEDIATION_FAILED",
						Message: "Failed to apply fix",
						Details: map[string]interface{}{
							"reason": "Value must be < 100 && > 0",
						},
					},
				}

				mcpRequest := &dotaiv1alpha1.McpRequest{
					Mode:  "manual",
					Issue: "Test issue",
				}

				message := reconciler.createGoogleChatMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				// Find the Error section
				var errorSection *GoogleChatSection
				for i := range message.CardsV2[0].Card.Sections {
					if message.CardsV2[0].Card.Sections[i].Header == "Error" {
						errorSection = &message.CardsV2[0].Card.Sections[i]
						break
					}
				}

				Expect(errorSection).NotTo(BeNil(), "Should have Error section")

				// Find the text paragraph with error details
				var errorDetailsText string
				for _, widget := range errorSection.Widgets {
					if widget.TextParagraph != nil {
						errorDetailsText = widget.TextParagraph.Text
						break
					}
				}

				// Verify error details special characters are escaped
				Expect(errorDetailsText).To(ContainSubstring("&lt;"))
				Expect(errorDetailsText).To(ContainSubstring("&gt;"))
				Expect(errorDetailsText).To(ContainSubstring("&amp;"))
			})

			It("should preserve full command with special characters without truncation", func() {
				// This test specifically verifies the fix for issue #37
				// Commands should not be truncated at < character
				fullCommand := `kubectl patch pvc data-pvc --type='json' -p '[{"op":"replace","path":"/spec/resources/requests/storage","value":"<SIZE>"}]' --namespace=production`

				mcpResponse := &McpResponse{
					Success: true,
					Data: &struct {
						Result        map[string]interface{} `json:"result"`
						Tool          string                 `json:"tool"`
						ExecutionTime float64                `json:"executionTime"`
					}{
						ExecutionTime: 1500,
						Result: map[string]interface{}{
							"message":    "Remediation completed",
							"confidence": 0.95,
							"remediation": map[string]interface{}{
								"actions": []interface{}{
									map[string]interface{}{
										"command": fullCommand,
									},
								},
								"executed": false,
							},
						},
					},
				}

				mcpRequest := &dotaiv1alpha1.McpRequest{
					Mode:  "manual",
					Issue: "Storage issue",
				}

				message := reconciler.createGoogleChatMessage(testPolicy, testEvent, "complete", mcpRequest, mcpResponse)

				// Find the command section
				var commandSection *GoogleChatSection
				for i := range message.CardsV2[0].Card.Sections {
					if message.CardsV2[0].Card.Sections[i].Header == "Recommended Commands" {
						commandSection = &message.CardsV2[0].Card.Sections[i]
						break
					}
				}

				Expect(commandSection).NotTo(BeNil())
				commandText := commandSection.Widgets[0].TextParagraph.Text

				// The command should contain ALL parts, including after the < character
				// Verify the part after < is present (escaped)
				Expect(commandText).To(ContainSubstring("SIZE"))
				Expect(commandText).To(ContainSubstring("--namespace=production"))

				// Verify the full command is present with proper escaping
				escapedCommand := strings.ReplaceAll(fullCommand, "&", "&amp;")
				escapedCommand = strings.ReplaceAll(escapedCommand, "<", "&lt;")
				escapedCommand = strings.ReplaceAll(escapedCommand, ">", "&gt;")
				escapedCommand = strings.ReplaceAll(escapedCommand, "'", "&#39;")
				escapedCommand = strings.ReplaceAll(escapedCommand, "\"", "&#34;")
				Expect(commandText).To(ContainSubstring(escapedCommand))
			})
		})
	})
})
