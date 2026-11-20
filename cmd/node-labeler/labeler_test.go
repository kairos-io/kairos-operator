package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const sampleKairosRelease = `KAIROS_ARCH="amd64"
KAIROS_BUG_REPORT_URL="https://github.com/kairos-io/kairos/issues"
KAIROS_FAMILY="hadron"
KAIROS_FIPS="false"
KAIROS_FLAVOR="hadron"
KAIROS_FLAVOR_RELEASE="v0.0.4"
KAIROS_HOME_URL="https://github.com/kairos-io/kairos"
KAIROS_ID="kairos"
KAIROS_ID_LIKE="kairos-core-hadron-v0.0.4"
KAIROS_INIT_VERSION="v0.8.4"
KAIROS_MODEL="generic"
KAIROS_NAME="kairos-core-hadron-v0.0.4"
KAIROS_RELEASE="v4.0.3"
KAIROS_SOFTWARE_VERSION="v1.32.4+k3s1"
KAIROS_SOFTWARE_VERSION_PREFIX="k3s"
KAIROS_TARGETARCH="amd64"
KAIROS_TRUSTED_BOOT="false"
KAIROS_VARIANT="core"
KAIROS_VERSION="v4.0.3"
`

func writeKairosRelease(content string) string {
	dir := GinkgoT().TempDir()
	if content != "" {
		Expect(os.WriteFile(filepath.Join(dir, "kairos-release"), []byte(content), 0644)).To(Succeed())
	}
	return dir
}

func writeCmdlineFile(content string) string {
	dir := GinkgoT().TempDir()
	path := filepath.Join(dir, "cmdline")
	Expect(os.WriteFile(path, []byte(content), 0644)).To(Succeed())
	return path
}

func newNode(name string, labels, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

var _ = Describe("Node Labeler", func() {

	Describe("collectMetadata", func() {
		Context("with a Kairos node", func() {
			It("returns the expected labels", func() {
				etcDir := writeKairosRelease(sampleKairosRelease)
				cmdlinePath := writeCmdlineFile("BOOT_IMAGE=/boot/vmlinuz COS_ACTIVE root=LABEL=COS_ACTIVE")

				labels, _ := collectMetadata(etcDir, cmdlinePath)
				Expect(labels).To(HaveKeyWithValue("kairos.io/managed", "true"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/id", "kairos"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/family", "hadron"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/flavor", "hadron"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/flavor-release", "v0.0.4"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/variant", "core"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/release", "v4.0.3"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/model", "generic"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/arch", "amd64"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/trusted-boot", "false"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/fips", "false"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/software-version-prefix", "k3s"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/software-version", "v1.32.4-k3s1"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/boot-state", "active"))
				Expect(labels).NotTo(HaveKey("kairos.io/targetarch"))
			})

			It("returns the expected annotations", func() {
				etcDir := writeKairosRelease(sampleKairosRelease)
				cmdlinePath := writeCmdlineFile("COS_ACTIVE")

				_, annotations := collectMetadata(etcDir, cmdlinePath)
				Expect(annotations).To(HaveKeyWithValue("kairos.io/name", "kairos-core-hadron-v0.0.4"))
				Expect(annotations).To(HaveKeyWithValue("kairos.io/id-like", "kairos-core-hadron-v0.0.4"))
				Expect(annotations).To(HaveKeyWithValue("kairos.io/init-version", "v0.8.4"))
				Expect(annotations).To(HaveKeyWithValue("kairos.io/version", "v4.0.3"))
				Expect(annotations).To(HaveKeyWithValue("kairos.io/bug-report-url", "https://github.com/kairos-io/kairos/issues"))
				Expect(annotations).To(HaveKeyWithValue("kairos.io/home-url", "https://github.com/kairos-io/kairos"))
			})
		})

		Context("with an older Kairos node (os-release only, no kairos-release)", func() {
			It("returns managed=true and boot-state from os-release", func() {
				dir := GinkgoT().TempDir()
				Expect(os.WriteFile(filepath.Join(dir, "os-release"), []byte("ID=kairos\nNAME=\"Kairos Legacy\"\n"), 0644)).To(Succeed())
				cmdlinePath := writeCmdlineFile("COS_ACTIVE")

				labels, annotations := collectMetadata(dir, cmdlinePath)
				Expect(labels).To(HaveKeyWithValue("kairos.io/managed", "true"))
				Expect(labels).To(HaveKeyWithValue("kairos.io/boot-state", "active"))
				Expect(annotations).To(BeEmpty())
			})
		})

		Context("with a non-Kairos node", func() {
			It("returns empty maps when kairos-release is absent", func() {
				etcDir := writeKairosRelease("")
				cmdlinePath := writeCmdlineFile("root=/dev/sda1")

				labels, annotations := collectMetadata(etcDir, cmdlinePath)
				Expect(labels).To(BeEmpty())
				Expect(annotations).To(BeEmpty())
			})
		})

		DescribeTable("boot state detection",
			func(cmdline, expectedState string) {
				etcDir := writeKairosRelease(sampleKairosRelease)
				cmdlinePath := writeCmdlineFile(cmdline)

				labels, _ := collectMetadata(etcDir, cmdlinePath)
				Expect(labels).To(HaveKeyWithValue("kairos.io/boot-state", expectedState))
			},
			Entry("active boot", "BOOT_IMAGE=/vmlinuz COS_ACTIVE root=LABEL=COS_ACTIVE", "active"),
			Entry("passive boot", "BOOT_IMAGE=/vmlinuz COS_PASSIVE root=LABEL=COS_PASSIVE", "passive"),
			Entry("recovery via COS_RECOVERY", "COS_RECOVERY", "recovery"),
			Entry("recovery via recovery-mode", "recovery-mode", "recovery"),
			Entry("livecd via live:LABEL", "live:LABEL=KAIROS_LIVE", "livecd"),
			Entry("livecd via netboot", "netboot ip=dhcp", "livecd"),
			Entry("unknown", "BOOT_IMAGE=/vmlinuz root=/dev/sda1", "unknown"),
		)
	})

	Describe("syncLabels", func() {
		Context("when metadata changes after an upgrade", func() {
			It("removes stale labels no longer present in kairos-release", func() {
				By("Setting up a node with an old label")
				node := newNode("test-node", map[string]string{
					"kairos.io/managed": "true",
					"kairos.io/family":  "old-family",
					"kairos.io/flavor":  "old-flavor",
					"other.io/label":    "keep-me",
				}, nil)
				clientset := fake.NewSimpleClientset(node)

				releaseWithoutFamily := `KAIROS_ID="kairos"
KAIROS_FLAVOR="new-flavor"
KAIROS_VARIANT="core"
KAIROS_RELEASE="v5.0.0"
KAIROS_MODEL="generic"
KAIROS_TRUSTED_BOOT="false"
KAIROS_FIPS="false"
`
				etcDir := writeKairosRelease(releaseWithoutFamily)
				cmdlinePath := writeCmdlineFile("COS_ACTIVE")

				By("Syncing labels with the new kairos-release")
				Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

				updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())

				Expect(updated.Labels).NotTo(HaveKey("kairos.io/family"))
				Expect(updated.Labels).To(HaveKeyWithValue("kairos.io/flavor", "new-flavor"))
				Expect(updated.Labels).To(HaveKeyWithValue("other.io/label", "keep-me"))
			})

			It("removes stale annotations no longer present in kairos-release", func() {
				By("Setting up a node with old annotations")
				node := newNode("test-node", nil, map[string]string{
					"kairos.io/init-version": "v0.7.0",
					"kairos.io/name":         "old-name",
					"other.io/annotation":    "keep-me",
				})
				clientset := fake.NewSimpleClientset(node)

				releaseWithoutInit := `KAIROS_ID="kairos"
KAIROS_FLAVOR="ubuntu"
KAIROS_VARIANT="core"
KAIROS_RELEASE="v5.0.0"
KAIROS_MODEL="generic"
KAIROS_TRUSTED_BOOT="false"
KAIROS_FIPS="false"
`
				etcDir := writeKairosRelease(releaseWithoutInit)
				cmdlinePath := writeCmdlineFile("COS_ACTIVE")

				By("Syncing labels with the new kairos-release")
				Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

				updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())

				Expect(updated.Annotations).NotTo(HaveKey("kairos.io/init-version"))
				Expect(updated.Annotations).NotTo(HaveKey("kairos.io/name"))
				Expect(updated.Annotations).To(HaveKeyWithValue("other.io/annotation", "keep-me"))
			})
		})

		Context("with a non-Kairos node", func() {
			It("does not touch the node", func() {
				node := newNode("test-node", map[string]string{"existing": "label"}, nil)
				clientset := fake.NewSimpleClientset(node)

				etcDir := writeKairosRelease("")
				cmdlinePath := writeCmdlineFile("root=/dev/sda1")

				Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

				updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(updated.Labels).To(HaveKeyWithValue("existing", "label"))
				Expect(updated.Labels).NotTo(HaveKey("kairos.io/managed"))
			})
		})
	})

	DescribeTable("sanitizeLabelValue",
		func(input, want string) {
			got := sanitizeLabelValue(input)
			Expect(got).To(Equal(want))
			Expect(len(got)).To(BeNumerically("<=", 63))
		},
		Entry("sanitizes + in version strings", "v1.32.4+k3s1", "v1.32.4-k3s1"),
		Entry("leaves valid values unchanged", "v4.0.3", "v4.0.3"),
		Entry("leaves hyphens and dots unchanged", "ubuntu-24.04", "ubuntu-24.04"),
		Entry("trims leading dash", "-leading-dash", "leading-dash"),
		Entry("trims trailing dot", "trailing-dot.", "trailing-dot"),
		Entry("truncates to 63 characters", strings.Repeat("a", 70), strings.Repeat("a", 63)),
		Entry("truncation does not leave trailing hyphen", strings.Repeat("a", 62)+"-a", strings.Repeat("a", 62)),
		Entry("sanitizes all-invalid characters to empty string", strings.Repeat("+", 10), ""),
	)

	Describe("the system UUID label", func() {
		const systemUUID = "4c4c4544-0051-4a10-8056-b4c04f4d3332"

		It("labels the node with the system UUID the kubelet reports", func() {
			node := newNode("test-node", nil, nil)
			node.Status.NodeInfo.SystemUUID = systemUUID
			clientset := fake.NewSimpleClientset(node)

			etcDir := writeKairosRelease(sampleKairosRelease)
			cmdlinePath := writeCmdlineFile("COS_ACTIVE")

			Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

			updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Labels).To(HaveKeyWithValue("kairos.io/system-uuid", systemUUID))
		})

		It("distinguishes two nodes built from the same kairos-release", func() {
			first := newNode("node-a", nil, nil)
			first.Status.NodeInfo.SystemUUID = systemUUID
			second := newNode("node-b", nil, nil)
			second.Status.NodeInfo.SystemUUID = "03000200-0400-0500-0006-000700080009"
			clientset := fake.NewSimpleClientset(first, second)

			etcDir := writeKairosRelease(sampleKairosRelease)
			cmdlinePath := writeCmdlineFile("COS_ACTIVE")

			Expect(syncLabels(context.Background(), clientset, "node-a", etcDir, cmdlinePath)).To(Succeed())
			Expect(syncLabels(context.Background(), clientset, "node-b", etcDir, cmdlinePath)).To(Succeed())

			a, err := clientset.CoreV1().Nodes().Get(context.Background(), "node-a", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			b, err := clientset.CoreV1().Nodes().Get(context.Background(), "node-b", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			Expect(a.Labels["kairos.io/id"]).To(Equal(b.Labels["kairos.io/id"]))
			Expect(a.Labels["kairos.io/system-uuid"]).NotTo(Equal(b.Labels["kairos.io/system-uuid"]))
		})

		It("writes no label when the kubelet reports no system UUID", func() {
			node := newNode("test-node", nil, nil)
			clientset := fake.NewSimpleClientset(node)

			etcDir := writeKairosRelease(sampleKairosRelease)
			cmdlinePath := writeCmdlineFile("COS_ACTIVE")

			Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

			updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Labels).NotTo(HaveKey("kairos.io/system-uuid"))
			Expect(updated.Labels).To(HaveKeyWithValue("kairos.io/managed", "true"))
		})

		It("removes a stale system UUID label the node no longer reports", func() {
			node := newNode("test-node", map[string]string{
				"kairos.io/system-uuid": "stale-uuid",
			}, nil)
			clientset := fake.NewSimpleClientset(node)

			etcDir := writeKairosRelease(sampleKairosRelease)
			cmdlinePath := writeCmdlineFile("COS_ACTIVE")

			Expect(syncLabels(context.Background(), clientset, "test-node", etcDir, cmdlinePath)).To(Succeed())

			updated, err := clientset.CoreV1().Nodes().Get(context.Background(), "test-node", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated.Labels).NotTo(HaveKey("kairos.io/system-uuid"))
		})

		It("sanitizes a system UUID that is not a valid label value", func() {
			labels := withSystemUUID(map[string]string{"kairos.io/managed": "true"}, "Not Settable")
			Expect(labels).To(HaveKeyWithValue("kairos.io/system-uuid", "Not-Settable"))
		})

		It("leaves the caller's map untouched", func() {
			original := map[string]string{"kairos.io/managed": "true"}
			labels := withSystemUUID(original, systemUUID)

			Expect(labels).To(HaveKey("kairos.io/system-uuid"))
			Expect(original).NotTo(HaveKey("kairos.io/system-uuid"))
		})
	})
})
