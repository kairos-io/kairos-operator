package controller

import (
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("diskOptionsFor", func() {
	newArtifact := func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec) *buildv1alpha2.OSArtifact {
		return &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "a", Annotations: annotations},
			Spec:       buildv1alpha2.OSArtifactSpec{Artifacts: artifacts},
		}
	}
	cloudImage := &buildv1alpha2.ArtifactSpec{CloudImage: true}

	DescribeTable("accepts valid annotations",
		func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec, want diskOptions, wantArgs string) {
			opts, err := diskOptionsFor(newArtifact(annotations, artifacts))
			Expect(err).ToNot(HaveOccurred())
			Expect(opts).To(Equal(want))
			Expect(opts.args()).To(Equal(wantArgs))
		},
		Entry("no annotations", nil, nil, diskOptions{}, ""),
		Entry("unrelated build.kairos.io annotation",
			map[string]string{artifactExporterIndexAnnotation: "0"}, nil, diskOptions{}, ""),
		Entry("boot_active on azure",
			map[string]string{annotationDiskBootActive: "true"},
			&buildv1alpha2.ArtifactSpec{AzureImage: true},
			diskOptions{BootActive: true},
			" --set 'disk.boot_active=true'"),
		Entry("boot_active with 2 slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "2"},
			cloudImage,
			diskOptions{BootActive: true, StateSlots: 2},
			" --set 'disk.boot_active=true' --set 'disk.state_slots=2'"),
		Entry("single image without recovery",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "1", annotationDiskNoRecovery: "true"},
			cloudImage,
			diskOptions{BootActive: true, StateSlots: 1, NoRecovery: true},
			" --set 'disk.boot_active=true' --set 'disk.state_slots=1' --set 'disk.no_recovery=true'"),
	)

	DescribeTable("rejects invalid annotations",
		func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec, wantErr string) {
			_, err := diskOptionsFor(newArtifact(annotations, artifacts))
			Expect(err).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("unknown disk key",
			map[string]string{"build.kairos.io/disk.bootactive": "true"}, cloudImage, "unknown annotation"),
		Entry("non bool boot_active",
			map[string]string{annotationDiskBootActive: "yes please"}, cloudImage, "invalid annotation"),
		Entry("shell injection in state_slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "1' && reboot '"}, cloudImage, "invalid annotation"),
		Entry("state_slots out of range",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "4"}, cloudImage, "between 1 and 3"),
		Entry("state_slots without boot_active",
			map[string]string{annotationDiskStateSlots: "1"}, cloudImage, "requires "+annotationDiskBootActive),
		Entry("no_recovery without boot_active",
			map[string]string{annotationDiskNoRecovery: "true"}, cloudImage, "requires "+annotationDiskBootActive),
		Entry("no_recovery with default slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskNoRecovery: "true"}, cloudImage, "state_slots=1"),
		Entry("boot_active without a raw disk artifact",
			map[string]string{annotationDiskBootActive: "true"}, &buildv1alpha2.ArtifactSpec{ISO: true}, "requires at least one of"),
		Entry("boot_active without artifacts",
			map[string]string{annotationDiskBootActive: "true"}, nil, "requires at least one of"),
	)
})
