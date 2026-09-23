package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// A node is routed to startMainJob only while it has no NodeStatus entry, so
// every attempt that creates children and then returns early is retried from
// the top: a drain that cannot evict a Pod, a Job rejected by quota or
// admission, a conflicting status update. The children of the first attempt
// have to be adopted, not duplicated.
var _ = Describe("startMainJob after an attempt that left children behind", func() {
	const (
		namespace = "default"
		nodeName  = "worker-1"
	)

	var (
		ctx    context.Context
		r      *NodeOpReconciler
		nodeOp *kairosiov1alpha1.NodeOp
		node   corev1.Node
	)

	newReconciler := func(objs ...client.Object) *NodeOpReconciler {
		builder := fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithStatusSubresource(&kairosiov1alpha1.NodeOp{}).
			WithObjects(objs...)
		return &NodeOpReconciler{Client: builder.Build(), Scheme: scheme.Scheme}
	}

	rebootPods := func() []corev1.Pod {
		podList := &corev1.PodList{}
		Expect(r.List(ctx, podList, client.InNamespace(namespace),
			client.MatchingLabels(map[string]string{labelKeyReboot: "true"}))).To(Succeed())
		return podList.Items
	}

	jobs := func() []batchv1.Job {
		jobList := &batchv1.JobList{}
		Expect(r.List(ctx, jobList, client.InNamespace(namespace))).To(Succeed())
		return jobList.Items
	}

	BeforeEach(func() {
		ctx = context.Background()
		nodeOp = &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "upgrade",
				Namespace: namespace,
				UID:       types.UID("nodeop-uid"),
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "hello"},
				Image:           "busybox",
				RebootOnSuccess: asBool(true),
			},
		}
		node = corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
	})

	When("the first attempt created the reboot Pod but never created the Job", func() {
		BeforeEach(func() {
			r = newReconciler(nodeOp)
			// Exactly what startMainJob does before it hands over to
			// createNodeJob, which is where a drain failure returns.
			Expect(r.createRebootPod(ctx, nodeOp, nodeName, "upgrade-worker-1-aaaaa")).To(Succeed())
			Expect(rebootPods()).To(HaveLen(1))
		})

		It("replaces the orphan instead of leaving a second privileged Pod on the node", func() {
			Expect(r.startMainJob(ctx, nodeOp, node)).To(Succeed())

			pods := rebootPods()
			Expect(pods).To(HaveLen(1),
				"a reboot Pod watching a Job that was never created can never finish, "+
					"and nothing deletes it while the node has no NodeStatus entry")

			// The surviving Pod must watch the Job that now exists, not the
			// sentinel name the abandoned attempt was told about.
			created := jobs()
			Expect(created).To(HaveLen(1))
			Expect(pods[0].Spec.Containers[0].Command[2]).To(ContainSubstring(created[0].Name + "-*"))
			Expect(pods[0].Spec.Containers[0].Command[2]).ToNot(ContainSubstring("upgrade-worker-1-aaaaa-*"))
		})

		It("does not grow the Pod count on every retry", func() {
			for i := 0; i < 3; i++ {
				Expect(r.startMainJob(ctx, nodeOp, node)).To(Succeed(),
					fmt.Sprintf("attempt %d", i+1))
				// Undo the status the successful attempt wrote, so the node
				// looks fresh again the way a failed attempt leaves it.
				nodeOp.Status.NodeStatuses = nil
			}
			Expect(rebootPods()).To(HaveLen(1))
		})
	})

	When("the first attempt created the Job but its status update failed", func() {
		var existing *batchv1.Job

		BeforeEach(func() {
			existing = &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name:      "upgrade-worker-1-bbbbb",
				Namespace: namespace,
				Labels: map[string]string{
					labelKeyNodeOp: nodeOp.Name,
					labelKeyNode:   nodeName,
				},
			}}
			Expect(controllerutil.SetControllerReference(nodeOp, existing, scheme.Scheme)).To(Succeed())
			r = newReconciler(nodeOp, existing)
		})

		It("adopts it instead of running the operation twice on the node", func() {
			Expect(r.startMainJob(ctx, nodeOp, node)).To(Succeed())

			Expect(jobs()).To(HaveLen(1),
				"two Jobs would run the same command concurrently on one node")
			Expect(nodeOp.Status.NodeStatuses).To(HaveKey(nodeName))
			Expect(nodeOp.Status.NodeStatuses[nodeName].JobName).To(Equal(existing.Name))
			Expect(nodeOp.Status.NodeStatuses[nodeName].RebootStatus).To(Equal(rebootStatusPending))
		})
	})

	When("a NodeOp of the same name was deleted and recreated", func() {
		BeforeEach(func() {
			// Still owned by the previous NodeOp, and on its way out.
			stale := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name:      "upgrade-worker-1-ccccc",
				Namespace: namespace,
				Labels: map[string]string{
					labelKeyNodeOp: nodeOp.Name,
					labelKeyNode:   nodeName,
				},
			}}
			previous := nodeOp.DeepCopy()
			previous.UID = types.UID("previous-nodeop-uid")
			Expect(controllerutil.SetControllerReference(previous, stale, scheme.Scheme)).To(Succeed())
			r = newReconciler(nodeOp, stale)
		})

		It("starts its own Job rather than adopting the previous owner's", func() {
			Expect(r.startMainJob(ctx, nodeOp, node)).To(Succeed())

			Expect(nodeOp.Status.NodeStatuses[nodeName].JobName).ToNot(Equal("upgrade-worker-1-ccccc"))
			Expect(jobs()).To(HaveLen(2))
		})
	})
})
