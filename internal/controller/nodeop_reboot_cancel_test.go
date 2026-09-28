package controller

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// errDeleteRefused stands in for whatever makes a Delete fail in a real
// cluster: an API server blip, a webhook rejection, a Forbidden while the
// reboot RBAC is still being reconciled.
var errDeleteRefused = errors.New("delete refused")

var _ = Describe("processRebootStatus cancelling a reboot", func() {
	// The reboot Pod is privileged, hostPID, and polls forever with infinite
	// NoExecute tolerations, so nothing in the cluster removes it on its own.
	// cleanupRebootPodForNode is the only thing that does, and
	// status.RebootStatus == cancelled is the only thing that stops the
	// controller from calling it again. Recording the cancellation before the
	// Delete therefore has to be wrong: one failed Delete would leak the Pod
	// for as long as the NodeOp lives.
	var (
		nodeOp     *kairosiov1alpha1.NodeOp
		podKey     types.NamespacedName
		failed     kairosiov1alpha1.NodeStatus
		failDelete bool
		cl         client.Client
		reconciler *NodeOpReconciler
	)

	const nodeName = "reboot-cancel-node"

	BeforeEach(func() {
		failDelete = false

		nodeOp = &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "reboot-cancel-" + randStringRunes(5),
				Namespace: "default",
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:           "quay.io/kairos/kairos:latest",
				Command:         []string{"/bin/true"},
				RebootOnSuccess: asBool(true),
			},
		}

		// The labels are the ones createRebootPod sets, since they are what
		// cleanupRebootPodForNode selects on.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      nodeOp.Name + "-reboot-abcde",
				Namespace: nodeOp.Namespace,
				Labels: map[string]string{
					labelKeyNodeOp: nodeOp.Name,
					labelKeyReboot: "true",
					labelKeyNode:   nodeName,
				},
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "reboot", Image: "quay.io/kairos/kairos-operator:latest"}},
			},
		}
		podKey = types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}

		// A fake client rather than envtest, because the point of the test is
		// what happens when the Delete fails, which the real API server will
		// not do on demand.
		cl = fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(nodeOp, pod).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if failDelete {
						return apierrors.NewInternalError(errDeleteRefused)
					}
					return c.Delete(ctx, obj, opts...)
				},
			}).
			Build()
		reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

		// The shape updateNodeOpStatus hands to processRebootStatus once
		// processJobStatus has seen the node's Job fail: the reboot was asked
		// for and is still pending.
		failed = kairosiov1alpha1.NodeStatus{
			Phase:        phaseFailed,
			JobName:      nodeOp.Name + "-" + nodeName,
			Message:      msgJobFailed,
			RebootStatus: rebootStatusPending,
		}
	})

	It("deletes the reboot Pod and records the cancellation", func() {
		status, err := reconciler.processRebootStatus(ctx, nodeOp, nodeName, failed)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.RebootStatus).To(Equal(rebootStatusCancelled))

		Expect(apierrors.IsNotFound(cl.Get(ctx, podKey, &corev1.Pod{}))).To(BeTrue(),
			"the reboot Pod should be gone")
	})

	It("leaves the cancellation unrecorded when the Pod cannot be deleted", func() {
		failDelete = true

		status, err := reconciler.processRebootStatus(ctx, nodeOp, nodeName, failed)
		Expect(err).NotTo(HaveOccurred(), "the rest of the status write must still happen")
		Expect(status.RebootStatus).To(Equal(rebootStatusPending),
			"recording the cancellation here is what makes the cleanup unreachable")

		Expect(cl.Get(ctx, podKey, &corev1.Pod{})).To(Succeed(),
			"the Pod is still there, so the status must not claim otherwise")
	})

	It("deletes the Pod on a later pass after a failed delete", func() {
		failDelete = true
		status, err := reconciler.processRebootStatus(ctx, nodeOp, nodeName, failed)
		Expect(err).NotTo(HaveOccurred())

		// The next reconcile gets the status the failed pass returned. With the
		// cancellation recorded too early this second pass is a no-op and the
		// Pod survives for as long as the NodeOp does.
		failDelete = false
		status, err = reconciler.processRebootStatus(ctx, nodeOp, nodeName, status)
		Expect(err).NotTo(HaveOccurred())
		Expect(status.RebootStatus).To(Equal(rebootStatusCancelled))

		Expect(apierrors.IsNotFound(cl.Get(ctx, podKey, &corev1.Pod{}))).To(BeTrue(),
			"the retry should have removed the reboot Pod")
	})
})
