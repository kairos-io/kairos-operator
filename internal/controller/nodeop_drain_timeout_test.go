package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("drainNode timeout", func() {
	var (
		node       *corev1.Node
		podKey     types.NamespacedName
		reconciler *NodeOpReconciler
	)

	asInt32 := func(i int32) *int32 { return &i }

	// controlledPod is a Pod the drain evicts under the default DrainOptions:
	// owned by a controller, not a DaemonSet, not the reboot Pod. It names the
	// node directly, since envtest runs no scheduler.
	controlledPod := func(name, nodeName string, ownerKind string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       ownerKind,
					Name:       name,
					UID:        types.UID("22222222-2222-2222-2222-222222222222"),
					Controller: asBool(true),
				}},
			},
			Spec: corev1.PodSpec{
				NodeName:                      nodeName,
				TerminationGracePeriodSeconds: ptr(int64(45)),
				Containers: []corev1.Container{{
					Name:  "workload",
					Image: "workload:latest",
				}},
			},
		}
	}

	// forceDelete finishes a termination the way a kubelet would.
	forceDelete := func(key types.NamespacedName) {
		GinkgoHelper()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		Expect(client.IgnoreNotFound(
			k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)),
		)).To(Succeed())
	}

	BeforeEach(func() {
		suffix := randStringRunes(5)
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "drain-timeout-" + suffix}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		pod := controlledPod("workload-"+suffix, node.Name, "ReplicaSet")
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		podKey = client.ObjectKeyFromObject(pod)

		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		forceDelete(podKey)
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
	})

	// envtest runs no kubelet, so nothing ever confirms a deletion and an
	// evicted Pod stays Terminating until something force-deletes it. That is
	// the same state a real Pod with a stuck preStop hook or an uncleared
	// finalizer sits in, which is exactly what the timeout exists for.

	It("does not report the node drained while the Pod is still terminating", func() {
		drained := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			drained <- reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{
				TimeoutSeconds: asInt32(60),
			})
		}()

		By("waiting for the eviction to be issued")
		Eventually(func() *metav1.Time {
			pod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, podKey, pod); err != nil {
				return nil
			}
			return pod.DeletionTimestamp
		}, "10s").ShouldNot(BeNil())

		By("checking the drain has not returned")
		Consistently(drained, "4s", "500ms").ShouldNot(Receive(),
			"the drain must not report success while the Pod it evicted is still terminating")

		By("finishing the termination")
		forceDelete(podKey)

		Eventually(drained, "20s").Should(Receive(BeNil()))
	})

	It("gives up when timeoutSeconds expires, and names the Pod and the node", func() {
		start := time.Now()
		err := reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{
			TimeoutSeconds: asInt32(1),
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(podKey.String()))
		Expect(err.Error()).To(ContainSubstring(node.Name))
		Expect(time.Since(start)).To(BeNumerically("<", 30*time.Second),
			"the configured timeout should bound the wait, not the default")
	})

	It("stops waiting for a Pod that was replaced under the same name", func() {
		// A new Pod with the same namespace/name is a different Pod. The drain
		// evicted the one it saw, and the replacement is the controller's
		// business, not the drain's.
		evicted := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, evicted)).To(Succeed())

		drained := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			drained <- reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{
				TimeoutSeconds: asInt32(60),
			})
		}()

		Eventually(func() *metav1.Time {
			pod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, podKey, pod); err != nil {
				return nil
			}
			return pod.DeletionTimestamp
		}, "10s").ShouldNot(BeNil())

		forceDelete(podKey)
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, podKey, &corev1.Pod{}))
		}, "10s").Should(BeTrue())

		replacement := controlledPod(podKey.Name, node.Name, "ReplicaSet")
		Expect(k8sClient.Create(ctx, replacement)).To(Succeed())
		Expect(replacement.UID).ToNot(Equal(evicted.UID))

		Eventually(drained, "20s").Should(Receive(BeNil()),
			"the replacement Pod carries a new UID, so it is not the Pod being drained")
	})

	It("does not wait when it evicted nothing", func() {
		// The only Pod on this node is DaemonSet-managed, which the default
		// IgnoreDaemonSets skips. An empty drain must return at once rather
		// than sit out the default timeout.
		suffix := randStringRunes(5)
		dsNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "drain-noop-" + suffix}}
		Expect(k8sClient.Create(ctx, dsNode)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, dsNode))).To(Succeed())
		})

		dsPod := controlledPod("logshipper-"+suffix, dsNode.Name, "DaemonSet")
		Expect(k8sClient.Create(ctx, dsPod)).To(Succeed())
		dsKey := client.ObjectKeyFromObject(dsPod)
		DeferCleanup(func() { forceDelete(dsKey) })

		start := time.Now()
		Expect(reconciler.drainNode(ctx, dsNode, &kairosiov1alpha1.DrainOptions{})).To(Succeed())
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))

		By("leaving the DaemonSet Pod alone")
		kept := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, dsKey, kept)).To(Succeed())
		Expect(kept.DeletionTimestamp).To(BeNil())
	})
})

var _ = Describe("drainTimeout", func() {
	asInt32 := func(i int32) *int32 { return &i }

	DescribeTable("resolves the wait a drain is given",
		func(opts *kairosiov1alpha1.DrainOptions, expected time.Duration) {
			Expect(drainTimeout(opts)).To(Equal(expected))
		},
		Entry("no drain options at all", nil, DrainTimeoutDefault),
		Entry("timeoutSeconds unset", &kairosiov1alpha1.DrainOptions{}, DrainTimeoutDefault),
		// The field is optional with no kubebuilder default, so zero is what an
		// operator writing `timeoutSeconds: 0` gets. kubectl reads zero as
		// "wait forever"; a reconcile cannot, so it reads as "unset".
		Entry("timeoutSeconds zero", &kairosiov1alpha1.DrainOptions{TimeoutSeconds: asInt32(0)}, DrainTimeoutDefault),
		Entry("timeoutSeconds negative", &kairosiov1alpha1.DrainOptions{TimeoutSeconds: asInt32(-5)}, DrainTimeoutDefault),
		Entry("timeoutSeconds set", &kairosiov1alpha1.DrainOptions{TimeoutSeconds: asInt32(90)}, 90*time.Second),
		Entry("timeoutSeconds matching the shipped sample", &kairosiov1alpha1.DrainOptions{TimeoutSeconds: asInt32(300)}, 5*time.Minute),
	)
})
