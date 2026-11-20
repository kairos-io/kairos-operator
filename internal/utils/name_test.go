package utils_test

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kairos-io/kairos-operator/internal/utils"
)

// dns1123Subdomain is the name format the API server enforces on Jobs and Pods.
var dns1123Subdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// jobNameLimit is what the NodeOp controller asks for: the Kubernetes limit
// less the six characters it appends as a per-run suffix.
const jobNameLimit = utils.KubernetesNameLengthLimit - 6

var _ = Describe("TruncateNameWithHash", func() {
	DescribeTable("builds the name",
		func(name string, maxLength int, expected string) {
			got := utils.TruncateNameWithHash(name, maxLength)
			Expect(got).To(Equal(expected))
			Expect(len(got)).To(BeNumerically("<=", maxLength))
			Expect(dns1123Subdomain.MatchString(got)).To(BeTrue(), "%q is not a valid resource name", got)
		},

		Entry("leaves a short enough name alone",
			"kairos-upgrade-worker-1", jobNameLimit,
			"kairos-upgrade-worker-1"),

		Entry("truncates a long name and appends the hash",
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal", jobNameLimit,
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.c-13c26f919108"),

		Entry("cuts at the limit it is given, not at the Kubernetes one",
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal", utils.KubernetesNameLengthLimit,
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.compute-13c26f919108"),

		// Two hostnames that differ only in the last octet must not collide.
		Entry("hashes a neighbouring node to a different name",
			"kairos-upgrade-ip-10-0-15-202.eu-central-1.compute.internal", jobNameLimit,
			"kairos-upgrade-ip-10-0-15-202.eu-central-1.c-74e6c23c951d"),

		// %x drops the leading zeros of the uint64 FNV sum, so these names used
		// to render fewer than 12 hex characters and panic on the [:12] slice.
		Entry("pads a hash below 2^60 to a full width",
			"kairos-upgrade-ip-10-27-39-167.eu-west-1.compute.internal", jobNameLimit,
			"kairos-upgrade-ip-10-27-39-167.eu-west-1.com-00000b36be95"),
		Entry("pads a hash below 2^48 to a full width",
			"kairos-upgrade-ip-10-39-37-195.eu-west-1.compute.internal", jobNameLimit,
			"kairos-upgrade-ip-10-39-37-195.eu-west-1.com-0000021b1316"),
		Entry("pads a hash below 2^44 to a full width",
			"kairos-upgrade-ip-10-69-245-77.eu-west-1.compute.internal", jobNameLimit,
			"kairos-upgrade-ip-10-69-245-77.eu-west-1.com-000006cf897d"),

		// A label may not start with the separator the cut would leave behind,
		// so the trailing "." or "-" goes before the hash is appended.
		Entry("drops a trailing dot left by the cut",
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.eu-west-1.compute.internal", jobNameLimit,
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-10eedb7039b5"),
		Entry("drops a trailing hyphen left by the cut",
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaa.eu-west-1.compute.internal", jobNameLimit,
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaa.eu-west-d99be9916da1"),
		Entry("keeps a cut that lands on a normal character",
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.eu-west-1.compute.internal", jobNameLimit,
			"os-upgrade-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-0cf68d652209"),

		// Nothing is left of the prefix once the separators are trimmed.
		Entry("returns the bare hash when the prefix trims away",
			"--------------------------------------------------", jobNameLimit,
			"c71fded56eae"),
	)
})

// labelValue is the format the API server enforces on label values.
var labelValue = regexp.MustCompile(`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`)

var _ = Describe("TruncateLabelValue", func() {
	DescribeTable("builds the value",
		func(name string, expected string) {
			got := utils.TruncateLabelValue(name)
			Expect(got).To(Equal(expected))
			Expect(len(got)).To(BeNumerically("<=", utils.KubernetesLabelValueLengthLimit))
			Expect(labelValue.MatchString(got)).To(BeTrue(), "%q is not a valid label value", got)
		},

		Entry("leaves a name that already fits alone",
			"kairos-upgrade-worker-1",
			"kairos-upgrade-worker-1"),

		// TruncateNameWithHash on its own would hash this one, because it
		// reserves 13 characters for the suffix. A label value of exactly 63
		// characters is valid, so it has to come back untouched.
		Entry("leaves a name exactly at the limit alone",
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal-abc",
			"kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal-abc"),

		// A NodeOp named after the image it rolls out goes past 63 characters
		// easily, and the API server accepts it: object names are capped at
		// 253, not 63.
		Entry("truncates a name only an object name may be",
			"upgrade-to-hadron-ubuntu-24-core-amd64-generic-v3.5.2-2026092301",
			"upgrade-to-hadron-ubuntu-24-core-amd64-generic-v3-814c1864c23f"),

		// Two NodeOps whose names share the first 49 characters must not end
		// up under the same label value.
		Entry("keeps two names that share a long prefix apart",
			"upgrade-to-hadron-ubuntu-24-core-amd64-generic-v3.5.2-2026-09-23-nightly",
			"upgrade-to-hadron-ubuntu-24-core-amd64-generic-v3-6753fa2d3b6b"),
	)
})
