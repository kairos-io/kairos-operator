package v1alpha2_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kairos-io/kairos-operator/api/v1alpha2"
)

// validImageRef returns a minimal spec with image.ref set so Validate passes
func validImageRef(ref string) v1alpha2.OSArtifactSpec {
	return v1alpha2.OSArtifactSpec{
		Image: v1alpha2.ImageSpec{Ref: ref},
	}
}

var _ = Describe("OSArtifactSpec.ArchSanitized", func() {
	Describe("Valid architectures (from spec.artifacts)", func() {
		It("should accept 'amd64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "amd64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("amd64"))
		})

		It("should accept 'arm64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "arm64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("arm64"))
		})

		// TODO: Arch will need to be set for the first stage too when we fix this:
		// https://github.com/kairos-io/kairos/issues/3966
		It("should accept empty string when Artifacts is nil", func() {
			spec := validImageRef("img")
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal(""))
		})

		It("should accept empty string when Artifacts.Arch is empty", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal(""))
		})
	})

	Describe("Invalid architectures", func() {
		It("should reject 'x86_64'", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "x86_64"},
			}
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		It("should reject 'AMD64' (uppercase)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image:     v1alpha2.ImageSpec{Ref: "img"},
				Artifacts: &v1alpha2.ArtifactSpec{Arch: "AMD64"},
			}
			_, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("OSArtifactSpec.Validate", func() {
	Describe("spec.image is required", func() {
		It("validates when image.ref is set", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})

	Describe("spec.volumes", func() {
		It("returns error for reserved volume name", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "artifacts"}}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})

		It("returns error for duplicate volume name", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "v1"}, {Name: "v1"}}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})
	})

	Describe("spec.image", func() {
		It("returns error when ref empty and neither buildOptions nor ociSpec", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one of buildOptions or ociSpec must be set"))
		})

		It("returns error when builtImageName set and ref set (mutually exclusive)", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			spec.Image.BuiltImageName = "my-registry.io/my-image:tag"
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("builtImageName"))
			Expect(err.Error()).To(ContainSubstring("ref must be empty"))
		})

		It("returns nil when ref set (buildOptions and ociSpec ignored)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					Ref:          "quay.io/kairos/kairos:v1",
					BuildOptions: &v1alpha2.BuildOptions{Version: "v1"},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when buildOptions without version", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{},
				},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildOptions.version is required"))
		})

		It("returns nil when only ref", func() {
			spec := validImageRef("quay.io/kairos/kairos:v1")
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when only buildOptions with version", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{Version: "v3.6.0"},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when only ociSpec ref", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "my-ocispec", Key: "Dockerfile"},
					},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when both buildOptions and ociSpec (operator injects FROM + kairos-init)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuildOptions: &v1alpha2.BuildOptions{Version: "v3.6.0"},
					OCISpec: &v1alpha2.OCISpec{
						Ref: &v1alpha2.SecretKeySelector{Name: "my-ocispec", Key: "Dockerfile"},
					},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns error when ociSpec.buildContextVolume references missing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref:                &v1alpha2.SecretKeySelector{Name: "df", Key: "Dockerfile"},
						BuildContextVolume: "missing-vol",
					},
				},
				Volumes: []corev1.Volume{{Name: "ctx"}},
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildContextVolume"))
		})

		It("returns nil when ociSpec.buildContextVolume references existing volume", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					OCISpec: &v1alpha2.OCISpec{
						Ref:                &v1alpha2.SecretKeySelector{Name: "df", Key: "Dockerfile"},
						BuildContextVolume: "ctx",
					},
				},
				Volumes: []corev1.Volume{{Name: "ctx"}},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})

		It("returns nil when builtImageName set and building (ref empty)", func() {
			spec := v1alpha2.OSArtifactSpec{
				Image: v1alpha2.ImageSpec{
					BuiltImageName: "my-registry.io/my-image:tag",
					OCISpec:        &v1alpha2.OCISpec{Ref: &v1alpha2.SecretKeySelector{Name: "df", Key: "Dockerfile"}},
				},
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})

	Describe("spec.artifacts", func() {
		It("returns error when overlayISOVolume references missing volume", func() {
			spec := validImageRef("img")
			spec.Artifacts = &v1alpha2.ArtifactSpec{OverlayISOVolume: "missing"}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayISOVolume"))
			Expect(err.Error()).To(ContainSubstring("missing"))
		})

		It("returns error when overlayRootfsVolume references missing volume", func() {
			spec := validImageRef("img")
			spec.Artifacts = &v1alpha2.ArtifactSpec{OverlayRootfsVolume: "missing"}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayRootfsVolume"))
		})

		It("returns nil when overlay volumes exist in spec.volumes", func() {
			spec := validImageRef("img")
			spec.Volumes = []corev1.Volume{{Name: "iso-ov"}, {Name: "rootfs-ov"}}
			spec.Artifacts = &v1alpha2.ArtifactSpec{
				OverlayISOVolume:    "iso-ov",
				OverlayRootfsVolume: "rootfs-ov",
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})
})
