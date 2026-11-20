package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOp overall phase with Concurrency", func() {
	const namespace = "default"

	var (
		ctx          context.Context
		reconciler   *NodeOpReconciler
		nodeOpName   string
		createdNodes []*corev1.Node
	)

	reconcileOnce := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: nodeOpName, Namespace: namespace},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	nodeOpJobs := func() []batchv1.Job {
		jobList := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobList,
			client.InNamespace(namespace),
			client.MatchingLabels{labelKeyNodeOp: nodeOpName},
		)).To(Succeed())
		return jobList.Items
	}

	phase := func() string {
		nodeOp := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: nodeOpName, Namespace: namespace}, nodeOp)).To(Succeed())
		return nodeOp.Status.Phase
	}

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		nodeOpName = fmt.Sprintf("phase-test-%d", time.Now().UnixNano())

		createdNodes = nil
		for i := 0; i < 3; i++ {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("%s-node-%d", nodeOpName, i),
				Labels: map[string]string{labelKeyKairosManaged: "true"},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			createdNodes = append(createdNodes, node)
		}

		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: nodeOpName, Namespace: namespace},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command:     []string{"echo", "test"},
				Concurrency: 1,
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{labelKeyKairosManaged: "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
	})

	AfterEach(func() {
		nodeOp := &kairosiov1alpha1.NodeOp{ObjectMeta: metav1.ObjectMeta{
			Name: nodeOpName, Namespace: namespace,
		}}
		_ = k8sClient.Delete(ctx, nodeOp)
		propagation := metav1.DeletePropagationBackground
		for _, job := range nodeOpJobs() {
			_ = k8sClient.Delete(ctx, &job, &client.DeleteOptions{PropagationPolicy: &propagation})
		}
		for _, node := range createdNodes {
			_ = k8sClient.Delete(ctx, node)
		}
	})

	It("stays Running until every target node has finished", func() {
		By("starting the first node")
		reconcileOnce()
		Expect(nodeOpJobs()).To(HaveLen(1), "Concurrency=1 runs one node at a time")
		Expect(phase()).To(Equal(phaseRunning))

		By("completing the first of three nodes")
		jobs := nodeOpJobs()
		Expect(markJobAsCompleted(ctx, k8sClient, &jobs[0])).To(Succeed())
		reconcileOnce()

		Expect(nodeOpJobs()).To(HaveLen(2), "the second node should have been started")
		Expect(phase()).To(Equal(phaseRunning),
			"one of three nodes is done, so the operation is not complete")
	})

	It("reports Completed once every target node has finished", func() {
		for i := 0; i < len(createdNodes); i++ {
			reconcileOnce()
			jobs := nodeOpJobs()
			Expect(jobs).To(HaveLen(i+1), "Concurrency=1 adds one node per round")
			for j := range jobs {
				if jobs[j].Status.Succeeded == 0 {
					Expect(markJobAsCompleted(ctx, k8sClient, &jobs[j])).To(Succeed())
				}
			}
		}

		reconcileOnce()
		Expect(phase()).To(Equal(phaseCompleted))
	})
})
