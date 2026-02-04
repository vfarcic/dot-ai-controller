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

	// expectedDocsCount is the number of markdown files in examples/docs/
	// Update this if you add or remove files from examples/docs/
	const expectedDocsCount = 5

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
    - "examples/docs/**/*.md"
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

			By("verifying documentCount matches expected count")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-public-repo",
					"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "documentCount should be set")
				var count int
				_, parseErr := fmt.Sscanf(output, "%d", &count)
				g.Expect(parseErr).NotTo(HaveOccurred())
				g.Expect(count).To(Equal(expectedDocsCount),
					"Should have synced exactly %d documents from examples/docs/", expectedDocsCount)
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
    - "examples/docs/**/*.md"
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
		It("should only sync files matching path patterns and respect excludes", func() {
			By("creating a GitKnowledgeSource with exclude pattern")
			// Exclude api-reference.md to test exclude functionality
			// This should sync 4 files (all except api-reference.md)
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
    - "examples/docs/**/*.md"
  exclude:
    - "examples/docs/api-reference.md"
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

			By("verifying documentCount reflects exclude pattern")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-patterns",
					"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				var count int
				_, parseErr := fmt.Sscanf(output, "%d", &count)
				g.Expect(parseErr).NotTo(HaveOccurred())
				// Should be expectedDocsCount - 1 because api-reference.md is excluded
				g.Expect(count).To(Equal(expectedDocsCount-1),
					"Should have synced %d documents (excluded api-reference.md)", expectedDocsCount-1)
			}).Should(Succeed())

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-patterns", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Change Detection (M4)", func() {
		It("should not re-sync documents when nothing has changed", func() {
			By("creating a GitKnowledgeSource")
			gks := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: test-change-detection
  namespace: %s
spec:
  repository:
    url: https://github.com/vfarcic/dot-ai-controller.git
    branch: main
  paths:
    - "examples/docs/**/*.md"
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

			By("waiting for first sync to complete with documents synced")
			var firstSyncCommit string
			var firstDocCount int
			Eventually(func(g Gomega) {
				// Check that lastSyncedCommit is set
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncedCommit}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncedCommit should be set")
				firstSyncCommit = output

				// Check that documentCount equals expected (first sync should sync all files)
				cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
					"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
				countOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(countOutput).NotTo(BeEmpty(), "documentCount should be set on first sync")
				_, parseErr := fmt.Sscanf(countOutput, "%d", &firstDocCount)
				g.Expect(parseErr).NotTo(HaveOccurred())
				g.Expect(firstDocCount).To(Equal(expectedDocsCount),
					"First sync should sync exactly %d documents", expectedDocsCount)
			}, 3*time.Minute).Should(Succeed())

			_, _ = fmt.Fprintf(GinkgoWriter, "First sync: commit=%s, documentCount=%d\n", firstSyncCommit, firstDocCount)

			By("recording the lastSyncTime before triggering re-sync")
			var firstSyncTime string
			cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
				"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
			firstSyncTime, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("triggering a re-sync by updating an annotation")
			cmd = exec.Command("kubectl", "annotate", "gitknowledgesource", "test-change-detection",
				"-n", testNamespace, "force-resync="+time.Now().Format(time.RFC3339), "--overwrite")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to annotate GitKnowledgeSource")

			By("waiting for re-sync to complete (lastSyncTime should change)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal(firstSyncTime), "lastSyncTime should be updated after re-sync")
			}, 2*time.Minute).Should(Succeed())

			By("verifying documentCount remains unchanged (change detection)")
			// With M4 change detection working:
			// - No files changed since lastSyncedCommit
			// - Therefore 0 files are synced in this operation
			// - documentCount stays at previous value (cumulative counter)
			//
			// Without M4 (would re-sync all files):
			// - All matching files would be re-synced
			// - documentCount would increase (5 + 5 = 10)
			cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
				"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
			secondCountOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			_, _ = fmt.Fprintf(GinkgoWriter, "Second sync documentCount output: '%s'\n", secondCountOutput)

			// documentCount is a cumulative counter that increments with each file processed
			// When no files change, 0 files are processed, so count stays the same
			var secondCount int
			_, parseErr := fmt.Sscanf(secondCountOutput, "%d", &secondCount)
			Expect(parseErr).NotTo(HaveOccurred())
			Expect(secondCount).To(Equal(firstDocCount),
				"documentCount should remain %d when no files changed. Got %d. "+
					"This validates M4 Change Detection (incremental sync processed 0 files).",
				firstDocCount, secondCount)

			By("verifying lastSyncedCommit remains the same")
			cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-change-detection",
				"-n", testNamespace, "-o", "jsonpath={.status.lastSyncedCommit}")
			secondCommit, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(secondCommit).To(Equal(firstSyncCommit), "lastSyncedCommit should remain the same")

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-change-detection", "-n", testNamespace)
			_, _ = utils.Run(cmd)
		})
	})

	Context("Skip Tracking (M6)", func() {
		It("should skip files exceeding maxFileSizeBytes and report in status", func() {
			By("creating a GitKnowledgeSource with maxFileSizeBytes limit")
			// Set limit to 1024 bytes - this should skip api-reference.md (3887 bytes)
			// and sync the other 4 files (all under 400 bytes)
			gks := fmt.Sprintf(`
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: GitKnowledgeSource
metadata:
  name: test-skip-tracking
  namespace: %s
spec:
  repository:
    url: https://github.com/vfarcic/dot-ai-controller.git
    branch: main
  paths:
    - "examples/docs/**/*.md"
  maxFileSizeBytes: 1024
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
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-skip-tracking",
					"-n", testNamespace, "-o", "jsonpath={.status.lastSyncTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastSyncTime should be set")
			}, 3*time.Minute).Should(Succeed())

			By("verifying documentCount is 4 (files under size limit)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-skip-tracking",
					"-n", testNamespace, "-o", "jsonpath={.status.documentCount}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				var count int
				_, parseErr := fmt.Sscanf(output, "%d", &count)
				g.Expect(parseErr).NotTo(HaveOccurred())
				g.Expect(count).To(Equal(4),
					"Should have synced 4 documents (excluding api-reference.md which exceeds 1024 bytes)")
			}).Should(Succeed())

			By("verifying skippedDocuments is 1")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-skip-tracking",
					"-n", testNamespace, "-o", "jsonpath={.status.skippedDocuments}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				var count int
				_, parseErr := fmt.Sscanf(output, "%d", &count)
				g.Expect(parseErr).NotTo(HaveOccurred())
				g.Expect(count).To(Equal(1),
					"Should have skipped 1 document (api-reference.md at 3887 bytes > 1024 limit)")
			}).Should(Succeed())

			By("verifying skippedFiles contains api-reference.md with reason")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gitknowledgesource", "test-skip-tracking",
					"-n", testNamespace, "-o", "jsonpath={.status.skippedFiles[0].path}")
				pathOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pathOutput).To(ContainSubstring("api-reference.md"),
					"Skipped file should be api-reference.md")

				cmd = exec.Command("kubectl", "get", "gitknowledgesource", "test-skip-tracking",
					"-n", testNamespace, "-o", "jsonpath={.status.skippedFiles[0].reason}")
				reasonOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(reasonOutput).To(ContainSubstring("exceeded max file size"),
					"Reason should mention exceeded max file size")
			}).Should(Succeed())

			By("cleaning up")
			cmd = exec.Command("kubectl", "delete", "gitknowledgesource", "test-skip-tracking", "-n", testNamespace)
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
