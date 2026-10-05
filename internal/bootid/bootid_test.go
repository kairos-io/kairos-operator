package bootid_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/kairos-io/kairos-operator/internal/bootid"
)

const (
	idA = "0123abcd-4567-89ab-cdef-0123456789ab"
	idB = "fedcba98-7654-3210-fedc-ba9876543210"
)

func pod(phase corev1.PodPhase, container, message string) corev1.Pod {
	return corev1.Pod{
		Status: corev1.PodStatus{
			Phase: phase,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: container,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Message: message},
				},
			}},
		},
	}
}

var _ = Describe("Valid", func() {
	DescribeTable("accepts only the canonical 8-4-4-4-12 hex form",
		func(in string, want bool) {
			Expect(bootid.Valid(in)).To(Equal(want))
		},
		Entry("lowercase", idA, true),
		Entry("uppercase", "0123ABCD-4567-89AB-CDEF-0123456789AB", true),
		Entry("empty", "", false),
		Entry("unhyphenated", "0123abcd456789abcdef0123456789ab", false),
		Entry("braces", "{"+idA+"}", false),
		Entry("urn prefix", "urn:uuid:"+idA, false),
		Entry("non-hex", "0123abcd-4567-89ab-cdef-0123456789az", false),
		Entry("trailing newline", idA+"\n", false),
		Entry("too short", "0123abcd-4567-89ab-cdef-0123456789a", false),
	)
})

var _ = Describe("FromJobPods", func() {
	DescribeTable("returns the boot ID reported by a succeeded Pod of the upgrade Job",
		func(pods []corev1.Pod, want string) {
			Expect(bootid.FromJobPods(pods)).To(Equal(want))
		},
		Entry("no Pods", nil, ""),
		Entry("no Succeeded Pod",
			[]corev1.Pod{pod(corev1.PodRunning, bootid.ReporterContainerName, idA)}, ""),
		Entry("Succeeded with empty message",
			[]corev1.Pod{pod(corev1.PodSucceeded, bootid.ReporterContainerName, "")}, ""),
		Entry("Succeeded with invalid message",
			[]corev1.Pod{pod(corev1.PodSucceeded, bootid.ReporterContainerName, "not-a-boot-id")}, ""),
		Entry("Failed Pod with valid message is ignored",
			[]corev1.Pod{pod(corev1.PodFailed, bootid.ReporterContainerName, idA)}, ""),
		Entry("only the Succeeded Pod counts",
			[]corev1.Pod{
				pod(corev1.PodFailed, bootid.ReporterContainerName, idA),
				pod(corev1.PodSucceeded, bootid.ReporterContainerName, idB),
			}, idB),
		Entry("first Succeeded Pod with a valid message wins",
			[]corev1.Pod{
				pod(corev1.PodSucceeded, bootid.ReporterContainerName, "garbage"),
				pod(corev1.PodSucceeded, bootid.ReporterContainerName, idA),
				pod(corev1.PodSucceeded, bootid.ReporterContainerName, idB),
			}, idA),
		Entry("message is trimmed",
			[]corev1.Pod{pod(corev1.PodSucceeded, bootid.ReporterContainerName, idA+"\n")}, idA),
		Entry("wrong container name",
			[]corev1.Pod{pod(corev1.PodSucceeded, "nodeop", idA)}, ""),
	)

	It("ignores a container that has not terminated", func() {
		p := corev1.Pod{Status: corev1.PodStatus{
			Phase:             corev1.PodSucceeded,
			ContainerStatuses: []corev1.ContainerStatus{{Name: bootid.ReporterContainerName}},
		}}
		Expect(bootid.FromJobPods([]corev1.Pod{p})).To(BeEmpty())
	})
})

var _ = Describe("ReadHost", func() {
	It("returns either empty or a valid boot ID", func() {
		got := bootid.ReadHost()
		if got != "" {
			Expect(bootid.Valid(got)).To(BeTrue())
		}
	})
})
