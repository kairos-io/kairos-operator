package controller

import (
	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var _ = Describe("OSArtifact spec immutability", func() {
	var namespace string
	var artifactName string
	var clientset *kubernetes.Clientset

	BeforeEach(func() {
		var err error
		clientset, err = kubernetes.NewForConfig(cfg)
		Expect(err).ToNot(HaveOccurred())
		namespace = createRandomNamespace(clientset)

		artifact := &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "immutable-spec-test",
				Namespace: namespace,
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: "quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0",
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{ISO: true},
			},
		}
		Expect(k8sClient.Create(ctx, artifact)).To(Succeed())
		artifactName = artifact.Name
	})

	AfterEach(func() {
		// envtest runs only etcd + kube-apiserver; it does not run kube-controller-manager.
		// The namespace controller (which removes the namespace's "kubernetes" finalizer after
		// emptying the namespace) is not running, so namespaces never leave Terminating if we
		// delete them. Just remove the OSArtifact's finalizer and delete it; the namespace is
		// left behind but testEnv.Stop() in AfterSuite tears down the whole environment.
		artifact := &buildv1alpha2.OSArtifact{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: artifactName, Namespace: namespace}, artifact); err != nil {
			return // already gone or not found
		}
		original := artifact.DeepCopy()
		controllerutil.RemoveFinalizer(artifact, FinalizerName)
		_ = k8sClient.Patch(ctx, artifact, client.MergeFrom(original))
		_ = k8sClient.Delete(ctx, artifact)
	})

	It("rejects updates to spec", func() {
		artifact := &buildv1alpha2.OSArtifact{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: artifactName, Namespace: namespace}, artifact)).To(Succeed())
		artifact.Spec.Image.Ref = "quay.io/other:tag"
		err := k8sClient.Update(ctx, artifact)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})
})
