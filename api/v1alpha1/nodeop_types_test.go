package v1alpha1_test

import (
	"github.com/kairos-io/kairos-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var _ = Describe("NodeOpSpec.Resources", func() {
	It("should have nil Resources when not set", func() {
		spec := v1alpha1.NodeOpSpec{
			Command: []string{"true"},
		}
		Expect(spec.Resources).To(BeNil())
	})

	It("should accept resources with requests and limits", func() {
		resources := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
		spec := v1alpha1.NodeOpSpec{
			Command:   []string{"true"},
			Resources: resources,
		}
		Expect(spec.Resources).ToNot(BeNil())
		Expect(spec.Resources.Requests).ToNot(BeNil())
		Expect(spec.Resources.Limits).ToNot(BeNil())
		Expect(spec.Resources.Requests.Cpu().String()).To(Equal("500m"))
		Expect(spec.Resources.Limits.Memory().String()).To(Equal("1Gi"))
	})

	It("should accept resources with only limits", func() {
		resources := &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("2"),
			},
		}
		spec := v1alpha1.NodeOpSpec{
			Command:   []string{"true"},
			Resources: resources,
		}
		Expect(spec.Resources.Limits.Cpu().String()).To(Equal("2"))
		Expect(spec.Resources.Requests).To(BeNil())
	})

	It("should accept resources with only requests", func() {
		resources := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		spec := v1alpha1.NodeOpSpec{
			Command:   []string{"true"},
			Resources: resources,
		}
		Expect(spec.Resources.Requests.Memory().String()).To(Equal("256Mi"))
		Expect(spec.Resources.Limits).To(BeNil())
	})
})

var _ = Describe("NodeOp.ResourcesOrDefault", func() {
	It("should return an empty ResourceRequirements when unset", func() {
		op := &v1alpha1.NodeOp{
			Spec: v1alpha1.NodeOpSpec{
				Command: []string{"true"},
			},
		}

		Expect(op.ResourcesOrDefault()).To(Equal(corev1.ResourceRequirements{}))
		Expect(op.Spec.Resources).To(BeNil())
	})

	It("should return a copy of Spec.Resources when set", func() {
		origResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("250m"),
			},
		}
		resources := &origResources
		op := &v1alpha1.NodeOp{
			Spec: v1alpha1.NodeOpSpec{
				Command:   []string{"true"},
				Resources: resources,
			},
		}

		Expect(op.ResourcesOrDefault()).To(Equal(*resources))

		op.ResourcesOrDefault().Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")
		Expect(op.Spec.Resources.Requests.Memory().String()).To(Equal("128Mi"))

		out := op.ResourcesOrDefault()
		out.Limits = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		}
		Expect(op.Spec.Resources.Limits).To(BeNil())
	})
})
