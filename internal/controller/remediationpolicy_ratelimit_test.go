package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	dotaiv1alpha1 "github.com/vfarcic/dot-ai-controller/api/v1alpha1"
)

var _ = Describe("RemediationPolicy Rate Limiting", func() {
	Describe("Owner Reference Resolution for Rate Limiting", func() {
		var (
			reconciler *RemediationPolicyReconciler
			ctx        context.Context
			testNs     string
		)

		BeforeEach(func() {
			ctx = context.Background()
			testNs = fmt.Sprintf("owner-ref-test-%d", time.Now().UnixNano())
			reconciler = &RemediationPolicyReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Recorder:   record.NewFakeRecorder(100),
				HttpClient: &http.Client{Timeout: 30 * time.Second},
			}

			// Create test namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNs},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		})

		AfterEach(func() {
			// Cleanup namespace
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testNs},
			}
			_ = k8sClient.Delete(ctx, ns)
		})

		Describe("parseCronJobNameFromPodName", func() {
			Context("with valid CronJob pod name patterns", func() {
				It("should parse simple CronJob name", func() {
					name, ok := parseCronJobNameFromPodName("my-backup-29409620-abc12")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("my-backup"))
				})

				It("should parse CronJob name with hyphens", func() {
					name, ok := parseCronJobNameFromPodName("my-backup-job-29409620-abc12")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("my-backup-job"))
				})

				It("should parse CronJob name with multiple hyphens", func() {
					name, ok := parseCronJobNameFromPodName("database-cleanup-task-29409621-xyz99")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("database-cleanup-task"))
				})

				It("should parse CronJob name with long timestamp", func() {
					name, ok := parseCronJobNameFromPodName("simple-task-1234567890123-a1b2c")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("simple-task"))
				})

				It("should handle single character suffix", func() {
					name, ok := parseCronJobNameFromPodName("my-job-12345678-x")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("my-job"))
				})
			})

			Context("with invalid pod name patterns", func() {
				It("should return false for too few segments", func() {
					name, ok := parseCronJobNameFromPodName("simple-abc12")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for non-numeric timestamp", func() {
					name, ok := parseCronJobNameFromPodName("my-job-notanumber-abc12")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for empty timestamp segment", func() {
					name, ok := parseCronJobNameFromPodName("my-job--abc12")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for empty suffix segment", func() {
					name, ok := parseCronJobNameFromPodName("my-job-12345678-")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for single segment name", func() {
					name, ok := parseCronJobNameFromPodName("singlepod")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for two segment name", func() {
					name, ok := parseCronJobNameFromPodName("my-pod")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false for empty string", func() {
					name, ok := parseCronJobNameFromPodName("")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})

				It("should return false when timestamp has letters", func() {
					name, ok := parseCronJobNameFromPodName("my-job-123abc-xyz")
					Expect(ok).To(BeFalse())
					Expect(name).To(Equal(""))
				})
			})

			Context("with edge cases", func() {
				It("should handle numeric CronJob name", func() {
					name, ok := parseCronJobNameFromPodName("123-29409620-abc12")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("123"))
				})

				It("should handle CronJob name starting with number", func() {
					name, ok := parseCronJobNameFromPodName("1st-backup-29409620-abc12")
					Expect(ok).To(BeTrue())
					Expect(name).To(Equal("1st-backup"))
				})
			})
		})

		Describe("resolveOwnerForRateLimiting", func() {
			Context("when the involved object is not a Pod", func() {
				It("should return empty kind and original name for Deployment", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Deployment",
						Name:      "my-deployment",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("my-deployment"))
				})

				It("should return empty kind and original name for Service", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Service",
						Name:      "my-service",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("my-service"))
				})
			})

			Context("when the Pod does not exist", func() {
				It("should return empty kind and original pod name for non-CronJob pattern", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "non-existent-pod",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("non-existent-pod"))
				})

				It("should parse CronJob name from deleted pod with CronJob naming pattern", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "my-backup-job-29409620-abc12",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal("cronjob"))
					Expect(name).To(Equal("my-backup-job"))
				})

				It("should parse CronJob name with hyphens from deleted pod", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "database-cleanup-task-29409621-xyz99",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal("cronjob"))
					Expect(name).To(Equal("database-cleanup-task"))
				})

				It("should fall back to original name when pattern has non-numeric timestamp", func() {
					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "my-job-notanumber-abc12",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("my-job-notanumber-abc12"))
				})

				It("should fall back to original name for standalone Job pod pattern", func() {
					// Standalone jobs (not from CronJob) have pattern: {job-name}-{suffix}
					// This has only 2 segments which doesn't match CronJob pattern
					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "my-job-xyz12",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("my-job-xyz12"))
				})
			})

			Context("when the Pod has no Job owner", func() {
				It("should return empty kind and original pod name", func() {
					// Create a pod without Job owner (e.g., owned by ReplicaSet)
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "regular-pod",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "apps/v1",
									Kind:       "ReplicaSet",
									Name:       "my-replicaset",
									UID:        "rs-uid-123",
								},
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "regular-pod",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal(""))
					Expect(name).To(Equal("regular-pod"))
				})
			})

			Context("when the Pod is owned by a Job (no CronJob)", func() {
				It("should return 'job' kind and job name", func() {
					// Create a Job first
					job := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "my-job",
							Namespace: testNs,
							UID:       "job-uid-456",
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, job)).To(Succeed())

					// Create a pod owned by the Job
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "my-job-abc123",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "Job",
									Name:       "my-job",
									UID:        "job-uid-456",
								},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "my-job-abc123",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal("job"))
					Expect(name).To(Equal("my-job"))
				})
			})

			Context("when the Pod is owned by a Job that is owned by a CronJob", func() {
				It("should return 'cronjob' kind and cronjob name", func() {
					// Create a CronJob first
					cronJob := &batchv1.CronJob{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "my-cronjob",
							Namespace: testNs,
							UID:       "cronjob-uid-789",
						},
						Spec: batchv1.CronJobSpec{
							Schedule: "*/5 * * * *",
							JobTemplate: batchv1.JobTemplateSpec{
								Spec: batchv1.JobSpec{
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											RestartPolicy: corev1.RestartPolicyNever,
											Containers: []corev1.Container{
												{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
											},
										},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, cronJob)).To(Succeed())

					// Create a Job owned by the CronJob
					job := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "my-cronjob-28373940",
							Namespace: testNs,
							UID:       "job-uid-cronjob-001",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "CronJob",
									Name:       "my-cronjob",
									UID:        "cronjob-uid-789",
								},
							},
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, job)).To(Succeed())

					// Create a pod owned by the Job
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "my-cronjob-28373940-xyz",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "Job",
									Name:       "my-cronjob-28373940",
									UID:        "job-uid-cronjob-001",
								},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "my-cronjob-28373940-xyz",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					Expect(kind).To(Equal("cronjob"))
					Expect(name).To(Equal("my-cronjob"))
				})
			})

			Context("when the Pod references a Job that no longer exists", func() {
				It("should return 'job' kind and the referenced job name as fallback", func() {
					// Create a pod referencing a non-existent Job
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "orphaned-job-pod",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "Job",
									Name:       "deleted-job",
									UID:        "deleted-job-uid",
								},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					involvedObject := corev1.ObjectReference{
						Kind:      "Pod",
						Name:      "orphaned-job-pod",
						Namespace: testNs,
					}

					kind, name := reconciler.resolveOwnerForRateLimiting(ctx, involvedObject)

					// Should fall back to job name even if job doesn't exist
					Expect(kind).To(Equal("job"))
					Expect(name).To(Equal("deleted-job"))
				})
			})
		})

		Describe("getRateLimitKey", func() {
			var policy *dotaiv1alpha1.RemediationPolicy

			BeforeEach(func() {
				policy = &dotaiv1alpha1.RemediationPolicy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rate-limit-policy",
						Namespace: testNs,
					},
					Spec: dotaiv1alpha1.RemediationPolicySpec{
						EventSelectors: []dotaiv1alpha1.EventSelector{
							{Type: "Warning", Reason: "BackOff"},
						},
						McpEndpoint: "http://test-mcp:3456/api/v1/tools/remediate",
						McpAuthSecretRef: dotaiv1alpha1.SecretReference{
							Name: "mcp-auth-secret",
							Key:  "api-key",
						},
						RateLimiting: dotaiv1alpha1.RateLimiting{
							EventsPerMinute: 5,
						},
					},
				}
			})

			Context("when involved object is a regular Pod (no Job owner)", func() {
				It("should use pod name without kind prefix", func() {
					// Create a regular pod
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "standalone-pod",
							Namespace: testNs,
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					event := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-event",
							Namespace: testNs,
						},
						InvolvedObject: corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "standalone-pod",
							Namespace: testNs,
						},
						Reason: "BackOff",
					}

					key := reconciler.getRateLimitKey(ctx, policy, event)

					expectedKey := fmt.Sprintf("%s/rate-limit-policy/%s/standalone-pod", testNs, testNs)
					Expect(key).To(Equal(expectedKey))
				})
			})

			Context("when involved object is a CronJob Pod", func() {
				It("should use cronjob: prefix and cronjob name", func() {
					// Create CronJob
					cronJob := &batchv1.CronJob{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-cronjob",
							Namespace: testNs,
							UID:       "test-cronjob-uid",
						},
						Spec: batchv1.CronJobSpec{
							Schedule: "*/5 * * * *",
							JobTemplate: batchv1.JobTemplateSpec{
								Spec: batchv1.JobSpec{
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											RestartPolicy: corev1.RestartPolicyNever,
											Containers: []corev1.Container{
												{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
											},
										},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, cronJob)).To(Succeed())

					// Create Job owned by CronJob
					job := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-cronjob-111111",
							Namespace: testNs,
							UID:       "test-job-uid-111",
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "CronJob",
									Name:       "test-cronjob",
									UID:        "test-cronjob-uid",
								},
							},
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, job)).To(Succeed())

					// Create Pod owned by Job
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-cronjob-111111-abc",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{
									APIVersion: "batch/v1",
									Kind:       "Job",
									Name:       "test-cronjob-111111",
									UID:        "test-job-uid-111",
								},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					}
					Expect(k8sClient.Create(ctx, pod)).To(Succeed())

					event := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "cronjob-event",
							Namespace: testNs,
						},
						InvolvedObject: corev1.ObjectReference{
							Kind:      "Pod",
							Name:      "test-cronjob-111111-abc",
							Namespace: testNs,
						},
						Reason: "BackOff",
					}

					key := reconciler.getRateLimitKey(ctx, policy, event)

					expectedKey := fmt.Sprintf("%s/rate-limit-policy/%s/cronjob:test-cronjob", testNs, testNs)
					Expect(key).To(Equal(expectedKey))
				})
			})

			Context("when multiple CronJob pods generate events", func() {
				It("should produce the same rate limit key for all pods from the same CronJob", func() {
					// Create CronJob
					cronJob := &batchv1.CronJob{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "shared-cronjob",
							Namespace: testNs,
							UID:       "shared-cronjob-uid",
						},
						Spec: batchv1.CronJobSpec{
							Schedule: "*/5 * * * *",
							JobTemplate: batchv1.JobTemplateSpec{
								Spec: batchv1.JobSpec{
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											RestartPolicy: corev1.RestartPolicyNever,
											Containers: []corev1.Container{
												{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}},
											},
										},
									},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, cronJob)).To(Succeed())

					// Create two different Jobs from the same CronJob (simulating two runs)
					job1 := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "shared-cronjob-run1",
							Namespace: testNs,
							UID:       "job-run1-uid",
							OwnerReferences: []metav1.OwnerReference{
								{APIVersion: "batch/v1", Kind: "CronJob", Name: "shared-cronjob", UID: "shared-cronjob-uid"},
							},
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers:    []corev1.Container{{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}}},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, job1)).To(Succeed())

					job2 := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "shared-cronjob-run2",
							Namespace: testNs,
							UID:       "job-run2-uid",
							OwnerReferences: []metav1.OwnerReference{
								{APIVersion: "batch/v1", Kind: "CronJob", Name: "shared-cronjob", UID: "shared-cronjob-uid"},
							},
						},
						Spec: batchv1.JobSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers:    []corev1.Container{{Name: "test", Image: "busybox", Command: []string{"echo", "hello"}}},
								},
							},
						},
					}
					Expect(k8sClient.Create(ctx, job2)).To(Succeed())

					// Create pods for each job
					pod1 := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "shared-cronjob-run1-pod",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{APIVersion: "batch/v1", Kind: "Job", Name: "shared-cronjob-run1", UID: "job-run1-uid"},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers:    []corev1.Container{{Name: "test", Image: "busybox"}},
						},
					}
					Expect(k8sClient.Create(ctx, pod1)).To(Succeed())

					pod2 := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "shared-cronjob-run2-pod",
							Namespace: testNs,
							OwnerReferences: []metav1.OwnerReference{
								{APIVersion: "batch/v1", Kind: "Job", Name: "shared-cronjob-run2", UID: "job-run2-uid"},
							},
						},
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers:    []corev1.Container{{Name: "test", Image: "busybox"}},
						},
					}
					Expect(k8sClient.Create(ctx, pod2)).To(Succeed())

					// Create events for both pods
					event1 := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{Name: "event1", Namespace: testNs},
						InvolvedObject: corev1.ObjectReference{
							Kind: "Pod", Name: "shared-cronjob-run1-pod", Namespace: testNs,
						},
						Reason: "BackOff",
					}

					event2 := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{Name: "event2", Namespace: testNs},
						InvolvedObject: corev1.ObjectReference{
							Kind: "Pod", Name: "shared-cronjob-run2-pod", Namespace: testNs,
						},
						Reason: "BackOff",
					}

					key1 := reconciler.getRateLimitKey(ctx, policy, event1)
					key2 := reconciler.getRateLimitKey(ctx, policy, event2)

					// Both keys should be identical - this is the core requirement!
					Expect(key1).To(Equal(key2))

					expectedKey := fmt.Sprintf("%s/rate-limit-policy/%s/cronjob:shared-cronjob", testNs, testNs)
					Expect(key1).To(Equal(expectedKey))
				})
			})

			Context("when involved object is not a Pod", func() {
				It("should use object name without kind prefix", func() {
					event := &corev1.Event{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "deployment-event",
							Namespace: testNs,
						},
						InvolvedObject: corev1.ObjectReference{
							Kind:      "Deployment",
							Name:      "my-deployment",
							Namespace: testNs,
						},
						Reason: "ScalingReplicaSet",
					}

					key := reconciler.getRateLimitKey(ctx, policy, event)

					expectedKey := fmt.Sprintf("%s/rate-limit-policy/%s/my-deployment", testNs, testNs)
					Expect(key).To(Equal(expectedKey))
				})
			})
		})
	})
})
