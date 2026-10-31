package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// A node that leaves the target set before its upgrade starts must give its
// concurrency slot back, or with Concurrency=1 no other node ever starts.
var _ = Describe("NodeOp Controller - node no longer targeted", func() {
	const selectorKey = "kairos.io/untargeted-test"

	var (
		ctx          context.Context
		resourceName string
		nodes        map[string]*corev1.Node
		reconciler   *NodeOpReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())
		uniq := fmt.Sprintf("%d", time.Now().UnixNano())
		resourceName = "untargeted-" + uniq
		nodes = map[string]*corev1.Node{}
		for _, name := range []string{"ut-a-" + uniq, "ut-b-" + uniq} {
			n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{selectorKey: resourceName},
			}}
			Expect(k8sClient.Create(ctx, n)).To(Succeed())
			nodes[name] = n
		}
		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	})

	AfterEach(func() {
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())
		nodeOp := &kairosiov1alpha1.NodeOp{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"}}
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, nodeOp))).To(Succeed())
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList, client.InNamespace("default"),
			client.MatchingLabels{labelKeyNodeOp: resourceName})).To(Succeed())
		for i := range podList.Items {
			Expect(client.IgnoreNotFound(
				k8sClient.Delete(ctx, &podList.Items[i], client.GracePeriodSeconds(0)),
			)).To(Succeed())
		}
		for _, n := range nodes {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, n))).To(Succeed())
		}
	})

	reconcileOnce := func() {
		GinkgoHelper()
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	getNodeOp := func() *kairosiov1alpha1.NodeOp {
		GinkgoHelper()
		out := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: "default"}, out)).To(Succeed())
		return out
	}

	listPods := func(kind string) []corev1.Pod {
		GinkgoHelper()
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList, client.InNamespace("default"),
			client.MatchingLabels{labelKeyNodeOp: resourceName, kind: "true"})).To(Succeed())
		return podList.Items
	}

	createNodeOp := func(spec kairosiov1alpha1.NodeOpSpec) {
		GinkgoHelper()
		spec.Command = []string{"echo", "test"}
		spec.Concurrency = 1
		spec.NodeSelector = &metav1.LabelSelector{MatchLabels: map[string]string{selectorKey: resourceName}}
		Expect(k8sClient.Create(ctx, &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec:       spec,
		})).To(Succeed())
	}

	// startedNode returns the one node that holds the only slot, after
	// checking that it is in the expected state.
	startedNode := func(inState func(kairosiov1alpha1.NodeStatus) bool) string {
		GinkgoHelper()
		statuses := getNodeOp().Status.NodeStatuses
		Expect(statuses).To(HaveLen(1))
		for name, status := range statuses {
			Expect(inState(status)).To(BeTrue(), "node %s: %+v", name, status)
			return name
		}
		return ""
	}

	otherNode := func(name string) string {
		for n := range nodes {
			if n != name {
				return n
			}
		}
		return ""
	}

	deleteNode := func(name string) {
		GinkgoHelper()
		Expect(k8sClient.Delete(ctx, nodes[name])).To(Succeed())
	}

	relabelNode := func(name string) {
		GinkgoHelper()
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
		delete(node.Labels, selectorKey)
		Expect(k8sClient.Update(ctx, node)).To(Succeed())
	}

	inPreflight := func(s kairosiov1alpha1.NodeStatus) bool { return s.Phase == phasePreflight }

	DescribeTable("frees the slot of a node in Preflight",
		func(leave func(string)) {
			createNodeOp(kairosiov1alpha1.NodeOpSpec{
				Preflight: &kairosiov1alpha1.PreflightSpec{Command: []string{"true"}},
			})
			reconcileOnce()
			first := startedNode(inPreflight)
			Expect(listPods(labelKeyPreflight)).To(HaveLen(1))

			leave(first)
			reconcileOnce()

			statuses := getNodeOp().Status.NodeStatuses
			Expect(statuses).NotTo(HaveKey(first))
			Expect(statuses).To(HaveKey(otherNode(first)))
			Expect(inPreflight(statuses[otherNode(first)])).To(BeTrue())
			pods := listPods(labelKeyPreflight)
			Expect(pods).To(HaveLen(1))
			Expect(pods[0].Spec.NodeName).To(Equal(otherNode(first)))
		},
		Entry("when the node is deleted", deleteNode),
		Entry("when the node no longer matches the selector", relabelNode),
	)

	DescribeTable("frees the slot of a node waiting for its reboot Pod",
		func(leave func(string)) {
			createNodeOp(kairosiov1alpha1.NodeOpSpec{RebootOnSuccess: asBool(true)})
			reconcileOnce()
			first := startedNode(awaitingRebootPod)
			Expect(listPods(labelKeyReboot)).To(HaveLen(1))

			leave(first)
			reconcileOnce()

			statuses := getNodeOp().Status.NodeStatuses
			Expect(statuses).NotTo(HaveKey(first))
			Expect(statuses).To(HaveKey(otherNode(first)))
			Expect(awaitingRebootPod(statuses[otherNode(first)])).To(BeTrue())
			// envtest runs no kubelet, so a deleted reboot Pod stays, terminating.
			live := 0
			for _, pod := range listPods(labelKeyReboot) {
				Expect(pod.DeletionTimestamp.IsZero()).To(Equal(pod.Spec.NodeName == otherNode(first)),
					"only the released node's reboot Pod may be deleted: %s on %s", pod.Name, pod.Spec.NodeName)
				if pod.DeletionTimestamp.IsZero() {
					live++
				}
			}
			Expect(live).To(Equal(1))
		},
		Entry("when the node is deleted", deleteNode),
		Entry("when the node no longer matches the selector", relabelNode),
	)
})
