package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// A NodeOpUpgrade hands its whole spec to the NodeOp it creates, once, and
// never looks at the spec again. Editing it after that is therefore a silent
// no-op: the rollout keeps running the old image and the phase still reports
// the old NodeOp, so the edit reads as applied when it is not. The API server
// has to refuse the edit instead.
var _ = Describe("NodeOpUpgrade spec immutability", func() {
	var name string

	newUpgrade := func(image string) *kairosiov1alpha1.NodeOpUpgrade {
		return &kairosiov1alpha1.NodeOpUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
				Image:        image,
				Concurrency:  1,
				ExcludePaths: []string{"/var/lib/rancher"},
			},
		}
	}

	get := func() *kairosiov1alpha1.NodeOpUpgrade {
		fetched := &kairosiov1alpha1.NodeOpUpgrade{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, fetched)).To(Succeed())
		return fetched
	}

	BeforeEach(func() {
		name = "immutable-" + randStringRunes(8)
		Expect(k8sClient.Create(ctx, newUpgrade("quay.io/kairos/core:v1"))).To(Succeed())
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &kairosiov1alpha1.NodeOpUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		})
	})

	It("refuses a change to the image instead of dropping it", func() {
		fetched := get()
		fetched.Spec.Image = "quay.io/kairos/core:v2"

		err := k8sClient.Update(ctx, fetched)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected an Invalid error, got %v", err)
		Expect(err.Error()).To(ContainSubstring("immutable"))

		Expect(get().Spec.Image).To(Equal("quay.io/kairos/core:v1"))
	})

	It("refuses a change to any other spec field", func() {
		for _, mutate := range []func(*kairosiov1alpha1.NodeOpUpgrade){
			func(u *kairosiov1alpha1.NodeOpUpgrade) { u.Spec.Concurrency = 5 },
			func(u *kairosiov1alpha1.NodeOpUpgrade) { u.Spec.UpgradeActive = asBool(false) },
			func(u *kairosiov1alpha1.NodeOpUpgrade) { u.Spec.ExcludePaths = []string{"/opt"} },
			func(u *kairosiov1alpha1.NodeOpUpgrade) {
				u.Spec.NodeSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}
			},
		} {
			fetched := get()
			mutate(fetched)
			Expect(k8sClient.Update(ctx, fetched)).NotTo(Succeed())
		}
	})

	It("still allows an update that leaves the spec alone", func() {
		fetched := get()
		fetched.Labels = map[string]string{"team": "kairos"}
		fetched.Annotations = map[string]string{"note": "still editable"}
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())
	})

	It("still allows the controller to write status", func() {
		fetched := get()
		fetched.Status.Phase = phaseInitializing
		fetched.Status.NodeOpName = name
		fetched.Status.Message = "NodeOp created"
		fetched.Status.LastUpdated = metav1.Now()
		Expect(k8sClient.Status().Update(ctx, fetched)).To(Succeed())

		Expect(get().Status.NodeOpName).To(Equal(name))
	})
})
