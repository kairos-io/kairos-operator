package v1alpha2

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = ginkgo.Describe("OSArtifactSpec.ArchSanitized", func() {
	var spec *OSArtifactSpec

	ginkgo.BeforeEach(func() {
		spec = &OSArtifactSpec{}
	})

	ginkgo.Describe("Valid architectures", func() {
		ginkgo.It("should accept 'amd64'", func() {
			spec.Arch = "amd64"
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("amd64"))
		})

		ginkgo.It("should accept 'arm64'", func() {
			spec.Arch = "arm64"
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal("arm64"))
		})

		ginkgo.It("should accept empty string", func() {
			spec.Arch = ""
			arch, err := spec.ArchSanitized()
			Expect(err).ToNot(HaveOccurred())
			Expect(arch).To(Equal(""))
		})
	})

	ginkgo.Describe("Invalid architectures", func() {
		ginkgo.It("should reject 'x86_64'", func() {
			spec.Arch = "x86_64"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		ginkgo.It("should reject 'aarch64'", func() {
			spec.Arch = "aarch64"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		ginkgo.It("should reject 'ppc64le'", func() {
			spec.Arch = "ppc64le"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		ginkgo.It("should reject 's390x'", func() {
			spec.Arch = "s390x"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		ginkgo.It("should reject arbitrary strings", func() {
			spec.Arch = "invalid-arch"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})
	})

	ginkgo.Describe("Case sensitivity", func() {
		ginkgo.It("should reject 'AMD64' (uppercase)", func() {
			spec.Arch = "AMD64"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})

		ginkgo.It("should reject 'ARM64' (uppercase)", func() {
			spec.Arch = "ARM64"
			arch, err := spec.ArchSanitized()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("arch must be either 'amd64', 'arm64', or empty"))
			Expect(arch).To(Equal(""))
		})
	})
})

var _ = ginkgo.Describe("OSArtifactSpec.Validate", func() {
	var spec *OSArtifactSpec

	ginkgo.BeforeEach(func() {
		spec = &OSArtifactSpec{}
	})

	ginkgo.Describe("when no volumes, importers, or volumeBindings are set", func() {
		ginkgo.It("returns nil", func() {
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})

	ginkgo.Describe("when volumeBindings reference volumes that exist in spec.volumes", func() {
		ginkgo.It("returns nil", func() {
			spec.Volumes = []corev1.Volume{
				{Name: "my-context"},
				{Name: "my-overlay"},
				{Name: "my-rootfs"},
			}
			spec.VolumeBindings = &VolumeBindings{
				BuildContext:  "my-context",
				OverlayISO:    "my-overlay",
				OverlayRootfs: "my-rootfs",
			}
			Expect(spec.Validate()).ToNot(HaveOccurred())
		})
	})

	ginkgo.Describe("when volumeBindings.buildContext references a name not in spec.volumes", func() {
		ginkgo.It("returns error", func() {
			spec.Volumes = []corev1.Volume{
				{Name: "something-else"},
			}
			spec.VolumeBindings = &VolumeBindings{
				BuildContext: "nonexistent",
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildContext"))
			Expect(err.Error()).To(ContainSubstring("nonexistent"))
		})
	})

	ginkgo.Describe("when volumeBindings.overlayISO references a name not in spec.volumes", func() {
		ginkgo.It("returns error", func() {
			spec.Volumes = []corev1.Volume{
				{Name: "something-else"},
			}
			spec.VolumeBindings = &VolumeBindings{
				OverlayISO: "nonexistent",
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayISO"))
			Expect(err.Error()).To(ContainSubstring("nonexistent"))
		})
	})

	ginkgo.Describe("when volumeBindings.overlayRootfs references a name not in spec.volumes", func() {
		ginkgo.It("returns error", func() {
			spec.Volumes = []corev1.Volume{
				{Name: "something-else"},
			}
			spec.VolumeBindings = &VolumeBindings{
				OverlayRootfs: "nonexistent",
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlayRootfs"))
			Expect(err.Error()).To(ContainSubstring("nonexistent"))
		})
	})

	ginkgo.Describe("when spec.volumes contains a reserved name", func() {
		reservedNames := []string{"artifacts", "rootfs", "config", "dockerfile", "cloudconfig"}

		for _, name := range reservedNames {
			ginkgo.It("returns error for reserved name '"+name+"'", func() {
				spec.Volumes = []corev1.Volume{
					{Name: name},
				}
				err := spec.Validate()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(name))
				Expect(err.Error()).To(ContainSubstring("reserved"))
			})
		}
	})

	ginkgo.Describe("when volumeBindings is set but spec.volumes is empty", func() {
		ginkgo.It("returns error", func() {
			spec.VolumeBindings = &VolumeBindings{
				BuildContext: "my-context",
			}
			err := spec.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("buildContext"))
			Expect(err.Error()).To(ContainSubstring("my-context"))
		})
	})
})
