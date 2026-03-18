package controller

import (
	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NodeOpUpgrade spec immutability", func() {
	var namespace string
	var nodeOpUpgradeName string
	var clientset *kubernetes.Clientset

	BeforeEach(func() {
		var err error
		clientset, err = kubernetes.NewForConfig(cfg)
		Expect(err).ToNot(HaveOccurred())
		namespace = createRandomNamespace(clientset)

		nodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "immutable-spec-test",
				Namespace: namespace,
			},
			Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
				Image: "quay.io/kairos/core:latest",
			},
		}
		Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())
		nodeOpUpgradeName = nodeOpUpgrade.Name
	})

	AfterEach(func() {
		// NodeOpUpgrade has no finalizer; delete it. Also delete any NodeOp created by the controller (same name/ns).
		_ = k8sClient.Delete(ctx, &kairosiov1alpha1.NodeOpUpgrade{
			ObjectMeta: metav1.ObjectMeta{Name: nodeOpUpgradeName, Namespace: namespace},
		})
		_ = k8sClient.Delete(ctx, &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: nodeOpUpgradeName, Namespace: namespace},
		})
	})

	It("rejects updates to spec", func() {
		nodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeOpUpgradeName, Namespace: namespace}, nodeOpUpgrade)).To(Succeed())
		nodeOpUpgrade.Spec.Image = "quay.io/other:tag"
		err := k8sClient.Update(ctx, nodeOpUpgrade)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})
})
