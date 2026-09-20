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
	It("leaves a short enough name alone", func() {
		name := "kairos-upgrade-worker-1"
		Expect(utils.TruncateNameWithHash(name, jobNameLimit)).To(Equal(name))
	})

	It("truncates a long name and stays within the limit", func() {
		name := "kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal"
		got := utils.TruncateNameWithHash(name, jobNameLimit)
		Expect(len(got)).To(BeNumerically("<=", jobNameLimit))
		Expect(got).ToNot(Equal(name))
	})

	It("gives different names to different inputs", func() {
		a := utils.TruncateNameWithHash("kairos-upgrade-ip-10-0-15-201.eu-central-1.compute.internal", jobNameLimit)
		b := utils.TruncateNameWithHash("kairos-upgrade-ip-10-0-15-202.eu-central-1.compute.internal", jobNameLimit)
		Expect(a).ToNot(Equal(b))
	})

	// %x drops the leading zeros of the uint64 FNV sum, so these names hash to
	// fewer than 12 hex characters and used to panic on the [:12] slice.
	DescribeTable("keeps a full width hash when the hash is small",
		func(name string) {
			got := utils.TruncateNameWithHash(name, jobNameLimit)
			Expect(len(got)).To(BeNumerically("<=", jobNameLimit))
			Expect(got).To(MatchRegexp(`-[0-9a-f]{12}$`))
		},
		Entry("ip-10-27-39-167", "kairos-upgrade-ip-10-27-39-167.eu-west-1.compute.internal"),
		Entry("ip-10-29-65-104", "kairos-upgrade-ip-10-29-65-104.eu-west-1.compute.internal"),
		Entry("ip-10-39-37-195", "kairos-upgrade-ip-10-39-37-195.eu-west-1.compute.internal"),
		Entry("ip-10-69-245-77", "kairos-upgrade-ip-10-69-245-77.eu-west-1.compute.internal"),
		Entry("ip-10-79-40-157", "kairos-upgrade-ip-10-79-40-157.eu-west-1.compute.internal"),
	)

	// The cut lands on the "." after the region, and a label may not start
	// with the hyphen that follows it.
	It("does not end the prefix on a separator", func() {
		name := "os-upgrade-v372-ip-172-31-255-254.us-east-2.compute.internal"
		got := utils.TruncateNameWithHash(name, jobNameLimit)
		Expect(got).To(MatchRegexp(dns1123Subdomain.String()))
	})

	It("returns a valid resource name for every offset of a hostname", func() {
		for i := 1; i < 40; i++ {
			name := "os-upgrade-" + repeat("a", i) + ".eu-west-1.compute.internal"
			got := utils.TruncateNameWithHash(name, jobNameLimit)
			Expect(len(got)).To(BeNumerically("<=", jobNameLimit), "name %q", name)
			Expect(dns1123Subdomain.MatchString(got)).To(BeTrue(), "name %q became %q", name, got)
		}
	})
})

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
