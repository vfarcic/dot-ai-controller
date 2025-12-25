package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vfarcic/dot-ai-controller/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	projectImage = "example.com/controller-init:v0.0.1"
)

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purpose of being used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting controller-init integration test suite\n")
	RunSpecs(t, "e2e suite")
}

// Shared constants for all e2e tests
const (
	// namespace where the controller is deployed
	namespace = "controller-init-system"
	// testNamespace where test resources are created
	testNamespace = "e2e-tests"
	// serviceAccountName for the controller
	serviceAccountName = "controller-init-controller-manager"
	// metricsServiceName for the controller metrics
	metricsServiceName = "controller-init-controller-manager-metrics-service"
	// metricsRoleBindingName for RBAC
	metricsRoleBindingName = "controller-init-metrics-binding"
)

var _ = BeforeSuite(func() {
	// Set KIND_CLUSTER to use the "controller-init-test-e2e" cluster by default
	os.Setenv("KIND_CLUSTER", "controller-init-test-e2e")

	By("building the manager(Operator) image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager(Operator) image")

	// TODO(user): If you want to change the e2e test vendor from Kind, ensure the image is
	// built and available before running the tests. Also, remove the following block.
	By("loading the manager(Operator) image on Kind")
	err = utils.LoadImageToKindClusterWithName(projectImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager(Operator) image into Kind")

	// The tests-e2e are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.
	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	// Deploy the controller (shared setup for all tests)
	deployController()
})

var _ = AfterSuite(func() {
	// Skip cleanup in CI environments - the VM gets destroyed anyway
	// This saves time and avoids errors from trying to uninstall from deleted clusters
	if os.Getenv("CI") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping cleanup in CI environment (VM will be destroyed)\n")
		return
	}

	// Undeploy the controller
	undeployController()

	// For local development: cleanup in correct order
	// 1. First uninstall CertManager (before deleting the cluster)
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}

	// 2. Then delete the Kind cluster
	By("Cleaning up Kind cluster")
	clusterName := os.Getenv("KIND_CLUSTER")
	if clusterName == "" {
		clusterName = "controller-init-test-e2e" // Default fallback
	}
	cmd := exec.Command("kind", "delete", "cluster", "--name", clusterName)
	_, _ = utils.Run(cmd) // Ignore errors - cluster might not exist
})

// Manager tests - basic sanity check that the controller is running
var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

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
})

// deployController deploys the controller and creates required namespaces
func deployController() {
	By("creating manager namespace")
	cmd := exec.Command("kubectl", "create", "ns", namespace)
	_, err := utils.Run(cmd)
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create namespace")
	}

	By("labeling the namespace to enforce the restricted security policy")
	cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
		"pod-security.kubernetes.io/enforce=restricted")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

	By("installing CRDs")
	cmd = exec.Command("make", "install")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to install CRDs")

	By("deploying the controller-manager")
	cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

	By("configuring fast persistence sync for e2e tests")
	cmd = exec.Command("kubectl", "set", "env", "deployment/controller-init-controller-manager",
		"-n", namespace,
		"COOLDOWN_SYNC_INTERVAL=5s",
		"COOLDOWN_MIN_PERSIST_DURATION=10s")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to configure persistence env vars")

	By("waiting for controller deployment rollout to complete")
	cmd = exec.Command("kubectl", "rollout", "status", "deployment/controller-init-controller-manager",
		"-n", namespace, "--timeout=120s")
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed waiting for rollout")

	By("creating test namespace without security restrictions")
	cmd = exec.Command("kubectl", "create", "ns", testNamespace)
	_, err = utils.Run(cmd)
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create test namespace")
	}

	By("deploying mock MCP server")
	deployMockMcpServer()
}

// undeployController cleans up the controller and related resources
func undeployController() {
	By("cleaning up the curl pod for metrics")
	cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	By("undeploying the controller-manager")
	cmd = exec.Command("make", "undeploy")
	_, _ = utils.Run(cmd)

	By("uninstalling CRDs")
	cmd = exec.Command("make", "uninstall")
	_, _ = utils.Run(cmd)

	By("removing manager namespace")
	cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)
}

// deployMockMcpServer deploys a mock MCP server for testing MCP integration
func deployMockMcpServer() {
	By("deploying mock MCP server in test namespace")
	cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/simple-mock-mcp-server.yaml", "-n", testNamespace)
	output, err := utils.Run(cmd)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Mock MCP server deployment failed: %s\nOutput: %s", err, output)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy mock MCP server")
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
