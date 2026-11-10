package controller

import (
	"context"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("OSArtifact ConfigMap and PVC ownership", func() {
	var artifact *buildv1alpha2.OSArtifact

	newReconciler := func(objs ...client.Object) *OSArtifactReconciler {
		cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
		return &OSArtifactReconciler{Client: cl, Scheme: scheme.Scheme}
	}

	ownerRefTo := func(a *buildv1alpha2.OSArtifact) []metav1.OwnerReference {
		return []metav1.OwnerReference{{
			APIVersion: buildv1alpha2.GroupVersion.String(),
			Kind:       "OSArtifact",
			Name:       a.Name,
			UID:        a.UID,
		}}
	}

	BeforeEach(func() {
		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "my-artifact",
				Namespace: "default",
				UID:       types.UID("11111111-1111-1111-1111-111111111111"),
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Artifacts: &buildv1alpha2.ArtifactSpec{OSRelease: "ID=kairos"},
			},
		}
	})

	Describe("CreateConfigMap", func() {
		It("refuses a ConfigMap with the same name that has no owner", func() {
			foreign := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: artifact.Name, Namespace: artifact.Namespace},
				Data:       map[string]string{"app.conf": "user data"},
			}
			r := newReconciler(foreign)

			err := r.CreateConfigMap(context.Background(), artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))

			got := &corev1.ConfigMap{}
			Expect(r.Get(context.Background(), client.ObjectKeyFromObject(foreign), got)).To(Succeed())
			Expect(got.Data).To(Equal(map[string]string{"app.conf": "user data"}))
		})

		It("refuses a ConfigMap owned by another OSArtifact", func() {
			other := artifact.DeepCopy()
			other.Name = "other-artifact"
			other.UID = types.UID("22222222-2222-2222-2222-222222222222")
			foreign := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            artifact.Name,
					Namespace:       artifact.Namespace,
					OwnerReferences: ownerRefTo(other),
				},
			}
			r := newReconciler(foreign)

			err := r.CreateConfigMap(context.Background(), artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))
		})

		It("accepts a ConfigMap this OSArtifact already owns", func() {
			r := newReconciler()
			Expect(r.CreateConfigMap(context.Background(), artifact)).To(Succeed())
			Expect(r.CreateConfigMap(context.Background(), artifact)).To(Succeed())
		})
	})

	Describe("createPVC", func() {
		It("refuses a PVC with the same name that has no owner", func() {
			foreign := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: artifact.Name + "-artifacts", Namespace: artifact.Namespace},
			}
			r := newReconciler(foreign)

			_, err := r.createPVC(context.Background(), artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))
		})

		It("refuses a PVC owned by another OSArtifact", func() {
			other := artifact.DeepCopy()
			other.Name = "other-artifact"
			other.UID = types.UID("22222222-2222-2222-2222-222222222222")
			foreign := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:            artifact.Name + "-artifacts",
					Namespace:       artifact.Namespace,
					OwnerReferences: ownerRefTo(other),
				},
			}
			r := newReconciler(foreign)

			_, err := r.createPVC(context.Background(), artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))
		})

		It("returns the existing PVC this OSArtifact already owns", func() {
			r := newReconciler()
			first, err := r.createPVC(context.Background(), artifact)
			Expect(err).ToNot(HaveOccurred())

			second, err := r.createPVC(context.Background(), artifact)
			Expect(err).ToNot(HaveOccurred())
			Expect(second.Name).To(Equal(first.Name))
		})
	})
})
