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
			Command: []string{
				"true",
			},
			Resources: resources,
		}

		Expect(spec.Resources.Requests.Memory().String()).To(Equal("256Mi"))
		Expect(spec.Resources.Limits).To(BeNil())
	})
})

var _ = Describe("NodeOp.ResourcesOrDefault", func() {
	It("should return a deep copy of Spec.Resources when set", func() {
		origResources := corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("250m"),
			},
		}
		resources := &origResources
		op := &v1alpha1.NodeOp{
			Spec: v1alpha1.NodeOpSpec{
				Command: []string{
					"true",
				},
				Resources: resources,
			},
		}

		Expect(op.ResourcesOrDefault()).To(Equal(*resources))

		// Mutating the returned value (map entry and struct field) must
		// not affect the spec.
		op.ResourcesOrDefault().Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")
		Expect(op.Spec.Resources.Requests.Memory().String()).To(Equal("0"))

		out := op.ResourcesOrDefault()
		out.Limits = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		}

		Expect(op.Spec.Resources.Limits).To(BeNil())
	})
})

var _ = Describe("NodeOp Pod resource resolvers", func() {
	It("resolves nil fields to the built-in default in both requests and limits", func() {
		op := &v1alpha1.NodeOp{
			Spec: v1alpha1.NodeOpSpec{
				Command: []string{
					"true",
				},
			},
		}

		preflight := op.PreflightResourcesOrDefault()
		Expect(preflight.Requests.Cpu().String()).To(Equal("200m"))
		Expect(preflight.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(preflight.Limits.Cpu().String()).To(Equal("200m"))
		Expect(preflight.Limits.Memory().String()).To(Equal("128Mi"))

		reboot := op.RebootResourcesOrDefault()
		Expect(reboot.Requests.Cpu().String()).To(Equal("200m"))
		Expect(reboot.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(reboot.Limits.Cpu().String()).To(Equal("200m"))
		Expect(reboot.Limits.Memory().String()).To(Equal("128Mi"))

		standard := op.RebootResourcesOrDefault()
		Expect(standard.Requests.Cpu().String()).To(Equal("200m"))
		Expect(standard.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(standard.Limits.Cpu().String()).To(Equal("200m"))
		Expect(standard.Limits.Memory().String()).To(Equal("128Mi"))
	})

	It("resolves an explicitly empty field to no resources", func() {
		empty := corev1.ResourceRequirements{}
		op := &v1alpha1.NodeOp{Spec: v1alpha1.NodeOpSpec{
			Command: []string{
				"true",
			},
			PreflightResources: &empty,
			RebootResources:    &empty,
			Resources:          &empty,
		}}

		Expect(op.PreflightResourcesOrDefault().Requests).To(BeEmpty())
		Expect(op.PreflightResourcesOrDefault().Limits).To(BeEmpty())
		Expect(op.RebootResourcesOrDefault().Requests).To(BeEmpty())
		Expect(op.RebootResourcesOrDefault().Limits).To(BeEmpty())
		Expect(op.ResourcesOrDefault().Requests).To(BeEmpty())
		Expect(op.ResourcesOrDefault().Limits).To(BeEmpty())
	})

	It("uses explicit requests and limits without aliasing the spec", func() {
		reqs := &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		}
		op := &v1alpha1.NodeOp{Spec: v1alpha1.NodeOpSpec{
			Command: []string{
				"true",
			},
			PreflightResources: reqs,
			RebootResources:    reqs,
			Resources:          reqs,
		}}

		pref := op.PreflightResourcesOrDefault()
		Expect(pref.Requests.Cpu().String()).To(Equal("200m"))
		Expect(pref.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(pref.Limits.Cpu().String()).To(Equal("250m"))
		Expect(pref.Limits.Memory().String()).To(Equal("256Mi"))

		reboot := op.RebootResourcesOrDefault()
		Expect(reboot.Requests.Cpu().String()).To(Equal("200m"))
		Expect(reboot.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(reboot.Limits.Cpu().String()).To(Equal("250m"))
		Expect(reboot.Limits.Memory().String()).To(Equal("256Mi"))

		std := op.ResourcesOrDefault()
		Expect(std.Requests.Cpu().String()).To(Equal("200m"))
		Expect(std.Requests.Memory().String()).To(Equal("128Mi"))
		Expect(std.Limits.Cpu().String()).To(Equal("250m"))
		Expect(std.Limits.Memory().String()).To(Equal("256Mi"))
	})
})
