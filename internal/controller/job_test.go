package controller

import (
	"context"
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("buildISOCommand", func() {
	// Tests for --cloud-config flag order
	// See: https://github.com/kairos-io/kairos-operator/pull/73

	When("CloudConfigRef is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cc", Key: "config.yaml"},
					},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("GRUBConfig is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("neither CloudConfigRef nor GRUBConfig is set", func() {
		It("does not include --cloud-config flag", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(cmd).ToNot(ContainSubstring("--cloud-config"))
			Expect(cmd).To(ContainSubstring("dir:/rootfs"))
		})
	})
})

var _ = Describe("buildUKICommand", func() {
	// Tests for --cloud-config flag order
	// See: https://github.com/kairos-io/kairos-operator/pull/73

	When("CloudConfigRef is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cc", Key: "config.yaml"},
					},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("GRUBConfig is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("neither CloudConfigRef nor GRUBConfig is set", func() {
		It("does not include --cloud-config flag", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(cmd).ToNot(ContainSubstring("--cloud-config"))
			Expect(cmd).To(ContainSubstring("dir:/rootfs"))
		})
	})
})

var _ = Describe("newBuilderPod scheduling", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var pvc *corev1.PersistentVolumeClaim

	BeforeEach(func() {
		r = &OSArtifactReconciler{ToolImage: "tool-image"}
		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: "myimage:latest"},
				Artifacts: &buildv1alpha2.ArtifactSpec{ISO: true},
			},
		}
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pvc"},
		}
	})

	When("nodeSelector is set", func() {
		It("propagates nodeSelector to the pod spec", func() {
			artifact.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "arm64"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.NodeSelector).To(Equal(map[string]string{"kubernetes.io/arch": "arm64"}))
		})
	})

	When("tolerations are set", func() {
		It("propagates tolerations to the pod spec", func() {
			artifact.Spec.Tolerations = []corev1.Toleration{
				{Key: "arm", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.Tolerations).To(ContainElement(
				corev1.Toleration{Key: "arm", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			))
		})
	})

	When("affinity is set", func() {
		It("propagates affinity to the pod spec", func() {
			artifact.Spec.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "kubernetes.io/arch",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"arm64"},
							}},
						}},
					},
				},
			}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.Affinity).To(Equal(artifact.Spec.Affinity))
		})
	})

	When("no scheduling fields are set", func() {
		It("leaves nodeSelector, tolerations, and affinity unset", func() {
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.NodeSelector).To(BeNil())
			Expect(pod.Spec.Tolerations).To(BeNil())
			Expect(pod.Spec.Affinity).To(BeNil())
		})
	})

	When("pod labels are set", func() {
		It("propagates labels to the pod metadata", func() {
			artifact.Spec.PodLabels = map[string]string{"team": "platform", "env": "prod"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Labels).To(HaveKeyWithValue("team", "platform"))
			Expect(pod.Labels).To(HaveKeyWithValue("env", "prod"))
		})
	})

	When("pod annotations are set", func() {
		It("propagates annotations to the pod metadata", func() {
			artifact.Spec.PodAnnotations = map[string]string{"prometheus.io/scrape": "false"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Annotations).To(HaveKeyWithValue("prometheus.io/scrape", "false"))
		})
	})
})

var _ = Describe("volumeForExportArtifacts", func() {
	When("artifacts.volume is set and no PVC exists", func() {
		It("returns volume named artifacts with source from spec.volumes", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{Volume: "my-artifacts"},
					Volumes: []corev1.Volume{
						{Name: "my-artifacts", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			vol, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol.Name).To(Equal("artifacts"))
			Expect(vol.EmptyDir).ToNot(BeNil())
		})
	})

	When("artifacts.volume is empty and a labeled PVC exists", func() {
		It("returns volume named artifacts backed by the PVC (read-only)", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact", Namespace: "default"},
				Spec:       buildv1alpha2.OSArtifactSpec{Artifacts: &buildv1alpha2.ArtifactSpec{}},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-artifact-artifacts",
					Namespace: "default",
					Labels:    map[string]string{artifactLabel: "my-artifact"},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pvc).Build()
			vol, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol.Name).To(Equal("artifacts"))
			Expect(vol.PersistentVolumeClaim).ToNot(BeNil())
			Expect(vol.PersistentVolumeClaim.ClaimName).To(Equal("my-artifact-artifacts"))
			Expect(vol.PersistentVolumeClaim.ReadOnly).To(BeTrue())
		})
	})

	When("artifacts.volume is set but volume not in spec.volumes", func() {
		It("returns error", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{Volume: "missing-vol"},
					Volumes:   []corev1.Volume{},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			_, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing-vol"))
		})
	})

	When("artifacts is nil and no PVC exists", func() {
		It("returns error", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact", Namespace: "default"},
				Spec:       buildv1alpha2.OSArtifactSpec{},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			_, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("artifacts.volume"))
		})
	})
})
