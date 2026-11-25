package controller

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Semver image policy", func() {
	DescribeTable("selects the highest eligible upgrade", func(expression, current string, tags []string, expected string) {
		policy, err := newSemverImagePolicy(expression)
		Expect(err).NotTo(HaveOccurred())
		original := slices.Clone(tags)
		selected, err := policy.selectUpgrade(tags, current)
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal(expected))
		Expect(tags).To(Equal(original))
	},
		Entry("numeric ordering", ">=3.6.2 <3.7.0", "3.6.2", []string{"3.6.3", "3.6.10", "3.6.9"}, "3.6.10"),
		Entry("preserves the original v prefix", ">=3.6.2 <3.7.0", "v3.6.2", []string{"3.6.3", "v3.6.4"}, "v3.6.4"),
		Entry("patch range", "~3.6.2", "3.6.2", []string{"3.6.3", "3.7.0", "4.0.0"}, "3.6.3"),
		Entry("minor range", "^3.6.2", "3.6.2", []string{"3.6.3", "3.7.0", "4.0.0"}, "3.7.0"),
		Entry("major upgrades", ">=3.6.2", "3.6.2", []string{"3.7.0", "4.0.0"}, "4.0.0"),
		Entry("exact target", "=3.6.3", "3.6.2", []string{"3.6.3", "3.6.4"}, "3.6.3"),
		Entry("current need not match the range", ">=3.7.0 <4.0.0", "3.6.2", []string{"3.7.0", "4.0.0"}, "3.7.0"),
		Entry("equal and older versions", ">=3.0.0", "3.6.2", []string{"3.5.9", "v3.6.2", "3.6.2"}, ""),
		Entry("an exact older target cannot downgrade", "=3.6.1", "3.6.2", []string{"3.6.1"}, ""),
		Entry("no matching versions", ">=3.6.2 <3.7.0", "3.6.2", []string{"3.7.0", "4.0.0"}, ""),
		Entry("an empty listing", ">=3.6.2", "3.6.2", nil, ""),
		Entry("invalid candidates are ignored", ">=3.6.2", "3.6.2", []string{
			"latest", "", "3", "3.7", "v4.0", "03.7.0", "3.07.0", "3.7.00",
			"vv4.0.0", "V4.0.0", " 4.0.0", "4.0.0 ", "4.0.0-rc.01", "4.0.0+", "v3.6.3",
		}, "v3.6.3"),
		Entry("prereleases excluded by default", ">=3.6.2 <3.7.0", "3.6.2", []string{"3.6.3", "3.6.4-rc.1"}, "3.6.3"),
		Entry("only prereleases available", ">=3.6.2 <3.7.0", "3.6.2", []string{"3.6.3-rc.1"}, ""),
		Entry("prereleases explicitly enabled", ">=3.6.2-0 <3.7.0", "3.6.2", []string{"3.6.3", "v3.6.4-rc.1"}, "v3.6.4-rc.1"),
		Entry("numeric prerelease ordering", ">=3.6.3-0 <3.7.0", "3.6.3-rc.1", []string{"3.6.3-rc.2", "3.6.3-rc.10"}, "3.6.3-rc.10"),
		Entry("stable supersedes prerelease", ">=3.6.3-0 <3.7.0", "v3.6.3-rc.1", []string{"3.6.3-rc.2", "3.6.3"}, "3.6.3"),
		Entry("metadata cannot trigger an upgrade", ">=3.6.2", "3.6.2+build.1", []string{"3.6.2+build.2"}, ""),
		Entry("equal candidate versions use tag ordering", ">=3.6.2", "3.6.2", []string{"v3.6.3", "3.6.3"}, "3.6.3"),
		Entry("equal candidate versions in reverse order", ">=3.6.2", "3.6.2", []string{"3.6.3", "v3.6.3"}, "3.6.3"),
		Entry("duplicate tags", ">=3.6.2", "3.6.2", []string{"v3.6.3", "v3.6.3"}, "v3.6.3"),
		Entry("disjoint ranges", "~3.6.2 || >=4.0.0 <4.1.0", "3.6.2", []string{"3.6.3", "3.7.0", "4.0.1", "4.1.0"}, "4.0.1"),
	)

	DescribeTable("rejects invalid constraints", func(expression string) {
		policy, err := newSemverImagePolicy(expression)
		Expect(err).To(MatchError(ContainSubstring("semver constraint")))
		Expect(policy).To(BeNil())
	},
		Entry("empty", ""),
		Entry("whitespace", " \t "),
		Entry("non-version", "latest"),
		Entry("invalid comparator", ">=>3.6.2"),
	)

	DescribeTable("rejects an unknown or invalid current version", func(current string) {
		policy, err := newSemverImagePolicy(">=3.6.2")
		Expect(err).NotTo(HaveOccurred())
		selected, err := policy.selectUpgrade([]string{"4.0.0"}, current)
		Expect(err).To(MatchError(ContainSubstring("invalid current version tag")))
		Expect(selected).To(BeEmpty())
	},
		Entry("unknown", ""),
		Entry("mutable tag", "latest"),
		Entry("incomplete version", "3.6"),
		Entry("leading zero", "03.6.2"),
		Entry("image reference", "example.com/kairos:v3.6.2"),
		Entry("digest", "sha256:abcdef"),
	)
})
