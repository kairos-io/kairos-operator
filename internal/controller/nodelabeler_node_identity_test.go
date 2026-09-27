package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// The one-shot labeler Job is what makes a node "managed": it is the only thing
// that sets kairos.io/managed, and the DaemonSet that keeps the labels in sync
// selects on that label. A node re-provisioned under the same host name comes
// back as a fresh Node object with no labels, so it needs the Job to run again.
var _ = Describe("NodeLabeler Controller node identity", func() {
	var (
		ctx       context.Context
		nodeName  string
		namespace = "default"
	)

	labelerJobsFor := func(name string) []batchv1.Job {
		GinkgoHelper()

		jobList := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobList,
			client.InNamespace(namespace),
			client.MatchingLabels(map[string]string{
				labelKeyJobNode: name,
				labelKeyApp:     labelValueNodeLabelerApp,
			}),
		)).To(Succeed())

		return jobList.Items
	}

	createNode := func(name string) *corev1.Node {
		GinkgoHelper()

		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		return node
	}

	deleteNode := func(name string) {
		GinkgoHelper()

		node := &corev1.Node{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, node); err == nil {
			Expect(k8sClient.Delete(ctx, node)).To(Succeed())
		}
		Eventually(func() bool {
			node := &corev1.Node{}
			return k8sClient.Get(ctx, types.NamespacedName{Name: name}, node) != nil
		}, time.Second*10, time.Millisecond*100).Should(BeTrue())
	}

	// reconcileUntilSettled drives the reconciler to a fixed point, so the spec
	// does not depend on whether a replacement lands in one pass or two.
	reconcileUntilSettled := func(r *NodeLabelerReconciler, name string) {
		GinkgoHelper()

		for i := 0; i < 5; i++ {
			result, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name},
			})
			Expect(err).NotTo(HaveOccurred())
			if !result.Requeue && result.RequeueAfter == 0 {
				return
			}
		}
		Fail("reconciler never settled")
	}

	BeforeEach(func() {
		ctx = context.Background()
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", namespace)).To(Succeed())
		Expect(os.Setenv("NODE_LABELER_IMAGE", "quay.io/kairos/operator-node-labeler:v0.0.1")).To(Succeed())
		nodeName = fmt.Sprintf("identity-node-%d", time.Now().UnixNano())
	})

	AfterEach(func() {
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())
		Expect(os.Unsetenv("NODE_LABELER_IMAGE")).To(Succeed())
		deleteNode(nodeName)

		propagation := metav1.DeletePropagationBackground
		for _, job := range labelerJobsFor(nodeName) {
			Expect(client.IgnoreNotFound(
				k8sClient.Delete(ctx, &job, client.PropagationPolicy(propagation)),
			)).To(Succeed())
		}
	})

	It("records the node UID on the Job it creates", func() {
		node := createNode(nodeName)
		reconciler := &NodeLabelerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		reconcileUntilSettled(reconciler, nodeName)

		jobs := labelerJobsFor(nodeName)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Labels).To(HaveKeyWithValue(labelKeyNodeUID, string(node.UID)))
	})

	It("labels a node re-provisioned under the same name, instead of gating on the old Job", func() {
		firstNode := createNode(nodeName)
		reconciler := &NodeLabelerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		reconcileUntilSettled(reconciler, nodeName)
		firstJobs := labelerJobsFor(nodeName)
		Expect(firstJobs).To(HaveLen(1))
		firstJobUID := firstJobs[0].UID

		By("re-provisioning the node under the same host name")
		deleteNode(nodeName)
		secondNode := createNode(nodeName)
		Expect(secondNode.UID).NotTo(Equal(firstNode.UID))

		By("leaving the Job of the previous node behind, as a real cluster does")
		Expect(labelerJobsFor(nodeName)).To(HaveLen(1))

		reconcileUntilSettled(reconciler, nodeName)

		jobs := labelerJobsFor(nodeName)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Labels).To(HaveKeyWithValue(labelKeyNodeUID, string(secondNode.UID)))
		Expect(jobs[0].UID).NotTo(Equal(firstJobUID))
	})

	It("replaces a Job that records no node UID, because it cannot be shown to belong to this node", func() {
		node := createNode(nodeName)
		reconciler := &NodeLabelerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		By("creating the Job an operator older than this fix would have created")
		legacy := reconciler.createNodeLabelerJob(node, namespace)
		delete(legacy.Labels, labelKeyNodeUID)
		Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
		legacyUID := legacy.UID

		reconcileUntilSettled(reconciler, nodeName)

		jobs := labelerJobsFor(nodeName)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Labels).To(HaveKeyWithValue(labelKeyNodeUID, string(node.UID)))
		Expect(jobs[0].UID).NotTo(Equal(legacyUID))
	})

	It("keeps the Job of the node it belongs to", func() {
		node := createNode(nodeName)
		reconciler := &NodeLabelerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		reconcileUntilSettled(reconciler, nodeName)
		jobs := labelerJobsFor(nodeName)
		Expect(jobs).To(HaveLen(1))
		created := jobs[0]

		reconcileUntilSettled(reconciler, nodeName)

		jobs = labelerJobsFor(nodeName)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].UID).To(Equal(created.UID))
		Expect(jobs[0].ResourceVersion).To(Equal(created.ResourceVersion))
		Expect(node.UID).NotTo(BeEmpty())
	})
})
