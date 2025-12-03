package v1alpha1

import (
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSolutionSpec(t *testing.T) {
	g := NewWithT(t)

	t.Run("creates Solution with all fields", func(t *testing.T) {
		solution := &Solution{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-solution",
				Namespace: "default",
			},
			Spec: SolutionSpec{
				Intent: "Deploy test application",
				Context: SolutionContext{
					CreatedBy: "dot-ai-recommend-tool",
					Rationale: "Testing solution CRD",
					Patterns:  []string{"HA Pattern"},
					Policies:  []string{"security-policy"},
				},
				Resources: []ResourceReference{
					{
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Name:       "test-app",
						Namespace:  "default",
					},
				},
				DocumentationURL: "https://example.com/docs",
			},
		}

		g.Expect(solution.Spec.Intent).To(Equal("Deploy test application"))
		g.Expect(solution.Spec.Context.CreatedBy).To(Equal("dot-ai-recommend-tool"))
		g.Expect(solution.Spec.Resources).To(HaveLen(1))
		g.Expect(solution.Spec.DocumentationURL).To(Equal("https://example.com/docs"))
	})

	t.Run("creates Solution with minimal required fields", func(t *testing.T) {
		solution := &Solution{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "minimal-solution",
				Namespace: "default",
			},
			Spec: SolutionSpec{
				Intent: "Deploy minimal app",
				Resources: []ResourceReference{
					{
						APIVersion: "v1",
						Kind:       "Pod",
						Name:       "test-pod",
					},
				},
			},
		}

		g.Expect(solution.Spec.Intent).To(Equal("Deploy minimal app"))
		g.Expect(solution.Spec.Resources).To(HaveLen(1))
		g.Expect(solution.Spec.Context.CreatedBy).To(BeEmpty())
		g.Expect(solution.Spec.DocumentationURL).To(BeEmpty())
	})
}

func TestSolutionStatus(t *testing.T) {
	g := NewWithT(t)

	t.Run("creates Solution status with all fields", func(t *testing.T) {
		solution := &Solution{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-solution",
				Namespace: "default",
			},
			Status: SolutionStatus{
				State:              "deployed",
				ObservedGeneration: 1,
				Resources: ResourceSummary{
					Total:  3,
					Ready:  3,
					Failed: 0,
				},
				Conditions: []metav1.Condition{
					{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "AllResourcesReady",
						Message:            "All resources are healthy",
					},
				},
			},
		}

		g.Expect(solution.Status.State).To(Equal("deployed"))
		g.Expect(solution.Status.ObservedGeneration).To(Equal(int64(1)))
		g.Expect(solution.Status.Resources.Total).To(Equal(3))
		g.Expect(solution.Status.Resources.Ready).To(Equal(3))
		g.Expect(solution.Status.Conditions).To(HaveLen(1))
		g.Expect(solution.Status.Conditions[0].Type).To(Equal("Ready"))
	})
}

func TestResourceReference(t *testing.T) {
	g := NewWithT(t)

	t.Run("creates ResourceReference with namespace", func(t *testing.T) {
		ref := ResourceReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       "test-deploy",
			Namespace:  "production",
		}

		g.Expect(ref.APIVersion).To(Equal("apps/v1"))
		g.Expect(ref.Kind).To(Equal("Deployment"))
		g.Expect(ref.Name).To(Equal("test-deploy"))
		g.Expect(ref.Namespace).To(Equal("production"))
	})

	t.Run("creates ResourceReference without namespace", func(t *testing.T) {
		ref := ResourceReference{
			APIVersion: "v1",
			Kind:       "Namespace",
			Name:       "test-ns",
		}

		g.Expect(ref.APIVersion).To(Equal("v1"))
		g.Expect(ref.Kind).To(Equal("Namespace"))
		g.Expect(ref.Name).To(Equal("test-ns"))
		g.Expect(ref.Namespace).To(BeEmpty())
	})
}

func TestSolutionContext(t *testing.T) {
	g := NewWithT(t)

	t.Run("creates SolutionContext with all fields", func(t *testing.T) {
		context := SolutionContext{
			CreatedBy: "test-user",
			Rationale: "Test deployment",
			Patterns:  []string{"pattern1", "pattern2"},
			Policies:  []string{"policy1"},
		}

		g.Expect(context.CreatedBy).To(Equal("test-user"))
		g.Expect(context.Rationale).To(Equal("Test deployment"))
		g.Expect(context.Patterns).To(Equal([]string{"pattern1", "pattern2"}))
		g.Expect(context.Policies).To(Equal([]string{"policy1"}))
	})

	t.Run("creates empty SolutionContext", func(t *testing.T) {
		context := SolutionContext{}

		g.Expect(context.CreatedBy).To(BeEmpty())
		g.Expect(context.Rationale).To(BeEmpty())
		g.Expect(context.Patterns).To(BeNil())
		g.Expect(context.Policies).To(BeNil())
	})
}

func TestResourceSummary(t *testing.T) {
	g := NewWithT(t)

	t.Run("creates ResourceSummary", func(t *testing.T) {
		summary := ResourceSummary{
			Total:  10,
			Ready:  8,
			Failed: 2,
		}

		g.Expect(summary.Total).To(Equal(10))
		g.Expect(summary.Ready).To(Equal(8))
		g.Expect(summary.Failed).To(Equal(2))
	})
}
