package e2e

import (
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vfarcic/dot-ai-controller/test/utils"
)

// Mock server endpoint for CapabilityScan tests
const mockCapabilityScanServerEndpoint = "http://mock-capability-scan-server.e2e-tests.svc.cluster.local:8080"

// Mock server returns these fixed capabilities:
// - "Pod"
// - "Service"
// - "FakeStaleCRD.old.example.com" (orphaned - doesn't exist in cluster)

var _ = Describe("CapabilityScanConfig", Ordered, func() {

	BeforeAll(func() {
		deployMockCapabilityScanServer()

		By("creating test secret for MCP auth")
		cmd := exec.Command("kubectl", "create", "secret", "generic", "capability-scan-auth-secret",
			"-n", testNamespace,
			"--from-literal=api-key=test-token",
			"--dry-run=client", "-o", "yaml")
		output, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to generate secret YAML")

		cmd = exec.Command("kubectl", "apply", "-n", testNamespace, "-f", "-")
		cmd.Stdin = strings.NewReader(output)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create capability-scan-auth-secret")
	})

	AfterAll(func() {
		By("cleaning up CapabilityScanConfig test resources")
		cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", "--all", "-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		cmd = exec.Command("kubectl", "delete", "secret", "capability-scan-auth-secret", "-n", testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		// Clean up any test CRDs created during tests
		cmd = exec.Command("kubectl", "delete", "crd", "-l", "e2e-test=capability-scan", "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	// =========================================================================
	// Context: CRUD Operations
	// =========================================================================

	Context("CRUD Operations", func() {

		It("should create and validate CapabilityScanConfig", func() {
			const configName = "test-crud-create"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 15
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("verifying the CapabilityScanConfig was created")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(configName))
			}).Should(Succeed())

			By("verifying spec.mcp.endpoint is correct")
			cmd = exec.Command("kubectl", "get", "capabilityscanconfig", configName,
				"-n", testNamespace, "-o", "jsonpath={.spec.mcp.endpoint}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(mockCapabilityScanServerEndpoint))

			By("verifying spec.debounceWindowSeconds is correct")
			cmd = exec.Command("kubectl", "get", "capabilityscanconfig", configName,
				"-n", testNamespace, "-o", "jsonpath={.spec.debounceWindowSeconds}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("15"))
		})

		It("should update CapabilityScanConfig configuration", func() {
			const configName = "test-crud-update"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig with debounceWindowSeconds: 10")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 10
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("verifying initial debounceWindowSeconds is 10")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.spec.debounceWindowSeconds}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("10"))
			}).Should(Succeed())

			By("updating debounceWindowSeconds to 20")
			updatedConfig := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 20
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(updatedConfig)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to update CapabilityScanConfig")

			By("verifying debounceWindowSeconds was updated to 20")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.spec.debounceWindowSeconds}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("20"))
			}).Should(Succeed())
		})

		It("should delete CapabilityScanConfig and cleanup", func() {
			const configName = "test-crud-delete"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for Ready=True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("deleting the CapabilityScanConfig")
			cmd = exec.Command("kubectl", "delete", "capabilityscanconfig", configName, "-n", testNamespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete CapabilityScanConfig")

			By("verifying the CapabilityScanConfig was deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName, "-n", testNamespace)
				_, err := utils.Run(cmd)
				g.Expect(err).To(HaveOccurred(), "CapabilityScanConfig should be deleted")
			}).Should(Succeed())
		})
	})

	// =========================================================================
	// Context: Status Updates
	// =========================================================================

	Context("Status Updates", func() {

		It("should set Ready condition to True when config is active", func() {
			const configName = "test-status-ready"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("verifying Ready condition status is True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("verifying Ready condition reason is ConfigActive")
			cmd = exec.Command("kubectl", "get", "capabilityscanconfig", configName,
				"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].reason}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("ConfigActive"))
		})

		It("should populate lastScanTime after initial reconciliation", func() {
			const configName = "test-status-lastscantime"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("verifying lastScanTime is populated after initial reconciliation")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.lastScanTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastScanTime should be set")
			}, 60*time.Second).Should(Succeed())
		})
	})

	// =========================================================================
	// Context: Authentication
	// =========================================================================

	Context("Authentication", func() {

		It("should authenticate successfully with valid auth secret", func() {
			const configName = "test-auth-valid"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("creating a CapabilityScanConfig referencing the auth secret")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("verifying Ready condition is True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("verifying lastError is empty")
			cmd = exec.Command("kubectl", "get", "capabilityscanconfig", configName,
				"-n", testNamespace, "-o", "jsonpath={.status.lastError}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(BeEmpty(), "lastError should be empty")
		})
	})

	// =========================================================================
	// Context: CRD Event Detection
	// =========================================================================

	Context("CRD Event Detection", func() {

		It("should trigger scan request to MCP when CRD is created", func() {
			const configName = "test-crd-create"
			const testKind = "TestCreate"
			const testGroup = "test.example.com"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				_ = deleteTestCRD(testKind, testGroup)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 2
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for Ready=True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("resetting mock server stats")
			err = resetMockCapabilityScanStats()
			Expect(err).NotTo(HaveOccurred(), "Failed to reset mock stats")

			By("creating a test CRD")
			err = createTestCRD(testKind, testGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test CRD")

			By("waiting for debounce window + buffer")
			time.Sleep(5 * time.Second)

			By("verifying mock server received scan request")
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.ScanRequests).NotTo(BeEmpty(), "Should have scan requests")

				// Check if any scan request contains our test CRD
				found := false
				for _, req := range stats.ScanRequests {
					if strings.Contains(req.ResourceList, testKind+"."+testGroup) {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Scan request should contain "+testKind+"."+testGroup)
			}, 30*time.Second).Should(Succeed())
		})

		It("should trigger delete request to MCP when CRD is deleted", func() {
			const configName = "test-crd-delete"
			const testKind = "TestDelete"
			const testGroup = "test.example.com"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				_ = deleteTestCRD(testKind, testGroup)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 2
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for Ready=True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("creating a test CRD")
			err = createTestCRD(testKind, testGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test CRD")

			By("waiting for initial scan to complete")
			time.Sleep(5 * time.Second)

			By("resetting mock server stats")
			err = resetMockCapabilityScanStats()
			Expect(err).NotTo(HaveOccurred(), "Failed to reset mock stats")

			By("deleting the test CRD")
			err = deleteTestCRD(testKind, testGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete test CRD")

			By("waiting for debounce window + buffer")
			time.Sleep(5 * time.Second)

			By("verifying mock server received delete request")
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.DeleteRequests).NotTo(BeEmpty(), "Should have delete requests")

				// Check if any delete request is for our test CRD
				found := false
				for _, req := range stats.DeleteRequests {
					if req.ID == testKind+"."+testGroup {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Delete request should be for "+testKind+"."+testGroup)
			}, 30*time.Second).Should(Succeed())
		})

		It("should trigger scan request to MCP when CRD is updated", func() {
			const configName = "test-crd-update"
			const testKind = "TestUpdate"
			const testGroup = "test.example.com"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				_ = deleteTestCRD(testKind, testGroup)
			})

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 2
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for Ready=True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("creating a test CRD")
			err = createTestCRD(testKind, testGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test CRD")

			By("waiting for initial scan to complete")
			time.Sleep(5 * time.Second)

			By("resetting mock server stats")
			err = resetMockCapabilityScanStats()
			Expect(err).NotTo(HaveOccurred(), "Failed to reset mock stats")

			By("updating the test CRD")
			err = updateTestCRD(testKind, testGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to update test CRD")

			By("waiting for debounce window + buffer")
			time.Sleep(5 * time.Second)

			By("verifying mock server received scan request for the update")
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.ScanRequests).NotTo(BeEmpty(), "Should have scan requests")

				// Check if any scan request contains our test CRD
				found := false
				for _, req := range stats.ScanRequests {
					if strings.Contains(req.ResourceList, testKind+"."+testGroup) {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Scan request should contain "+testKind+"."+testGroup)
			}, 30*time.Second).Should(Succeed())
		})
	})

	// =========================================================================
	// Context: Resource Filtering
	// =========================================================================

	Context("Resource Filtering", func() {

		It("should apply combined include and exclude filtering", func() {
			const configName = "test-filtering"

			// Test CRDs with different groups
			includedKind := "Included"
			includedGroup := "public.example.com"

			excludedKind := "Excluded"
			excludedGroup := "internal.example.com"

			ignoredKind := "Ignored"
			ignoredGroup := "other.io"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				_ = deleteTestCRD(includedKind, includedGroup)
				_ = deleteTestCRD(excludedKind, excludedGroup)
				_ = deleteTestCRD(ignoredKind, ignoredGroup)
			})

			By("creating a CapabilityScanConfig with include/exclude filters")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 2
  includeResources:
    - "*.example.com"
  excludeResources:
    - "*.internal.example.com"
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for Ready=True")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}, 60*time.Second).Should(Succeed())

			By("resetting mock server stats")
			err = resetMockCapabilityScanStats()
			Expect(err).NotTo(HaveOccurred(), "Failed to reset mock stats")

			By("creating three test CRDs with different groups")
			err = createTestCRD(includedKind, includedGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create included CRD")

			err = createTestCRD(excludedKind, excludedGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create excluded CRD")

			err = createTestCRD(ignoredKind, ignoredGroup)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ignored CRD")

			By("waiting for debounce window + buffer")
			time.Sleep(5 * time.Second)

			By("verifying only the included CRD was scanned")
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())

				// Collect all resourceLists from scan requests
				var allScanned string
				for _, req := range stats.ScanRequests {
					allScanned += req.ResourceList + ","
				}

				// Included CRD should be in scan requests
				g.Expect(allScanned).To(ContainSubstring(includedKind+"."+includedGroup),
					"Included CRD should be scanned")

				// Excluded CRD should NOT be in scan requests
				g.Expect(allScanned).NotTo(ContainSubstring(excludedKind+"."+excludedGroup),
					"Excluded CRD should NOT be scanned")

				// Ignored CRD should NOT be in scan requests
				g.Expect(allScanned).NotTo(ContainSubstring(ignoredKind+"."+ignoredGroup),
					"Ignored CRD should NOT be scanned")
			}, 30*time.Second).Should(Succeed())
		})
	})

	// =========================================================================
	// Context: Startup Reconciliation
	// =========================================================================

	Context("Startup Reconciliation", func() {

		It("should sync diff on startup (scan missing, delete orphaned)", func() {
			const configName = "test-startup-reconciliation"

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "capabilityscanconfig", configName,
					"-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

			By("resetting mock server stats")
			err := resetMockCapabilityScanStats()
			Expect(err).NotTo(HaveOccurred(), "Failed to reset mock stats")

			By("creating a CapabilityScanConfig")
			config := `
apiVersion: dot-ai.devopstoolkit.live/v1alpha1
kind: CapabilityScanConfig
metadata:
  name: ` + configName + `
  namespace: ` + testNamespace + `
spec:
  mcp:
    endpoint: ` + mockCapabilityScanServerEndpoint + `
    authSecretRef:
      name: capability-scan-auth-secret
      key: api-key
  debounceWindowSeconds: 2
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(config)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create CapabilityScanConfig")

			By("waiting for initial reconciliation to complete")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "capabilityscanconfig", configName,
					"-n", testNamespace, "-o", "jsonpath={.status.lastScanTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "lastScanTime should be set after initial reconciliation")
			}, 60*time.Second).Should(Succeed())

			By("verifying mock server was queried for existing capabilities (list operation)")
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.ListRequests).To(BeNumerically(">=", 1),
					"Controller should query MCP for existing capabilities")
			}, 30*time.Second).Should(Succeed())

			By("verifying orphaned capability was deleted")
			// Mock server returns "FakeStaleCRD.old.example.com" which doesn't exist in cluster
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.DeleteRequests).NotTo(BeEmpty(), "Should have delete requests for orphaned capabilities")

				// Check if the orphaned capability was deleted
				found := false
				for _, req := range stats.DeleteRequests {
					if req.ID == "FakeStaleCRD.old.example.com" {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "Should delete orphaned FakeStaleCRD.old.example.com")
			}, 30*time.Second).Should(Succeed())

			By("verifying missing resources were scanned")
			// Cluster has more resources than mock's list (Pod, Service), so scan requests should exist
			Eventually(func(g Gomega) {
				stats, err := getMockCapabilityScanStats()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(stats.ScanRequests).NotTo(BeEmpty(),
					"Should have scan requests for resources not in MCP")
			}, 30*time.Second).Should(Succeed())
		})
	})
})

// =============================================================================
// Helper Functions
// =============================================================================

// deployMockCapabilityScanServer deploys the mock server for CapabilityScan tests
func deployMockCapabilityScanServer() {
	By("deploying mock capability scan server in test namespace")
	cmd := exec.Command("kubectl", "apply", "-f", "test/e2e/mock-capability-scan-server.yaml", "-n", testNamespace)
	output, err := utils.Run(cmd)
	if err != nil {
		_, _ = GinkgoWriter.Write([]byte("Mock capability scan server deployment failed: " + err.Error() + "\nOutput: " + output))
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy mock capability scan server")
	}

	By("waiting for mock capability scan server deployment to exist")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "deployment", "mock-capability-scan-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock capability scan server deployment should exist")
	}, 30*time.Second).Should(Succeed())

	By("waiting for mock capability scan server pods to be ready")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-l", "app=mock-capability-scan-server",
			"-n", testNamespace, "-o", "jsonpath={.items[?(@.status.phase=='Running')].metadata.name}")
		runningPods, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(runningPods).NotTo(BeEmpty(), "At least one mock capability scan server pod should be running")
	}, 2*time.Minute).Should(Succeed())

	By("verifying mock capability scan server service is accessible")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "service", "mock-capability-scan-server", "-n", testNamespace)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "Mock capability scan server service should exist")
	}).Should(Succeed())

	By("waiting for mock capability scan server HTTP endpoint to be ready")
	Eventually(func(g Gomega) {
		// Use Python to check health since wget may not be available in python:alpine
		cmd := exec.Command("kubectl", "exec",
			"-n", testNamespace,
			"deploy/mock-capability-scan-server",
			"--",
			"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://localhost:8080/health').read().decode())")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "HTTP endpoint should respond")
		g.Expect(output).To(ContainSubstring("healthy"))
	}, 60*time.Second, 2*time.Second).Should(Succeed())
}

// resetMockCapabilityScanStats resets the mock server stats via POST /reset
func resetMockCapabilityScanStats() error {
	// Use Python instead of wget since wget may not be available in python:alpine
	cmd := exec.Command("kubectl", "exec",
		"-n", testNamespace,
		"deploy/mock-capability-scan-server",
		"--",
		"python3", "-c", "import urllib.request; req = urllib.request.Request('http://localhost:8080/reset', data=b'', method='POST'); urllib.request.urlopen(req)")
	_, err := utils.Run(cmd)
	return err
}

// MockCapabilityScanStats represents the response from GET /stats
type MockCapabilityScanStats struct {
	ScanRequests   []ScanRequest   `json:"scanRequests"`
	DeleteRequests []DeleteRequest `json:"deleteRequests"`
	ListRequests   int             `json:"listRequests"`
	TotalRequests  int             `json:"totalRequests"`
}

// ScanRequest represents a recorded scan request
type ScanRequest struct {
	ResourceList string `json:"resourceList"`
	Timestamp    string `json:"timestamp"`
}

// DeleteRequest represents a recorded delete request
type DeleteRequest struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
}

// getMockCapabilityScanStats retrieves stats from the mock server via GET /stats
func getMockCapabilityScanStats() (*MockCapabilityScanStats, error) {
	// Use Python instead of wget since wget may not be available in python:alpine
	cmd := exec.Command("kubectl", "exec",
		"-n", testNamespace,
		"deploy/mock-capability-scan-server",
		"--",
		"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://localhost:8080/stats').read().decode())")
	output, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}

	var stats MockCapabilityScanStats
	if err := json.Unmarshal([]byte(output), &stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// createTestCRD creates a test CRD for testing CRD event detection
// The CRD is labeled with e2e-test=capability-scan for easy cleanup
func createTestCRD(kind, group string) error {
	plural := strings.ToLower(kind) + "s"
	crdName := plural + "." + group

	crdYAML := `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: ` + crdName + `
  labels:
    e2e-test: capability-scan
spec:
  group: ` + group + `
  names:
    kind: ` + kind + `
    plural: ` + plural + `
    singular: ` + strings.ToLower(kind) + `
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
`
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(crdYAML)
	_, err := utils.Run(cmd)
	return err
}

// deleteTestCRD deletes a test CRD
func deleteTestCRD(kind, group string) error {
	plural := strings.ToLower(kind) + "s"
	crdName := plural + "." + group
	cmd := exec.Command("kubectl", "delete", "crd", crdName, "--ignore-not-found")
	_, err := utils.Run(cmd)
	return err
}

// updateTestCRD updates a test CRD by adding/modifying an annotation
func updateTestCRD(kind, group string) error {
	plural := strings.ToLower(kind) + "s"
	crdName := plural + "." + group
	cmd := exec.Command("kubectl", "annotate", "crd", crdName,
		"updated-at="+time.Now().Format(time.RFC3339), "--overwrite")
	_, err := utils.Run(cmd)
	return err
}
