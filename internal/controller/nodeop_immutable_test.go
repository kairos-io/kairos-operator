package controller

import (
	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var _ = Describe("NodeOp spec immutability", func() {
	var namespace string
	var nodeOpName string
	var clientset *kubernetes.Clientset

	BeforeEach(func() {
		var err error
		clientset, err = kubernetes.NewForConfig(cfg)
		Expect(err).ToNot(HaveOccurred())
		namespace = createRandomNamespace(clientset)

		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "immutable-spec-test",
				Namespace: namespace,
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command: []string{"echo", "test"},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
		nodeOpName = nodeOp.Name
	})

	AfterEach(func() {
		// envtest does not run the namespace controller; remove finalizer and delete NodeOp.
		obj := &kairosiov1alpha1.NodeOp{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeOpName, Namespace: namespace}, obj); err != nil {
			return
		}
		original := obj.DeepCopy()
		controllerutil.RemoveFinalizer(obj, clusterRoleBindingFinalizer)
		_ = k8sClient.Patch(ctx, obj, client.MergeFrom(original))
		_ = k8sClient.Delete(ctx, obj)
	})

	It("rejects updates to spec", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeOpName, Namespace: namespace}, nodeOp)).To(Succeed())
		nodeOp.Spec.Command = []string{"echo", "modified"}
		err := k8sClient.Update(ctx, nodeOp)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})
})
