package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vfarcic/dot-ai-controller/test/utils"
)

var _ = Describe("ResourceSyncConfig", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	// Shared setup for all ResourceSyncConfig tests - deploy mock server once
	BeforeAll(func() {
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

	AfterAll(func() {
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

			By("verifying lastSyncTime is set after initial sync")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-status-config",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncTime should be set after initial sync")
			}, 60*time.Second).Should(Succeed())

			By("verifying lastResyncTime is set after initial sync")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "resourcesyncconfig", "test-status-config",
					"-n", testNamespace, "-o", "jsonpath={.status.lastResyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastResyncTime should be set after initial sync")
			}, 60*time.Second).Should(Succeed())

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
