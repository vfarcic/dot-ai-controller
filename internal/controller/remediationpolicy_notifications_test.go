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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("RemediationPolicy Notifications", func() {
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

	Describe("Secret Resolution (resolveWebhookUrl)", func() {
		// Helper function to create unique namespace for each test
		createTestNamespace := func() string {
			nsName := fmt.Sprintf("test-secrets-%d", time.Now().UnixNano())
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			return nsName
		}

		cleanupTestNamespace := func(nsName string) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: nsName,
				},
			}
			k8sClient.Delete(ctx, ns)
		}

		Context("Secret reference provided", func() {
			It("should resolve webhook URL from Secret successfully", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				// Create a Secret with webhook URL
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "webhook-secret",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"slack-url": []byte("https://hooks.slack.com/services/TEST/URL/SECRET"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				// Test resolution
				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret",
					Key:  "slack-url",
				}

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", secretRef, "Slack")
				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal("https://hooks.slack.com/services/TEST/URL/SECRET"))
			})

			It("should resolve Google Chat webhook URL from Secret successfully", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				// Create a Secret with Google Chat webhook URL
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gchat-webhook-secret",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"gchat-url": []byte("https://chat.googleapis.com/v1/spaces/AAAA/messages?key=test"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				// Test resolution
				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "gchat-webhook-secret",
					Key:  "gchat-url",
				}

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", secretRef, "Google Chat")
				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal("https://chat.googleapis.com/v1/spaces/AAAA/messages?key=test"))
			})

			It("should return error when Secret not found", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "nonexistent-secret",
					Key:  "slack-url",
				}

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", secretRef, "Slack")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("not found"))
				Expect(err.Error()).To(ContainSubstring("nonexistent-secret"))
				Expect(url).To(BeEmpty())
			})

			It("should return error when Secret key not found", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				// Create Secret without expected key
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "webhook-secret-wrong-key",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"wrong-key": []byte("some-value"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret-wrong-key",
					Key:  "slack-url",
				}

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", secretRef, "Slack")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("does not contain key"))
				Expect(err.Error()).To(ContainSubstring("slack-url"))
				Expect(url).To(BeEmpty())
			})

			It("should return error when Secret key is empty", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "webhook-secret-empty",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"slack-url": []byte(""),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret-empty",
					Key:  "slack-url",
				}

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", secretRef, "Slack")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("is empty"))
				Expect(url).To(BeEmpty())
			})
		})

		Context("Preference logic (both fields provided)", func() {
			It("should prefer Secret reference over plain text", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "webhook-secret-preference",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"slack-url": []byte("https://hooks.slack.com/services/SECRET/URL"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "webhook-secret-preference",
					Key:  "slack-url",
				}

				// Both fields provided - Secret should win
				plainUrl := "https://hooks.slack.com/services/PLAIN/URL"
				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, plainUrl, secretRef, "Slack")

				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal("https://hooks.slack.com/services/SECRET/URL"))
				Expect(url).NotTo(Equal(plainUrl), "Should use Secret URL, not plain text URL")
			})

			It("should prefer Secret reference over plain text for Google Chat", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "gchat-secret-preference",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"gchat-url": []byte("https://chat.googleapis.com/v1/spaces/SECRET/messages"),
					},
				}
				Expect(k8sClient.Create(ctx, secret)).To(Succeed())

				secretRef := &dotaiv1alpha1.SecretReference{
					Name: "gchat-secret-preference",
					Key:  "gchat-url",
				}

				// Both fields provided - Secret should win
				plainUrl := "https://chat.googleapis.com/v1/spaces/PLAIN/messages"
				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, plainUrl, secretRef, "Google Chat")

				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal("https://chat.googleapis.com/v1/spaces/SECRET/messages"))
				Expect(url).NotTo(Equal(plainUrl))
			})
		})

		Context("Plain text URL (deprecated)", func() {
			It("should return plain text URL when only plain text provided", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				plainUrl := "https://hooks.slack.com/services/PLAIN/URL"
				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, plainUrl, nil, "Slack")

				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal(plainUrl))
			})

			It("should return plain text Google Chat URL when only plain text provided", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				plainUrl := "https://chat.googleapis.com/v1/spaces/AAAA/messages"
				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, plainUrl, nil, "Google Chat")

				Expect(err).NotTo(HaveOccurred())
				Expect(url).To(Equal(plainUrl))
			})
		})

		Context("Neither field provided", func() {
			It("should return error when no webhook URL configured for Slack", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", nil, "Slack")

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no webhook URL configured"))
				Expect(err.Error()).To(ContainSubstring("Slack"))
				Expect(url).To(BeEmpty())
			})

			It("should return error when no webhook URL configured for Google Chat", func() {
				testNamespace := createTestNamespace()
				defer cleanupTestNamespace(testNamespace)

				url, err := reconciler.resolveWebhookUrl(ctx, testNamespace, "", nil, "Google Chat")

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no webhook URL configured"))
				Expect(err.Error()).To(ContainSubstring("Google Chat"))
				Expect(url).To(BeEmpty())
			})
		})
	})

	Describe("Secret-based Notification Integration", func() {
		var (
			slackServer   *httptest.Server
			slackRequests []SlackMessage
			slackMutex    sync.RWMutex
			mockMcpServer *httptest.Server
		)

		BeforeEach(func() {
			// Reset Slack requests
			slackMutex.Lock()
			slackRequests = []SlackMessage{}
			slackMutex.Unlock()

			// Create mock MCP server
			mockMcpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := createSuccessfulMcpResponse("Issue resolved successfully", 1500.0)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(response)
			}))

			// Create mock Slack server
			slackServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message SlackMessage
				if err := json.NewDecoder(r.Body).Decode(&message); err == nil {
					slackMutex.Lock()
					slackRequests = append(slackRequests, message)
					slackMutex.Unlock()
				}
				w.WriteHeader(http.StatusOK)
			}))
		})

		AfterEach(func() {
			if slackServer != nil {
				slackServer.Close()
			}
			if mockMcpServer != nil {
				mockMcpServer.Close()
			}
		})

		Context("Slack notifications with Secret references", func() {
			It("should send notifications using Secret-based webhook URL", func() {
				testNamespace := fmt.Sprintf("test-notif-%d", time.Now().UnixNano())

				// Create namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNamespace,
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
				defer k8sClient.Delete(ctx, ns)

				// Create Secret with Slack webhook URL
				webhookSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "notification-webhooks",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"slack-url": []byte(slackServer.URL),
					},
				}
				Expect(k8sClient.Create(ctx, webhookSecret)).To(Succeed())

				// Create policy using Secret reference
				testPolicy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-policy-secret",
						Namespace: testNamespace,
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
								Enabled: true,
								WebhookUrlSecretRef: &dotaiv1alpha1.SecretReference{
									Name: "notification-webhooks",
									Key:  "slack-url",
								},
								Channel:          "#test-channel",
								NotifyOnStart:    true,
								NotifyOnComplete: true,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, testPolicy)).To(Succeed())
				defer k8sClient.Delete(ctx, testPolicy)

				// Create test event
				testEvent := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-event-secret",
						Namespace: testNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: testNamespace,
					},
					Type:    "Warning",
					Reason:  "FailedScheduling",
					Message: "Pod cannot be scheduled",
				}
				Expect(k8sClient.Create(ctx, testEvent)).To(Succeed())
				defer k8sClient.Delete(ctx, testEvent)

				// Trigger reconciliation
				result, err := reconciler.reconcileEvent(ctx, testEvent)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				// Verify notifications were sent
				Eventually(func() int {
					slackMutex.RLock()
					defer slackMutex.RUnlock()
					return len(slackRequests)
				}, "5s", "100ms").Should(BeNumerically(">=", 1))

				// Verify NotificationsHealthy condition is True
				Eventually(func() bool {
					updated := &dotaiv1alpha1.RemediationPolicy{}
					k8sClient.Get(ctx, types.NamespacedName{
						Name:      testPolicy.Name,
						Namespace: testPolicy.Namespace,
					}, updated)

					for _, condition := range updated.Status.Conditions {
						if condition.Type == "NotificationsHealthy" {
							return condition.Status == metav1.ConditionTrue
						}
					}
					return false
				}, "5s", "100ms").Should(BeTrue())
			})

			It("should update NotificationsHealthy condition on Secret resolution failure", func() {
				testNamespace := fmt.Sprintf("test-notif-%d", time.Now().UnixNano())

				// Create namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNamespace,
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
				defer k8sClient.Delete(ctx, ns)

				// Create policy referencing non-existent Secret
				testPolicy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-policy-missing-secret",
						Namespace: testNamespace,
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
								Enabled: true,
								WebhookUrlSecretRef: &dotaiv1alpha1.SecretReference{
									Name: "nonexistent-secret",
									Key:  "slack-url",
								},
								Channel:          "#test-channel",
								NotifyOnStart:    true,
								NotifyOnComplete: true,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, testPolicy)).To(Succeed())
				defer k8sClient.Delete(ctx, testPolicy)

				// Create test event
				testEvent := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-event-missing-secret",
						Namespace: testNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: testNamespace,
					},
					Type:    "Warning",
					Reason:  "FailedScheduling",
					Message: "Pod cannot be scheduled",
				}
				Expect(k8sClient.Create(ctx, testEvent)).To(Succeed())
				defer k8sClient.Delete(ctx, testEvent)

				// Trigger reconciliation (should fail to resolve Secret)
				reconciler.reconcileEvent(ctx, testEvent)

				// Verify NotificationsHealthy condition shows error
				Eventually(func() bool {
					updated := &dotaiv1alpha1.RemediationPolicy{}
					k8sClient.Get(ctx, types.NamespacedName{
						Name:      testPolicy.Name,
						Namespace: testPolicy.Namespace,
					}, updated)

					for _, condition := range updated.Status.Conditions {
						if condition.Type == "NotificationsHealthy" {
							return condition.Status == metav1.ConditionFalse &&
								strings.Contains(condition.Message, "not found")
						}
					}
					return false
				}, "5s", "100ms").Should(BeTrue())
			})

			It("should update NotificationsHealthy condition when Secret key is missing", func() {
				testNamespace := fmt.Sprintf("test-notif-%d", time.Now().UnixNano())

				// Create namespace
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNamespace,
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
				defer k8sClient.Delete(ctx, ns)

				// Create Secret without expected key
				webhookSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "incomplete-webhooks",
						Namespace: testNamespace,
					},
					Data: map[string][]byte{
						"wrong-key": []byte("https://hooks.slack.com/services/TEST/URL"),
					},
				}
				Expect(k8sClient.Create(ctx, webhookSecret)).To(Succeed())

				// Create policy using Secret reference with wrong key
				testPolicy := &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-policy-wrong-key",
						Namespace: testNamespace,
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
								Enabled: true,
								WebhookUrlSecretRef: &dotaiv1alpha1.SecretReference{
									Name: "incomplete-webhooks",
									Key:  "slack-url",
								},
								Channel:          "#test-channel",
								NotifyOnStart:    true,
								NotifyOnComplete: true,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, testPolicy)).To(Succeed())
				defer k8sClient.Delete(ctx, testPolicy)

				// Create test event
				testEvent := &corev1.Event{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-event-wrong-key",
						Namespace: testNamespace,
					},
					InvolvedObject: corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "test-pod",
						Namespace: testNamespace,
					},
					Type:    "Warning",
					Reason:  "FailedScheduling",
					Message: "Pod cannot be scheduled",
				}
				Expect(k8sClient.Create(ctx, testEvent)).To(Succeed())
				defer k8sClient.Delete(ctx, testEvent)

				// Trigger reconciliation
				reconciler.reconcileEvent(ctx, testEvent)

				// Verify NotificationsHealthy condition shows key missing error
				Eventually(func() bool {
					updated := &dotaiv1alpha1.RemediationPolicy{}
					k8sClient.Get(ctx, types.NamespacedName{
						Name:      testPolicy.Name,
						Namespace: testPolicy.Namespace,
					}, updated)

					for _, condition := range updated.Status.Conditions {
						if condition.Type == "NotificationsHealthy" {
							return condition.Status == metav1.ConditionFalse &&
								strings.Contains(condition.Message, "does not contain key")
						}
					}
					return false
				}, "5s", "100ms").Should(BeTrue())
			})
		})
	})

	Describe("Dual Notification Integration (Slack + Google Chat)", func() {
		var (
			slackServer        *httptest.Server
			googleChatServer   *httptest.Server
			slackRequests      []SlackMessage
			googleChatRequests []GoogleChatMessage
			slackMutex         sync.RWMutex
			googleChatMutex    sync.RWMutex
			testPolicy         *dotaiv1alpha1.RemediationPolicy
			testEvent          *corev1.Event
			mockMcpServer      *httptest.Server
		)

		BeforeEach(func() {
			// Reset requests
			slackMutex.Lock()
			slackRequests = []SlackMessage{}
			slackMutex.Unlock()

			googleChatMutex.Lock()
			googleChatRequests = []GoogleChatMessage{}
			googleChatMutex.Unlock()

			// Create mock MCP server
			mockMcpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := createSuccessfulMcpResponse("Issue resolved", 1500.0)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(response)
			}))

			// Create mock Slack server
			slackServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message SlackMessage
				json.NewDecoder(r.Body).Decode(&message)
				slackMutex.Lock()
				slackRequests = append(slackRequests, message)
				slackMutex.Unlock()
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			}))

			// Create mock Google Chat server
			googleChatServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message GoogleChatMessage
				json.NewDecoder(r.Body).Decode(&message)
				googleChatMutex.Lock()
				googleChatRequests = append(googleChatRequests, message)
				googleChatMutex.Unlock()
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("{}"))
			}))

			// Create test event
			testEvent = &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dual-test-event-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
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

			// Create test policy with both Slack and Google Chat enabled
			testPolicy = &dotaiv1alpha1.RemediationPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dual-test-policy-" + fmt.Sprintf("%d", GinkgoRandomSeed()),
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
							Channel:          "#test",
							NotifyOnStart:    true,
							NotifyOnComplete: true,
						},
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
			if slackServer != nil {
				slackServer.Close()
			}
			if googleChatServer != nil {
				googleChatServer.Close()
			}
			if mockMcpServer != nil {
				mockMcpServer.Close()
			}
		})

		It("should send notifications to both Slack and Google Chat when both enabled", func() {
			// Create the policy and event in the cluster
			err := k8sClient.Create(ctx, testPolicy)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Create(ctx, testEvent)
			Expect(err).NotTo(HaveOccurred())

			defer func() {
				k8sClient.Delete(ctx, testEvent)
				k8sClient.Delete(ctx, testPolicy)
			}()

			// Trigger event processing
			result, err := reconciler.reconcileEvent(ctx, testEvent)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			// Verify Slack notifications
			Eventually(func() int {
				slackMutex.RLock()
				defer slackMutex.RUnlock()
				return len(slackRequests)
			}, "5s").Should(Equal(2))

			// Verify Google Chat notifications
			Eventually(func() int {
				googleChatMutex.RLock()
				defer googleChatMutex.RUnlock()
				return len(googleChatRequests)
			}, "5s").Should(Equal(2))

			// Verify content
			slackMutex.RLock()
			googleChatMutex.RLock()
			defer slackMutex.RUnlock()
			defer googleChatMutex.RUnlock()

			// Both should have start and complete
			Expect(slackRequests[0].Attachments[0].Blocks[0].Text.Text).To(ContainSubstring("Started"))
			Expect(slackRequests[1].Attachments[0].Blocks[0].Text.Text).To(ContainSubstring("Completed"))
			Expect(googleChatRequests[0].CardsV2[0].Card.Header.Title).To(ContainSubstring("Started"))
			Expect(googleChatRequests[1].CardsV2[0].Card.Header.Title).To(ContainSubstring("Completed"))
		})
	})
})
