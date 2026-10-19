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

// The Jobs a NodeOp creates carry no ttlSecondsAfterFinished and live as long
// as the NodeOp does, so pruning the finished ones is ordinary housekeeping.
// These specs pin that housekeeping cannot rewrite an outcome the NodeOp has
// already recorded.
var _ = Describe("NodeOp terminal node status", func() {
	var (
		ctx        context.Context
		node       *corev1.Node
		nodeOp     *kairosiov1alpha1.NodeOp
		reconciler *NodeOpReconciler
		key        types.NamespacedName
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		nodeName := fmt.Sprintf("terminal-node-%d", time.Now().UnixNano())
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: map[string]string{"kairos.io/terminal-test": nodeName},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		nodeOp = &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("terminal-nodeop-%d", time.Now().UnixNano()),
				Namespace: "default",
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command:      []string{"echo", "test"},
				NodeSelector: &metav1.LabelSelector{MatchLabels: node.Labels},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
		key = types.NamespacedName{Name: nodeOp.Name, Namespace: nodeOp.Namespace}

		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nodeOp)
			_ = k8sClient.Delete(ctx, node)
		})
	})

	// nodeJob returns the single Job the NodeOp created for the target node.
	nodeJob := func() *batchv1.Job {
		jobList := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobList,
			client.InNamespace(nodeOp.Namespace),
			client.MatchingLabels{"kairos.io/nodeop": nodeOp.Name})).To(Succeed())
		Expect(jobList.Items).To(HaveLen(1))
		return &jobList.Items[0]
	}

	// finishJob drives the Job to the given terminal condition and reconciles
	// once so the NodeOp records the outcome. The real API server validates
	// Job status transitions, so the surrounding timestamps and the
	// SuccessCriteriaMet/FailureTarget precursor conditions are set too.
	finishJob := func(conditionType batchv1.JobConditionType) {
		job := nodeJob()
		now := metav1.Now()
		job.Status.StartTime = &now

		var precursor batchv1.JobConditionType
		switch conditionType {
		case batchv1.JobComplete:
			precursor = batchv1.JobSuccessCriteriaMet
			job.Status.CompletionTime = &now
			job.Status.Succeeded = 1
		case batchv1.JobFailed:
			precursor = batchv1.JobFailureTarget
			job.Status.Failed = 1
		}

		job.Status.Conditions = []batchv1.JobCondition{
			{
				Type:               precursor,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
			},
			{
				Type:               conditionType,
				Status:             corev1.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
			},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
	}

	// pruneJob deletes the finished Job, the way an operator cleaning up
	// completed Jobs in the namespace would.
	pruneJob := func() {
		policy := metav1.DeletePropagationBackground
		Expect(k8sClient.Delete(ctx, nodeJob(), &client.DeleteOptions{PropagationPolicy: &policy})).To(Succeed())
		Eventually(func() int {
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(nodeOp.Namespace),
				client.MatchingLabels{"kairos.io/nodeop": nodeOp.Name})).To(Succeed())
			return len(jobList.Items)
		}, timeout, interval).Should(BeZero())
	}

	nodeStatus := func() kairosiov1alpha1.NodeStatus {
		current := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		status, ok := current.Status.NodeStatuses[node.Name]
		Expect(ok).To(BeTrue(), "expected a node status entry for %s", node.Name)
		return status
	}

	overallPhase := func() string {
		current := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, key, current)).To(Succeed())
		return current.Status.Phase
	}

	It("keeps a completed node completed after its Job is pruned", func() {
		By("Reconciling once so the Job is created")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		By("Completing the Job")
		finishJob(batchv1.JobComplete)
		Expect(nodeStatus().Phase).To(Equal(phaseCompleted))
		Expect(overallPhase()).To(Equal(phaseCompleted))

		By("Pruning the finished Job")
		pruneJob()

		By("Reconciling again, as the 5-minute resync does")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(nodeStatus().Phase).To(Equal(phaseCompleted),
			"a recorded success must not become a failure because its Job was cleaned up")
		Expect(nodeStatus().Message).To(Equal("Job completed successfully"))
		Expect(overallPhase()).To(Equal(phaseCompleted))
	})

	It("keeps the real failure reason after a failed node's Job is pruned", func() {
		By("Reconciling once so the Job is created")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		By("Failing the Job")
		finishJob(batchv1.JobFailed)
		Expect(nodeStatus().Phase).To(Equal(phaseFailed))
		Expect(nodeStatus().Message).To(Equal("Job failed"))

		By("Pruning the failed Job")
		pruneJob()

		By("Reconciling again, as the 5-minute resync does")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		Expect(nodeStatus().Phase).To(Equal(phaseFailed))
		Expect(nodeStatus().Message).To(Equal("Job failed"),
			"the reason the node failed must survive the Job being cleaned up")
	})
})
