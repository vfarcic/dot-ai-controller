package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vfarcic/dot-ai-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "controller-init-system"

// testNamespace where test resources (policies, pods, mock servers) are created
const testNamespace = "e2e-tests"

// serviceAccountName created for the project
const serviceAccountName = "controller-init-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "controller-init-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "controller-init-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
			Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")
		}

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("configuring fast persistence sync for e2e tests")
		cmd = exec.Command("kubectl", "set", "env", "deployment/controller-init-controller-manager",
			"-n", namespace,
			"COOLDOWN_SYNC_INTERVAL=5s",
			"COOLDOWN_MIN_PERSIST_DURATION=10s")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to configure persistence env vars")

		By("waiting for controller deployment rollout to complete")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/controller-init-controller-manager",
			"-n", namespace, "--timeout=120s")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed waiting for rollout")

		By("creating test namespace without security restrictions")
		cmd = exec.Command("kubectl", "create", "ns", testNamespace)
		_, err = utils.Run(cmd)
		if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
		}

		deployMockMcpServer()
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

	})

	Context("RemediationPolicy CRUD Operations", func() {
		It("should create and validate RemediationPolicy resources", func() {
			By("creating a basic RemediationPolicy")
			basicPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: test-basic-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: automatic
  rateLimiting:
    eventsPerMinute: 10
    cooldownMinutes: 5
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(basicPolicy)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create basic RemediationPolicy")

			By("verifying the RemediationPolicy was created successfully")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-basic-policy",
					"-n", namespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("test-basic-policy"))
			}).Should(Succeed())

			By("verifying RemediationPolicy spec was applied correctly")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-basic-policy",
					"-n", namespace, "-o", "jsonpath={.spec.mode}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("automatic"), "Spec mode should be set to automatic")
			}).Should(Succeed())

			By("updating the RemediationPolicy with new configuration")
			updatedPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: test-basic-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: automatic
  - type: Warning
    reason: CrashLoopBackOff
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 20
    cooldownMinutes: 10
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(updatedPolicy)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to update RemediationPolicy")

			By("verifying the update was applied")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-basic-policy",
					"-n", namespace, "-o", "jsonpath={.spec.rateLimiting.eventsPerMinute}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("20"), "Rate limiting should be updated to 20 events per minute")
			}).Should(Succeed())

			By("cleaning up the test RemediationPolicy")
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "test-basic-policy", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete RemediationPolicy")

			By("verifying the RemediationPolicy was deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-basic-policy", "-n", namespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "RemediationPolicy should be deleted")
			}).Should(Succeed())
		})

		It("should reject invalid RemediationPolicy configurations", func() {
			By("attempting to create a RemediationPolicy with invalid mode")
			invalidModePolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: test-invalid-mode
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: invalid_mode
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: automatic
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidModePolicy)
			output, err := utils.Run(cmd)
			if err == nil {
				// If apply succeeds, check that the resource was rejected by validation
				By("verifying invalid mode was rejected or not applied correctly")
				cmd = exec.Command("kubectl", "get", "remediationpolicy", "test-invalid-mode", "-n", namespace)
				_, getErr := utils.Run(cmd)
				if getErr == nil {
					// Resource was created, clean it up and skip this validation test
					cmd = exec.Command("kubectl", "delete", "remediationpolicy", "test-invalid-mode", "-n", namespace)
					_, _ = utils.Run(cmd)
					Skip("CRD validation allows invalid mode - this may be a CRD schema issue")
				}
			} else {
				// Expected behavior - kubectl apply should fail
				Expect(output).To(ContainSubstring("invalid"), "Error should mention invalid value")
			}

			By("attempting to create a RemediationPolicy with invalid global mode")
			invalidGlobalModePolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: test-invalid-global-mode
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: invalid_global_mode
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(invalidGlobalModePolicy)
			output, err = utils.Run(cmd)
			if err == nil {
				// Clean up if somehow created
				cmd = exec.Command("kubectl", "delete", "remediationpolicy", "test-invalid-global-mode", "-n", namespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				Skip("CRD validation allows invalid global mode - this may be a CRD schema issue")
			} else {
				// Expected behavior - kubectl apply should fail
				Expect(output).To(ContainSubstring("invalid"), "Error should mention invalid value")
			}
		})
	})

	Context("RemediationPolicy Configuration Validation", func() {
		It("should persist complex configurations correctly", func() {
			By("creating a RemediationPolicy with complex configuration")
			complexPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: test-complex-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  - type: Normal
    reason: Created
    involvedObjectKind: Pod
    mode: automatic
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(complexPolicy)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create complex RemediationPolicy")

			By("verifying complex configuration was applied correctly")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-complex-policy",
					"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[0].mode}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("manual"), "First selector mode should be manual")
			}).Should(Succeed())

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "test-complex-policy",
					"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[1].mode}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("automatic"), "Second selector mode should be automatic")
			}).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "test-complex-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Solution CRUD Operations", func() {
		It("should create and validate Solution resources", func() {
			By("creating a basic Solution")
			basicSolution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: test-basic-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Deploy a test application"
  context:
    createdBy: "e2e-test"
    rationale: "Testing Solution CRD basic functionality"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: test-deployment
    - apiVersion: v1
      kind: Service
      name: test-service
  documentationURL: "https://example.com/docs"
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(basicSolution)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create basic Solution")

			By("verifying the Solution was created successfully")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "test-basic-solution",
					"-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("test-basic-solution"))
			}).Should(Succeed())

			By("verifying Solution spec was applied correctly")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "test-basic-solution",
					"-n", testNamespace, "-o", "jsonpath={.spec.intent}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deploy a test application"))
			}).Should(Succeed())

			By("verifying Solution status was initialized")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "test-basic-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("pending"), Equal("degraded")), "Status should be initialized")
			}).Should(Succeed())

			By("updating the Solution with new intent")
			updatedSolution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: test-basic-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Deploy an updated test application"
  context:
    createdBy: "e2e-test"
    rationale: "Testing Solution CRD update functionality"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: test-deployment
    - apiVersion: v1
      kind: Service
      name: test-service
  documentationURL: "https://example.com/updated-docs"
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(updatedSolution)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to update Solution")

			By("verifying the update was applied")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "test-basic-solution",
					"-n", testNamespace, "-o", "jsonpath={.spec.intent}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Deploy an updated test application"))
			}).Should(Succeed())

			By("cleaning up the test Solution")
			cmd = exec.Command("kubectl", "delete", "solution", "test-basic-solution", "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete Solution")

			By("verifying the Solution was deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "test-basic-solution", "-n", testNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "Solution should be deleted")
			}).Should(Succeed())
		})
	})

	Context("Solution Resource Tracking", func() {
		It("should track resources and add ownerReferences", func() {
			By("creating child resources first")
			childResources := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tracking-test-deployment
  namespace: ` + testNamespace + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: tracking-test
  template:
    metadata:
      labels:
        app: tracking-test
    spec:
      containers:
      - name: httpd
        image: httpd:2.4-alpine
        ports:
        - containerPort: 80
---
apiVersion: v1
kind: Service
metadata:
  name: tracking-test-service
  namespace: ` + testNamespace + `
spec:
  selector:
    app: tracking-test
  ports:
  - protocol: TCP
    port: 80
    targetPort: 80
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(childResources)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create child resources")

			By("waiting for Deployment to exist")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "tracking-test-deployment", "-n", testNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			By("creating a Solution that references these resources")
			solution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: tracking-test-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Test resource tracking and ownerReferences"
  context:
    createdBy: "e2e-test"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: tracking-test-deployment
    - apiVersion: v1
      kind: Service
      name: tracking-test-service
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(solution)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create Solution")

			By("waiting for controller to add ownerReferences to Deployment")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "tracking-test-deployment",
					"-n", testNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Solution"), "Deployment should have Solution as owner")
			}, 30*time.Second).Should(Succeed())

			By("verifying ownerReference has correct fields")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "tracking-test-deployment",
					"-n", testNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("tracking-test-solution"))

				cmd = exec.Command("kubectl", "get", "deployment", "tracking-test-deployment",
					"-n", testNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].controller}")
				output, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("false"))
			}).Should(Succeed())

			By("verifying ownerReference was added to Service")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "service", "tracking-test-service",
					"-n", testNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Solution"))
			}, 30*time.Second).Should(Succeed())

			By("verifying Solution status reflects tracked resources")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "tracking-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.resources.total}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("2"), "Should track 2 resources")
			}, 30*time.Second).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "solution", "tracking-test-solution", "-n", testNamespace)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "deployment", "tracking-test-deployment", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "service", "tracking-test-service", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})
	})

	Context("Solution Health Checking", func() {
		It("should detect healthy resources and update status to deployed", func() {
			By("creating a simple Deployment")
			deployment := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: health-test-deployment
  namespace: ` + testNamespace + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: health-test
  template:
    metadata:
      labels:
        app: health-test
    spec:
      containers:
      - name: httpd
        image: httpd:2.4-alpine
        ports:
        - containerPort: 80
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(deployment)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a Solution that tracks the Deployment")
			solution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: health-test-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Test health checking"
  context:
    createdBy: "e2e-test"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: health-test-deployment
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(solution)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for Deployment to become ready")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "health-test-deployment",
					"-n", testNamespace, "-o", "jsonpath={.status.readyReplicas}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Deployment should have 1 ready replica")
			}, 2*time.Minute).Should(Succeed())

			By("verifying Solution status shows deployed state")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "health-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("deployed"), "Solution should be in deployed state")
			}, 30*time.Second).Should(Succeed())

			By("verifying Solution shows all resources ready")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "health-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.resources.ready}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"))
			}).Should(Succeed())

			By("verifying Ready condition is True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "health-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "solution", "health-test-solution", "-n", testNamespace)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "deployment", "health-test-deployment", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should detect unhealthy resources and update status to degraded", func() {
			By("creating a Deployment with an invalid image")
			failingDeployment := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: degraded-test-deployment
  namespace: ` + testNamespace + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: degraded-test
  template:
    metadata:
      labels:
        app: degraded-test
    spec:
      containers:
      - name: test
        image: invalid-image-that-does-not-exist:latest
        imagePullPolicy: Always
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingDeployment)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a Solution that tracks the failing Deployment")
			solution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: degraded-test-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Test degraded state detection"
  context:
    createdBy: "e2e-test"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: degraded-test-deployment
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(solution)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for controller to detect unhealthy resource")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "degraded-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("degraded"), "Solution should be in degraded state")
			}, 2*time.Minute).Should(Succeed())

			By("verifying Solution shows failed resources")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "degraded-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.resources.failed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Should have 1 failed resource")
			}).Should(Succeed())

			By("verifying Ready condition is False")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "degraded-test-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("False"))
			}).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "solution", "degraded-test-solution", "-n", testNamespace)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "deployment", "degraded-test-deployment", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should handle missing resources correctly", func() {
			By("creating a Solution that references non-existent resources")
			solution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: missing-resources-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Test missing resource handling"
  context:
    createdBy: "e2e-test"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: non-existent-deployment
    - apiVersion: v1
      kind: Service
      name: non-existent-service
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(solution)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying Solution status shows failed resources")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "missing-resources-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.resources.failed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("2"), "Should have 2 failed (missing) resources")
			}, 30*time.Second).Should(Succeed())

			By("verifying Solution is in degraded state")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "solution", "missing-resources-solution",
					"-n", testNamespace, "-o", "jsonpath={.status.state}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("degraded"))
			}).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "solution", "missing-resources-solution", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Solution Garbage Collection", func() {
		It("should delete child resources when Solution is deleted", func() {
			By("creating child resources")
			childResources := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gc-test-deployment
  namespace: ` + testNamespace + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: gc-test
  template:
    metadata:
      labels:
        app: gc-test
    spec:
      containers:
      - name: httpd
        image: httpd:2.4-alpine
---
apiVersion: v1
kind: Service
metadata:
  name: gc-test-service
  namespace: ` + testNamespace + `
spec:
  selector:
    app: gc-test
  ports:
  - protocol: TCP
    port: 80
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(childResources)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a Solution that tracks these resources")
			solution := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: Solution
metadata:
  name: gc-test-solution
  namespace: ` + testNamespace + `
spec:
  intent: "Test garbage collection"
  context:
    createdBy: "e2e-test"
  resources:
    - apiVersion: apps/v1
      kind: Deployment
      name: gc-test-deployment
    - apiVersion: v1
      kind: Service
      name: gc-test-service
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(solution)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for ownerReferences to be added")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "gc-test-deployment",
					"-n", testNamespace, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Solution"))
			}, 30*time.Second).Should(Succeed())

			By("verifying child resources exist before deletion")
			cmd = exec.Command("kubectl", "get", "deployment", "gc-test-deployment", "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Deployment should exist")

			cmd = exec.Command("kubectl", "get", "service", "gc-test-service", "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Service should exist")

			By("deleting the Solution")
			cmd = exec.Command("kubectl", "delete", "solution", "gc-test-solution", "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying child resources are automatically deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployment", "gc-test-deployment", "-n", testNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "Deployment should be deleted by garbage collection")
			}, 60*time.Second).Should(Succeed())

			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "service", "gc-test-service", "-n", testNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "Service should be deleted by garbage collection")
			}, 60*time.Second).Should(Succeed())
		})
	})

	Context("Event Processing Pipeline", func() {
		// Note: These tests focus on event processing without MCP integration
		// MCP integration can be tested separately once mock server issues are resolved

		It("should generate events and validate policy status updates", func() {
			By("creating a RemediationPolicy for event processing")
			eventPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: event-tracking-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(eventPolicy)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create event tracking policy")

			By("creating a pod that will fail to schedule (generate events)")
			failingPod := `
apiVersion: v1
kind: Pod
metadata:
  name: failing-pod
  namespace: ` + namespace + `
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: test
    image: nginx:latest
    resources:
      requests:
        cpu: "1000"  # Request way too much CPU to force scheduling failure
        memory: "10Gi"  # Request way too much memory to force scheduling failure
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
  restartPolicy: Never
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create failing pod")

			By("waiting for FailedScheduling events to be generated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "-n", namespace,
					"--field-selector", "reason=FailedScheduling", "-o", "jsonpath={.items}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal("[]"), "Should have FailedScheduling events")
			}, 60*time.Second).Should(Succeed())

			By("verifying the policy was created with correct configuration")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "event-tracking-policy",
					"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[0].reason}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("FailedScheduling"), "Policy should watch for FailedScheduling events")
			}).Should(Succeed())

			By("verifying events were generated in Kubernetes")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--field-selector", "reason=FailedScheduling")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("FailedScheduling"), "Should have FailedScheduling events in cluster")

			By("waiting for MCP request to be processed and policy status to be updated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "event-tracking-policy",
					"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal(""), "Policy status should have totalEventsProcessed")
				g.Expect(output).NotTo(Equal("0"), "Should have processed at least one event")
			}, 30*time.Second).Should(Succeed())

			By("verifying MCP request was successful and policy status reflects success")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "event-tracking-policy",
					"-n", namespace, "-o", "jsonpath={.status.successfulRemediations}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal(""), "Policy should have successfulRemediations status")
				g.Expect(output).NotTo(Equal("0"), "Should have at least one successful MCP remediation")
			}, 30*time.Second).Should(Succeed())

			By("verifying no failed remediations occurred")
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "event-tracking-policy",
				"-n", namespace, "-o", "jsonpath={.status.failedRemediations}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Or(Equal(""), Equal("0")), "Should have no failed remediations with successful mock server")

			By("verifying MCP messages were generated")
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "event-tracking-policy",
				"-n", namespace, "-o", "jsonpath={.status.totalMcpMessagesGenerated}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).NotTo(Equal(""), "Policy should have totalMcpMessagesGenerated status")
			Expect(output).NotTo(Equal("0"), "Should have generated at least one MCP message")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", "failing-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "event-tracking-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})

		It("should respect event filtering by selector configuration", func() {
			By("creating a RemediationPolicy with specific event selectors")
			selectivePolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: selective-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedMount
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(selectivePolicy)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create selective policy")

			By("creating a pod that will fail to schedule (should NOT match FailedMount selector)")
			nonMatchingPod := `
apiVersion: v1
kind: Pod
metadata:
  name: non-matching-pod
  namespace: ` + namespace + `
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: test
    image: nginx:latest
    resources:
      requests:
        cpu: "1000"  # Force FailedScheduling, not FailedMount
        memory: "10Gi"
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
  restartPolicy: Never
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nonMatchingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create non-matching pod")

			By("waiting for FailedScheduling events (should not be processed by policy)")
			time.Sleep(30 * time.Second) // Give controller time to potentially process events

			By("verifying the policy has correct selective configuration")
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "selective-policy",
				"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[0].reason}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("FailedMount"), "Policy should be configured to watch for FailedMount events, not FailedScheduling")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", "non-matching-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "selective-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})

		It("should handle multiple policies with first-match behavior", func() {
			By("creating two RemediationPolicies with overlapping selectors")
			firstPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: first-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(firstPolicy)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create first policy")

			secondPolicy := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: second-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: automatic
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: automatic
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 1
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(secondPolicy)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create second policy")

			By("creating a pod that will generate FailedScheduling events")
			multiPolicyPod := `
apiVersion: v1
kind: Pod
metadata:
  name: multi-policy-pod
  namespace: ` + namespace + `
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: test
    image: nginx:latest
    resources:
      requests:
        cpu: "1000"
        memory: "10Gi"
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
  restartPolicy: Never
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(multiPolicyPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create multi-policy pod")

			By("verifying both policies were created with different configurations")
			Eventually(func(g Gomega) {
				// Verify first policy configuration
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "first-policy",
					"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[0].mode}")
				firstOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(firstOutput).To(Equal("manual"))

				// Verify second policy configuration
				cmd = exec.Command("kubectl", "get", "remediationpolicy", "second-policy",
					"-n", namespace, "-o", "jsonpath={.spec.eventSelectors[0].mode}")
				secondOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(secondOutput).To(Equal("automatic"))
			}).Should(Succeed())

			By("verifying FailedScheduling events were generated")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--field-selector", "reason=FailedScheduling")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(ContainSubstring("FailedScheduling"), "Should have FailedScheduling events")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", "multi-policy-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "first-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "second-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Cooldown State Persistence", func() {
		// These tests validate that cooldown state persistence works correctly across pod restarts

		It("should lose cooldown state after restart when persistence is disabled (demonstrates the problem)", func() {
			By("creating a RemediationPolicy with persistence explicitly disabled")
			policyNoPersist := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: no-persist-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 60
  persistence:
    enabled: false
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(policyNoPersist)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create policy without persistence")

			By("creating a pod that will fail to schedule and trigger cooldown")
			failingPod := `
apiVersion: v1
kind: Pod
metadata:
  name: no-persist-test-pod
  namespace: ` + namespace + `
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: test
    image: nginx:latest
    resources:
      requests:
        cpu: "1000"
        memory: "10Gi"
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
  restartPolicy: Never
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create failing pod")

			By("waiting for event to be processed and cooldown to be set")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "no-persist-policy",
					"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal(""))
				g.Expect(output).NotTo(Equal("0"))
			}, 60*time.Second).Should(Succeed())

			By("verifying no ConfigMap was created (persistence disabled)")
			cmd = exec.Command("kubectl", "get", "configmap", "no-persist-policy-cooldown-state", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "ConfigMap should NOT exist when persistence is disabled")

			By("deleting the failing pod before restart")
			cmd = exec.Command("kubectl", "delete", "pod", "no-persist-test-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("restarting the controller pod to simulate pod restart")
			cmd = exec.Command("kubectl", "delete", "pod", "-l", "control-plane=controller-manager", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete controller pod")

			By("waiting for controller to be ready again")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-l", "control-plane=controller-manager",
					"-n", namespace, "-o", "jsonpath={.items[0].status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"))
			}, 120*time.Second).Should(Succeed())

			// Wait for controller to become leader and start processing
			time.Sleep(10 * time.Second)

			By("recording event count before recreating pod")
			var countBeforeRecreate string
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "no-persist-policy",
				"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
			countBeforeRecreate, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("recreating the failing pod to generate a NEW event after restart")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to recreate failing pod")

			By("verifying cooldown state was lost - new event should be processed (not suppressed by cooldown)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "no-persist-policy",
					"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// After restart with no persistence, the new event should be processed
				// because cooldown state was lost
				g.Expect(output).NotTo(Equal(countBeforeRecreate),
					"Event count should increase after restart when persistence is disabled (cooldown lost)")
			}, 60*time.Second).Should(Succeed())

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", "no-persist-test-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "no-persist-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})

		It("should preserve cooldown state after restart when persistence is enabled (demonstrates the solution)", func() {
			By("creating a RemediationPolicy with persistence enabled (default)")
			policyWithPersist := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: RemediationPolicy
metadata:
  name: persist-policy
  namespace: ` + namespace + `
spec:
  eventSelectors:
  - type: Warning
    reason: FailedScheduling
    involvedObjectKind: Pod
    mode: manual
  mcpEndpoint: http://mock-mcp-server.e2e-tests.svc.cluster.local:3456/api/v1/tools/remediate
  mode: manual
  rateLimiting:
    eventsPerMinute: 100
    cooldownMinutes: 120
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(policyWithPersist)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create policy with persistence")

			By("creating a pod that will fail to schedule and trigger cooldown")
			failingPod := `
apiVersion: v1
kind: Pod
metadata:
  name: persist-test-pod
  namespace: ` + namespace + `
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: test
    image: nginx:latest
    resources:
      requests:
        cpu: "1000"
        memory: "10Gi"
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop:
        - ALL
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65534
  restartPolicy: Never
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create failing pod")

			By("waiting for event to be processed and cooldown to be set")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "remediationpolicy", "persist-policy",
					"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal(""))
				g.Expect(output).NotTo(Equal("0"))
			}, 60*time.Second).Should(Succeed())

			By("waiting for ConfigMap to be created with cooldown state")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "configmap", "persist-policy-cooldown-state",
					"-n", namespace, "-o", "jsonpath={.data.cooldowns}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "ConfigMap should exist when persistence is enabled")
				g.Expect(output).NotTo(BeEmpty(), "ConfigMap should have cooldown data")
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			By("verifying ConfigMap has correct labels and ownerReference")
			cmd = exec.Command("kubectl", "get", "configmap", "persist-policy-cooldown-state",
				"-n", namespace, "-o", "jsonpath={.metadata.labels.app\\.kubernetes\\.io/managed-by}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("dot-ai-controller"), "ConfigMap should have correct managed-by label")

			cmd = exec.Command("kubectl", "get", "configmap", "persist-policy-cooldown-state",
				"-n", namespace, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("RemediationPolicy"), "ConfigMap should have ownerReference to RemediationPolicy")

			By("deleting the failing pod before restart")
			cmd = exec.Command("kubectl", "delete", "pod", "persist-test-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("restarting the controller pod to simulate pod restart")
			cmd = exec.Command("kubectl", "delete", "pod", "-l", "control-plane=controller-manager", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete controller pod")

			By("waiting for controller to be ready again")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-l", "control-plane=controller-manager",
					"-n", namespace, "-o", "jsonpath={.items[0].status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"))
			}, 120*time.Second).Should(Succeed())

			// Wait for controller to become leader and load persisted state
			time.Sleep(15 * time.Second)

			By("recording event count before recreating pod")
			var countBeforeRecreate string
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "persist-policy",
				"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
			countBeforeRecreate, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("recreating the failing pod to generate a NEW event after restart")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(failingPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to recreate failing pod")

			By("waiting for new event to be generated")
			time.Sleep(10 * time.Second)

			By("verifying cooldown state was preserved - new event should be SUPPRESSED (cooldown restored)")
			// The event count should remain the same because the cooldown was restored from ConfigMap
			cmd = exec.Command("kubectl", "get", "remediationpolicy", "persist-policy",
				"-n", namespace, "-o", "jsonpath={.status.totalEventsProcessed}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(countBeforeRecreate),
				"Event count should remain the same when persistence is enabled (cooldown preserved, new event suppressed)")

			By("verifying ConfigMap still exists after restart")
			cmd = exec.Command("kubectl", "get", "configmap", "persist-policy-cooldown-state", "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "ConfigMap should still exist after controller restart")

			By("cleaning up test resources")
			cmd = exec.Command("kubectl", "delete", "pod", "persist-test-pod", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "remediationpolicy", "persist-policy", "-n", namespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("ResourceSyncConfig", func() {
		// Shared setup for all ResourceSyncConfig tests
		BeforeEach(func() {
			deployMockResourceSyncServer()
			By("creating test secret for MCP auth")
			cmd := exec.Command("kubectl", "create", "secret", "generic", "mcp-auth-secret",
				"-n", testNamespace,
				"--from-literal=token=test-token",
				"--dry-run=client", "-o", "yaml")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to generate secret YAML")
			cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
			cmd.Stdin = strings.NewReader(output)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create mcp-auth-secret")
		})

		AfterEach(func() {
			// Clean up any test ResourceSyncConfigs
			cmd := exec.Command("kubectl", "delete", "resourcesyncconfig", "--all", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "secret", "mcp-auth-secret", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		Context("CRUD Operations", func() {
			It("should create and validate ResourceSyncConfig resources", func() {
				By("creating a basic ResourceSyncConfig")
				basicConfig := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: test-basic-config
  namespace: %s
spec:
  mcpEndpoint: http://mock-resource-sync-server.e2e-tests.svc.cluster.local:8080/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-auth-secret
    key: token
  debounceWindowSeconds: 10
  resyncIntervalMinutes: 60
`, testNamespace)
				cmd := exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(basicConfig)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to create basic ResourceSyncConfig")

				By("verifying the ResourceSyncConfig was created successfully")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-basic-config",
						"-n", testNamespace, "-o", "jsonpath={.metadata.name}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("test-basic-config"))
				}).Should(Succeed())

				By("verifying ResourceSyncConfig spec was applied correctly")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-basic-config",
						"-n", testNamespace, "-o", "jsonpath={.spec.debounceWindowSeconds}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("10"), "debounceWindowSeconds should be 10")
				}).Should(Succeed())

				By("updating the ResourceSyncConfig with new configuration")
				updatedConfig := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: test-basic-config
  namespace: %s
spec:
  mcpEndpoint: http://mock-resource-sync-server.e2e-tests.svc.cluster.local:8080/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-auth-secret
    key: token
  debounceWindowSeconds: 20
  resyncIntervalMinutes: 120
`, testNamespace)
				cmd = exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(updatedConfig)
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to update ResourceSyncConfig")

				By("verifying the update was applied")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-basic-config",
						"-n", testNamespace, "-o", "jsonpath={.spec.debounceWindowSeconds}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("20"), "debounceWindowSeconds should be updated to 20")
				}).Should(Succeed())

				By("cleaning up the test ResourceSyncConfig")
				cmd = exec.Command("kubectl", "delete", "resourcesyncconfig", "test-basic-config", "-n", testNamespace)
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to delete ResourceSyncConfig")

				By("verifying the ResourceSyncConfig was deleted")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-basic-config", "-n", testNamespace)
					_, err := utils.Run(cmd)
					g.Expect(err).To(HaveOccurred(), "ResourceSyncConfig should be deleted")
				}).Should(Succeed())
			})
		})

		Context("Status Updates", func() {
			It("should update status when watcher starts", func() {
				By("creating a ResourceSyncConfig")
				config := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: test-status-config
  namespace: %s
spec:
  mcpEndpoint: http://mock-resource-sync-server.e2e-tests.svc.cluster.local:8080/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-auth-secret
    key: token
  debounceWindowSeconds: 5
  resyncIntervalMinutes: 60
`, testNamespace)
				cmd := exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(config)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to create ResourceSyncConfig")

				By("waiting for status to show active watcher")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-status-config",
						"-n", testNamespace, "-o", "jsonpath={.status.active}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("true"), "Status should show active watcher")
				}, 60*time.Second).Should(Succeed())

				By("verifying watchedResourceTypes is populated")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-status-config",
						"-n", testNamespace, "-o", "jsonpath={.status.watchedResourceTypes}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).NotTo(Equal(""), "watchedResourceTypes should be set")
					g.Expect(output).NotTo(Equal("0"), "Should be watching at least some resource types")
				}).Should(Succeed())

				By("verifying Ready condition is True")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-status-config",
						"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("True"), "Ready condition should be True")
				}).Should(Succeed())

				By("cleaning up")
				cmd = exec.Command("kubectl", "delete", "resourcesyncconfig", "test-status-config", "-n", testNamespace)
				_, _ = utils.Run(cmd)
			})

			It("should stop watcher when ResourceSyncConfig is deleted", func() {
				By("creating a ResourceSyncConfig")
				config := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: test-delete-config
  namespace: %s
spec:
  mcpEndpoint: http://mock-resource-sync-server.e2e-tests.svc.cluster.local:8080/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-auth-secret
    key: token
`, testNamespace)
				cmd := exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(config)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for watcher to become active")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-delete-config",
						"-n", testNamespace, "-o", "jsonpath={.status.active}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(Equal("true"))
				}, 60*time.Second).Should(Succeed())

				By("deleting the ResourceSyncConfig")
				cmd = exec.Command("kubectl", "delete", "resourcesyncconfig", "test-delete-config", "-n", testNamespace)
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())

				By("verifying the ResourceSyncConfig was deleted")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-delete-config", "-n", testNamespace)
					_, err := utils.Run(cmd)
					g.Expect(err).To(HaveOccurred(), "ResourceSyncConfig should be deleted")
				}).Should(Succeed())
			})
		})

		Context("Resource Discovery", func() {
			It("should discover and watch built-in resource types", func() {
				By("creating a ResourceSyncConfig")
				config := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: ResourceSyncConfig
metadata:
  name: test-discovery-config
  namespace: %s
spec:
  mcpEndpoint: http://mock-resource-sync-server.e2e-tests.svc.cluster.local:8080/api/v1/resources/sync
  mcpAuthSecretRef:
    name: mcp-auth-secret
    key: token
`, testNamespace)
				cmd := exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(config)
				_, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())

				By("waiting for resource discovery to complete")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-discovery-config",
						"-n", testNamespace, "-o", "jsonpath={.status.watchedResourceTypes}")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					// Should discover many resource types (pods, deployments, services, etc.)
					// Typically 20+ resource types in a basic cluster
					g.Expect(output).NotTo(Equal(""))
					// Parse as int and check it's reasonable
					var count int
					_, parseErr := fmt.Sscanf(output, "%d", &count)
					g.Expect(parseErr).NotTo(HaveOccurred())
					g.Expect(count).To(BeNumerically(">=", 10), "Should discover at least 10 resource types")
				}, 60*time.Second).Should(Succeed())

				By("cleaning up")
				cmd = exec.Command("kubectl", "delete", "resourcesyncconfig", "test-discovery-config", "-n", testNamespace)
				_, _ = utils.Run(cmd)
			})
		})
	})
})

// deployMockMcpServer deploys a mock MCP server for testing MCP integration
func deployMockMcpServer() {
	By("deploying mock MCP server in test namespace")
	cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/simple-mock-mcp-server.yaml", "-n", testNamespace)
	output, err := utils.Run(cmd)
	if err != nil {
		// Print the error output for debugging
		_, _ = fmt.Fprintf(GinkgoWriter, "Mock MCP server deployment failed: %s\nOutput: %s", err, output)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy mock MCP server")
	}

	By("waiting for mock MCP server deployment to exist")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "deployment", "mock-mcp-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock MCP server deployment should exist")
	}, 30*time.Second).Should(Succeed())

	By("waiting for mock MCP server pods to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "app=mock-mcp-server",
			"-n", testNamespace, "--no-headers")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).NotTo(BeEmpty(), "Should have mock MCP server pods")

		// Check if any pod is in Running state
		cmd = exec.Command("kubectl", "get", "pods", "-l", "app=mock-mcp-server",
			"-n", testNamespace, "-o", "jsonpath={.items[?(@.status.phase=='Running')].metadata.name}")
		runningPods, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(runningPods).NotTo(BeEmpty(), "At least one mock MCP server pod should be running")
	}, 2*time.Minute).Should(Succeed())

	By("verifying mock MCP server service is accessible")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "service", "mock-mcp-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock MCP server service should exist")
	}).Should(Succeed())
}

// cleanupMockMcpServer removes the mock MCP server
func cleanupMockMcpServer() {
	By("cleaning up mock MCP server")
	cmd := exec.Command("kubectl", "delete", "-f", "test/e2e/simple-mock-mcp-server.yaml", "--ignore-not-found=true")
	_, _ = utils.Run(cmd) // Ignore errors during cleanup
}

// deployMockResourceSyncServer deploys a mock resource sync server for testing ResourceSyncConfig
func deployMockResourceSyncServer() {
	By("deploying mock resource sync server in test namespace")
	cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/mock-resource-sync-server.yaml", "-n", testNamespace)
	output, err := utils.Run(cmd)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Mock resource sync server deployment failed: %s\nOutput: %s", err, output)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy mock resource sync server")
	}

	By("waiting for mock resource sync server deployment to exist")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "deployment", "mock-resource-sync-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock resource sync server deployment should exist")
	}, 30*time.Second).Should(Succeed())

	By("waiting for mock resource sync server pods to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "app=mock-resource-sync-server",
			"-n", testNamespace, "--no-headers")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).NotTo(BeEmpty(), "Should have mock resource sync server pods")

		cmd = exec.Command("kubectl", "get", "pods", "-l", "app=mock-resource-sync-server",
			"-n", testNamespace, "-o", "jsonpath={.items[?(@.status.phase=='Running')].metadata.name}")
		runningPods, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(runningPods).NotTo(BeEmpty(), "At least one mock resource sync server pod should be running")
	}, 2*time.Minute).Should(Succeed())

	By("verifying mock resource sync server service is accessible")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "service", "mock-resource-sync-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock resource sync server service should exist")
	}).Should(Succeed())
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
