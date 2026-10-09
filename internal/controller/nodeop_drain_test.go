package controller

import (
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("drainNode grace period", func() {
	// The Pod declares its own grace period, which the API server applies to a
	// DELETE that carries none. Keeping it different from every requested value
	// tells the two apart in the deletionGracePeriodSeconds the Pod ends up with.
	const podGracePeriod int64 = 45

	var (
		node       *corev1.Node
		podKey     types.NamespacedName
		reconciler *NodeOpReconciler
	)

	// asInt32 mirrors asBool: DrainOptions.GracePeriodSeconds is a *int32.
	asInt32 := func(i int32) *int32 { return &i }

	BeforeEach(func() {
		suffix := randStringRunes(5)
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "drain-grace-" + suffix}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		// A Pod owned by a controller, so drainNode evicts it with the default
		// DrainOptions.Force. It names the node directly, since envtest runs no
		// scheduler, and it stays Pending, since envtest runs no kubelet.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload-" + suffix,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "workload-" + suffix,
					UID:        types.UID("11111111-1111-1111-1111-111111111111"),
					Controller: asBool(true),
				}},
			},
			Spec: corev1.PodSpec{
				NodeName:                      node.Name,
				TerminationGracePeriodSeconds: ptr(podGracePeriod),
				Containers: []corev1.Container{{
					Name:  "workload",
					Image: "workload:latest",
				}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		podKey = client.ObjectKeyFromObject(pod)

		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace}}
		Expect(client.IgnoreNotFound(
			k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)),
		)).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
	})

	// drainedPod runs a drain and returns the Pod the API server kept, which is
	// terminating and carries the grace period the DELETE asked for.
	//
	// The drain waits for the Pods it evicted, and envtest runs no kubelet to
	// finish the termination, so it waits out its timeout and reports the
	// failure. These specs are about which grace period the DELETE carried, so
	// they give the wait one second and read the Pod it left behind.
	drainedPod := func(opts *kairosiov1alpha1.DrainOptions) *corev1.Pod {
		GinkgoHelper()
		opts.TimeoutSeconds = asInt32(1)
		Expect(reconciler.drainNode(ctx, node, opts)).To(
			MatchError(ContainSubstring("still terminating")))

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.DeletionTimestamp).ToNot(BeNil(), "the workload Pod should be terminating")
		Expect(pod.DeletionGracePeriodSeconds).ToNot(BeNil())
		return pod
	}

	It("evicts with the requested grace period", func() {
		pod := drainedPod(&kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(30)})
		Expect(*pod.DeletionGracePeriodSeconds).To(Equal(int64(30)))
	})

	It("evicts with the Pod's own grace period when none is configured", func() {
		pod := drainedPod(&kairosiov1alpha1.DrainOptions{})
		Expect(*pod.DeletionGracePeriodSeconds).To(Equal(podGracePeriod))
	})

	It("evicts with the Pod's own grace period when a negative one is configured", func() {
		// The CRD documents a negative value as "use the grace period the Pod
		// declares", which is what the API server applies to a DELETE that
		// carries no grace period.
		pod := drainedPod(&kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(-1)})
		Expect(*pod.DeletionGracePeriodSeconds).To(Equal(podGracePeriod))
	})

	It("evicts immediately with a zero grace period", func() {
		opts := &kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(0)}
		Expect(reconciler.drainNode(ctx, node, opts)).To(Succeed())

		// No Eventually: a zero grace period removes the Pod at once, and the
		// drain only reports the node drained once the Pod is actually gone.
		err := k8sClient.Get(ctx, podKey, &corev1.Pod{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the drain should have waited for the Pod to go")
	})
})
