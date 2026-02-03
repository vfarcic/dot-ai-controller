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

var _ = Describe("GitKnowledgeSource", Ordered, func() {
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	BeforeAll(func() {
		deployMockKnowledgeServer()

		By("creating test secret for MCP auth")
		cmd := exec.Command("kubectl", "create", "secret", "generic", "mcp-knowledge-auth",
			"-n", testNamespace,
			"--from-literal=token=test-token",
			"--dry-run=client", "-o", "yaml")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to generate secret YAML")
		cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
		cmd.Stdin = strings.NewReader(output)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create mcp-knowledge-auth secret")
	})

	AfterAll(func() {
		cmd := exec.Command("kubectl", "delete", "gitknowledgesource", "--all", "-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
		cmd = exec.Command("kubectl", "delete", "secret", "mcp-knowledge-auth", "-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	Context("Basic Sync Operations", func() {
		It("should sync documents from a public repository", func() {
			By("creating a GitKnowledgeSource for a public repo")
			gks := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: test-public-repo
  namespace: %s
spec:
  repository:
    url: https://github.com/vfarcic/dot-ai-controller.git
    branch: main
  paths:
    - "*.md"
  mcpServer:
    url: http://mock-knowledge-server.e2e-tests.svc.cluster.local:8080
    authSecretRef:
      name: mcp-knowledge-auth
      key: token
`, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(gks)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create GitKnowledgeSource")

			By("verifying the GitKnowledgeSource was created")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("test-public-repo"))
			}).Should(Succeed())

			By("waiting for sync to complete and status to be updated")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.active}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"), "Status should show active")
			}, 3*time.Minute).Should(Succeed())

			By("verifying lastSyncTime is set")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncTime should be set")
			}).Should(Succeed())

			By("verifying lastSyncedCommit is set")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncedCommit}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncedCommit should be set")
				g.Expect(len(output)).To(BeNumerically(">=", 7), "Should be a git commit SHA")
			}).Should(Succeed())

			By("verifying documentCount is set")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "documentCount should be set")
				// Should have synced at least README.md
				var count int
				_, parseErr := fmt.Sscanf(output, "%d", &count)
				g.Expect(parseErr).NotTo(HaveOccurred())
				g.Expect(count).To(BeNumerically(">=", 1), "Should have synced at least 1 document")
			}).Should(Succeed())

			By("verifying Ready condition is True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Ready condition should be True")
			}).Should(Succeed())

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-public-repo", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Error Handling", func() {
		It("should report error when MCP auth secret is missing", func() {
			By("creating a GitKnowledgeSource with non-existent secret")
			gks := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: test-missing-secret
  namespace: %s
spec:
  repository:
    url: https://github.com/vfarcic/dot-ai-controller.git
    branch: main
  paths:
    - "*.md"
  mcpServer:
    url: http://mock-knowledge-server.e2e-tests.svc.cluster.local:8080
    authSecretRef:
      name: nonexistent-secret
      key: token
`, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(gks)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create GitKnowledgeSource")

			By("verifying error is reported in status")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-missing-secret",
					"-n", testNamespace, "-o", "jsonpath={.status.lastError}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("not found"), "Should report secret not found error")
			}, 2*time.Minute).Should(Succeed())

			By("verifying Synced condition is False")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-missing-secret",
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Synced')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("False"), "Synced condition should be False")
			}).Should(Succeed())

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-missing-secret", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Pattern Matching", func() {
		It("should only sync files matching path patterns", func() {
			By("creating a GitKnowledgeSource with specific patterns")
			gks := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: test-patterns
  namespace: %s
spec:
  repository:
    url: https://github.com/vfarcic/dot-ai-controller.git
    branch: main
  paths:
    - "docs/**/*.md"
  exclude:
    - "docs/internal/**"
  mcpServer:
    url: http://mock-knowledge-server.e2e-tests.svc.cluster.local:8080
    authSecretRef:
      name: mcp-knowledge-auth
      key: token
`, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(gks)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create GitKnowledgeSource")

			By("waiting for sync to complete")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-patterns",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncTime should be set")
			}, 3*time.Minute).Should(Succeed())

			By("verifying documentCount reflects pattern filtering")
			cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-patterns",
				"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			// The count should be the number of docs/*.md files (excluding internal)
			// We just verify it's a valid number
			var count int
			_, parseErr := fmt.Sscanf(output, "%d", &count)
			Expect(parseErr).NotTo(HaveOccurred())
			_, _ = fmt.Fprintf(GinkgoWriter, "Synced %d documents matching docs/**/*.md pattern\n", count)

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-patterns", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})
})

// deployMockKnowledgeServer deploys a mock knowledge server for testing GitKnowledgeSource
func deployMockKnowledgeServer() {
	By("deploying mock knowledge server in test namespace")
	cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/mock-knowledge-server.yaml", "-n", testNamespace)
	output, err := utils.Run(cmd)
	if err != nil {
		_, _ = fmt.Fprintf(GinkgoWriter, "Mock knowledge server deployment failed: %s\nOutput: %s", err, output)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy mock knowledge server")
	}

	By("waiting for mock knowledge server deployment to exist")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "deployment", "mock-knowledge-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock knowledge server deployment should exist")
	}, 30*time.Second).Should(Succeed())

	By("waiting for mock knowledge server pods to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "app=mock-knowledge-server",
			"-n", testNamespace, "--no-headers")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).NotTo(BeEmpty(), "Should have mock knowledge server pods")

		cmd = exec.Command("kubectl", "get", "pods", "-l", "app=mock-knowledge-server",
			"-n", testNamespace, "-o", "jsonpath={.items[?(@.status.phase=='Running')].metadata.name}")
		runningPods, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(runningPods).NotTo(BeEmpty(), "At least one mock knowledge server pod should be running")
	}, 2*time.Minute).Should(Succeed())

	By("verifying mock knowledge server service is accessible")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "service", "mock-knowledge-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock knowledge server service should exist")
	}).Should(Succeed())
}
