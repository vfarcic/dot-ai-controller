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
					Mode:        "manual",
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

				// Give some time for any potential notifications
				time.Sleep(500 * time.Millisecond)

				// Verify no notifications were sent
				googleChatMutex.RLock()
				defer googleChatMutex.RUnlock()
				Expect(len(googleChatRequests)).To(Equal(0))
			})
		})
	})
})
