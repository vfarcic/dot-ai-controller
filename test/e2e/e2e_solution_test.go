package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vfarcic/dot-ai-controller/test/utils"
)

var _ = Describe("Solution", Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("CRUD Operations", func() {
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

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "solution", "test-basic-solution", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

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

			By("deleting the Solution")
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

	Context("Resource Tracking", func() {
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

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "solution", "tracking-test-solution", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", "tracking-test-deployment", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "service", "tracking-test-service", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

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
		})
	})

	Context("Health Checking", func() {
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

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "solution", "health-test-solution", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", "health-test-deployment", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

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

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "solution", "degraded-test-solution", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
				cmd = exec.Command("kubectl", "delete", "deployment", "degraded-test-deployment", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

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

			DeferCleanup(func() {
				cmd := exec.Command("kubectl", "delete", "solution", "missing-resources-solution", "-n", testNamespace, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			})

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
		})
	})

	Context("Garbage Collection", func() {
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

			// Note: No DeferCleanup for resources - we test that GC deletes them

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
})
