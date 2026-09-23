package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	"github.com/kairos-io/kairos-operator/internal/utils"
)

var _ = Describe("OSArtifact child objects, long artifact name", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var namespace *corev1.Namespace
	// 70 characters: valid as an OSArtifact name (a DNS subdomain, 253 max),
	// too long for a label value (63 max).
	longName := "kairos-opensuse-leap-15-6-standard-amd64-generic-v3-6-0-uki-signed-abcd"

	BeforeEach(func() {
		Expect(len(longName)).To(BeNumerically(">", utils.KubernetesNameLengthLimit))

		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("osa-long-%s", randStringRunes(8))},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: longName, Namespace: namespace.Name},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: testImageName},
				Artifacts: &buildv1alpha2.ArtifactSpec{},
			},
		}
		Expect(k8sClient.Create(ctx, artifact)).To(Succeed())

		r = &OSArtifactReconciler{Client: k8sClient, Scheme: scheme.Scheme, ToolImage: "tool-image"}
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
	})

	It("creates the ConfigMap", func() {
		Expect(r.CreateConfigMap(ctx, artifact)).To(Succeed())

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: artifact.Name, Namespace: namespace.Name}, cm)).To(Succeed())
		Expect(cm.Labels).To(HaveKey(artifactLabel))
		Expect(len(cm.Labels[artifactLabel])).To(BeNumerically("<=", utils.KubernetesNameLengthLimit))
	})

	It("creates the artifacts PVC", func() {
		_, err := r.createPVC(ctx, artifact)
		Expect(err).ToNot(HaveOccurred())
	})

	It("maps a child object back to the OSArtifact that owns it", func() {
		Expect(r.CreateConfigMap(ctx, artifact)).To(Succeed())

		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: artifact.Name, Namespace: namespace.Name}, cm)).To(Succeed())

		requests := r.findOwningArtifact(ctx, cm)
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Name).To(Equal(artifact.Name))
		Expect(requests[0].Namespace).To(Equal(namespace.Name))
	})

	It("finds its own builder Pod again through the label selector", func() {
		pvc, err := r.createPVC(ctx, artifact)
		Expect(err).ToNot(HaveOccurred())
		_, err = r.createBuilderPod(ctx, artifact, pvc)
		Expect(err).ToNot(HaveOccurred())

		// checkBuild lists by the same label the create path writes; if the two
		// ever disagree the build is restarted on every reconcile.
		_, err = r.checkBuild(ctx, artifact)
		Expect(err).ToNot(HaveOccurred())

		fresh := &buildv1alpha2.OSArtifact{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(artifact), fresh)).To(Succeed())
		pods := &corev1.PodList{}
		Expect(k8sClient.List(ctx, pods, client.InNamespace(namespace.Name))).To(Succeed())
		Expect(pods.Items).To(HaveLen(1))
	})
})

var _ = Describe("OSArtifact export Job, name too long for the Job label", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var namespace *corev1.Namespace
	// 56 characters: short enough for a label value, too long once "-export-0"
	// is appended and the Job controller copies the name into a pod label.
	mediumName := "kairos-opensuse-leap-15-6-standard-amd64-generic-v3-6-0a"

	BeforeEach(func() {
		Expect(len(mediumName)).To(BeNumerically("<=", utils.KubernetesNameLengthLimit))
		Expect(len(mediumName + "-export-0")).To(BeNumerically(">", utils.KubernetesNameLengthLimit))

		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("osa-exp-%s", randStringRunes(8))},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: mediumName, Namespace: namespace.Name},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: testImageName},
				Artifacts: &buildv1alpha2.ArtifactSpec{},
			},
		}
		Expect(k8sClient.Create(ctx, artifact)).To(Succeed())

		r = &OSArtifactReconciler{Client: k8sClient, Scheme: scheme.Scheme, ToolImage: "tool-image"}
	})

	AfterEach(func() {
		Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
	})

	It("creates an export Job the API server accepts", func() {
		// The Job controller stamps the Job name into spec.template.labels, so an export
		// Job name over 63 characters is rejected outright. That bites at 55 characters of
		// artifact name, well before the artifactLabel value does.
		artifact.Spec.Volumes = []corev1.Volume{{
			Name:         "artifacts",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		}}
		artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{Volume: "artifacts"}
		artifact.Spec.Exporters = []batchv1.JobSpec{{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers:    []corev1.Container{{Name: "export", Image: "busybox"}},
			}},
		}}

		_, err := r.checkExport(ctx, artifact)
		Expect(err).ToNot(HaveOccurred())

		jobs := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobs, client.InNamespace(namespace.Name))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		Expect(len(jobs.Items[0].Name)).To(BeNumerically("<=", utils.KubernetesNameLengthLimit))

		// A second pass finds the Job it already made through the index annotation
		// rather than creating a duplicate.
		_, err = r.checkExport(ctx, artifact)
		Expect(err).ToNot(HaveOccurred())
		Expect(k8sClient.List(ctx, jobs, client.InNamespace(namespace.Name))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
	})
})
