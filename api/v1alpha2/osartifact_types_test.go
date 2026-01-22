package v1alpha2

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
