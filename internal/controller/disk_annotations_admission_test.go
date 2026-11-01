package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	admissionTestNamespace = "default"
	admissionPolicyTimeout = 30 * time.Second
)

var admissionPolicyPath = filepath.Join("..", "..", "config", "admission", "osartifact_disk_annotations.yaml")

// loadDiskAnnotationsPolicy decodes the shipped policy manifest so the test runs exactly what is deployed.
func loadDiskAnnotationsPolicy() (*admissionregistrationv1.ValidatingAdmissionPolicy, *admissionregistrationv1.ValidatingAdmissionPolicyBinding) {
	raw, err := os.ReadFile(admissionPolicyPath)
	Expect(err).ToNot(HaveOccurred())
	docs := strings.Split(string(raw), "\n---\n")
	Expect(docs).To(HaveLen(2))

	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{}
	Expect(yaml.UnmarshalStrict([]byte(docs[0]), policy)).To(Succeed())
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	Expect(yaml.UnmarshalStrict([]byte(docs[1]), binding)).To(Succeed())
	return policy, binding
}

var _ = Describe("OSArtifact disk annotations admission policy", Ordered, func() {
	var (
		ctx     = context.Background()
		policy  *admissionregistrationv1.ValidatingAdmissionPolicy
		binding *admissionregistrationv1.ValidatingAdmissionPolicyBinding
		created []client.Object
	)

	newArtifact := func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec) *buildv1alpha2.OSArtifact {
		return &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "disk-annotations-",
				Namespace:    admissionTestNamespace,
				Annotations:  annotations,
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: "quay.io/kairos/test:latest"},
				Artifacts: artifacts,
			},
		}
	}
	create := func(artifact *buildv1alpha2.OSArtifact) error {
		err := k8sClient.Create(ctx, artifact)
		if err == nil {
			created = append(created, artifact)
		}
		return err
	}
	cloudImage := &buildv1alpha2.ArtifactSpec{CloudImage: true}

	BeforeAll(func() {
		policy, binding = loadDiskAnnotationsPolicy()
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())

		// The apiserver enforces new policies asynchronously, wait until a known-invalid object is denied.
		Eventually(func() error {
			return create(newArtifact(map[string]string{annotationDiskStateSlots: "9"}, cloudImage))
		}, admissionPolicyTimeout, time.Second).Should(MatchError(ContainSubstring(policy.Name)))
	})

	AfterAll(func() {
		for _, obj := range created {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
		}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, binding))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, policy))).To(Succeed())
	})

	// A CEL runtime error would deny these under failurePolicy Fail, so they also guard the expressions themselves.
	DescribeTable("admits valid OSArtifacts",
		func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec) {
			Expect(create(newArtifact(annotations, artifacts))).To(Succeed())
		},
		Entry("no annotations", nil, cloudImage),
		Entry("unrelated annotations", map[string]string{"example.com/foo": "bar", artifactExporterIndexAnnotation: "0"}, cloudImage),
		Entry("boot_active=false on an ISO", map[string]string{annotationDiskBootActive: "false"}, &buildv1alpha2.ArtifactSpec{ISO: true}),
		Entry("boot_active on gce", map[string]string{annotationDiskBootActive: "true"}, &buildv1alpha2.ArtifactSpec{GCEImage: true}),
		Entry("boot_active with 2 slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "2"}, cloudImage),
		Entry("single image without recovery",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "1", annotationDiskNoRecovery: "true"},
			&buildv1alpha2.ArtifactSpec{AzureImage: true}),
	)

	DescribeTable("denies invalid OSArtifacts",
		func(annotations map[string]string, artifacts *buildv1alpha2.ArtifactSpec, wantErr string) {
			err := create(newArtifact(annotations, artifacts))
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "unexpected error: %v", err)
			Expect(err).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("unknown disk key",
			map[string]string{"build.kairos.io/disk.bootactive": "true"}, cloudImage, "unknown build.kairos.io/disk.* annotation"),
		Entry("non bool boot_active",
			map[string]string{annotationDiskBootActive: "yes"}, cloudImage, "boot_active must be"),
		Entry("non bool no_recovery",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "1", annotationDiskNoRecovery: "1"}, cloudImage, "no_recovery must be"),
		Entry("shell injection in state_slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "1' && reboot '"}, cloudImage, "state_slots must be"),
		Entry("state_slots out of range",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "4"}, cloudImage, "state_slots must be"),
		Entry("state_slots without boot_active",
			map[string]string{annotationDiskStateSlots: "1"}, cloudImage, "state_slots requires"),
		Entry("no_recovery without boot_active",
			map[string]string{annotationDiskNoRecovery: "true"}, cloudImage, "no_recovery requires build.kairos.io/disk.boot_active"),
		Entry("no_recovery with default slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskNoRecovery: "true"}, cloudImage, "state_slots=1"),
		Entry("no_recovery with 2 slots",
			map[string]string{annotationDiskBootActive: "true", annotationDiskStateSlots: "2", annotationDiskNoRecovery: "true"}, cloudImage, "state_slots=1"),
		Entry("boot_active on an ISO only",
			map[string]string{annotationDiskBootActive: "true"}, &buildv1alpha2.ArtifactSpec{ISO: true}, "requires at least one of"),
	)

	It("denies an update that adds an invalid annotation", func() {
		artifact := newArtifact(nil, cloudImage)
		Expect(create(artifact)).To(Succeed())

		artifact.Annotations = map[string]string{annotationDiskNoRecovery: "true"}
		err := k8sClient.Update(ctx, artifact)
		Expect(err).To(MatchError(ContainSubstring("no_recovery requires")))
	})
})
