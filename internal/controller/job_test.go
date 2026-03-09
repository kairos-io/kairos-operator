package controller

import (
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
