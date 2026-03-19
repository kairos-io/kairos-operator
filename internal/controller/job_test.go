package controller

import (
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

var _ = Describe("volumeForExportArtifacts", func() {
	When("artifacts.volume is set and pvc is nil", func() {
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
			vol, err := volumeForExportArtifacts(artifact, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol.Name).To(Equal("artifacts"))
			Expect(vol.EmptyDir).ToNot(BeNil())
		})
	})

	When("artifacts.volume is empty and pvc is set", func() {
		It("returns volume named artifacts backed by the PVC (read-only)", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec:       buildv1alpha2.OSArtifactSpec{Artifacts: &buildv1alpha2.ArtifactSpec{}},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact-artifacts", Namespace: "default"},
			}
			vol, err := volumeForExportArtifacts(artifact, pvc)
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
			_, err := volumeForExportArtifacts(artifact, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing-vol"))
		})
	})

	When("artifacts is nil and pvc is nil", func() {
		It("returns error", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec:       buildv1alpha2.OSArtifactSpec{},
			}
			_, err := volumeForExportArtifacts(artifact, nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("artifacts.volume"))
		})
	})
})
