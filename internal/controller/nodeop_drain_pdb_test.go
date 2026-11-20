package controller

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// A drain is only a drain if it goes through the eviction subresource: a plain
// DELETE on the Pod is never checked against a PodDisruptionBudget, so an
// operation that drains several nodes can take every replica of a guarded
// workload down at once. These specs pin the budget to a known status, which is
// what the eviction admission check reads, and drive drainNode against both
// answers.
var _ = Describe("drainNode and PodDisruptionBudgets", func() {
	var (
		suffix     string
		node       *corev1.Node
		podKey     types.NamespacedName
		pdb        *policyv1.PodDisruptionBudget
		reconciler *NodeOpReconciler
	)

	BeforeEach(func() {
		suffix = randStringRunes(5)
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   "drain-pdb-" + suffix,
			Labels: map[string]string{"drain-pdb": suffix},
		}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		// Owned by a controller, so the default DrainOptions.Force lets the
		// drain touch it. It names the node directly, since envtest runs no
		// scheduler.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "guarded-" + suffix,
				Namespace: "default",
				Labels:    map[string]string{"drain-pdb": suffix},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "guarded-" + suffix,
					UID:        types.UID("22222222-2222-2222-2222-222222222222"),
					Controller: asBool(true),
				}},
			},
			Spec: corev1.PodSpec{
				NodeName: node.Name,
				Containers: []corev1.Container{{
					Name:  "guarded",
					Image: "guarded:latest",
				}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())
		podKey = client.ObjectKeyFromObject(pod)

		// The eviction admission check skips the budget entirely for a Pod that
		// is Pending or not Ready, and envtest runs no kubelet to report either.
		// Writing the status here is what puts the Pod inside its budget's
		// scope, so the spec exercises the guarded case rather than the bypass.
		pod.Status = corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			}},
		}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		pdb = &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "guarded-" + suffix, Namespace: "default"},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"drain-pdb": suffix},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pdb)).To(Succeed())

		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pdb))).To(Succeed())
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace}}
		Expect(client.IgnoreNotFound(
			k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)),
		)).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
	})

	// envtest runs no kube-controller-manager, so nothing computes the budget's
	// status. Writing it here is what makes the eviction admission check
	// deterministic: it refuses while DisruptionsAllowed is 0 and admits once it
	// is positive, and it only trusts a status whose ObservedGeneration has
	// caught up with the spec.
	allowDisruptions := func(allowed int32) {
		GinkgoHelper()
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pdb), pdb)).To(Succeed())
		pdb.Status = policyv1.PodDisruptionBudgetStatus{
			ObservedGeneration: pdb.Generation,
			DisruptionsAllowed: allowed,
			CurrentHealthy:     1,
			DesiredHealthy:     1,
			ExpectedPods:       1,
		}
		Expect(k8sClient.Status().Update(ctx, pdb)).To(Succeed())
	}

	It("leaves a Pod alone when its budget allows no disruption", func() {
		allowDisruptions(0)

		err := reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{TimeoutSeconds: ptr(int32(1))})
		Expect(err).To(MatchError(ContainSubstring(podKey.String())),
			"a drain the budget blocks until the timeout must fail and name the Pod")

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.DeletionTimestamp).To(BeNil(),
			"the guarded Pod must survive a drain its PodDisruptionBudget forbids")
	})

	It("evicts a Pod whose budget has room", func() {
		allowDisruptions(1)

		// The drain then waits for the evicted Pod to finish terminating, and
		// envtest runs no kubelet to end it, so the wait gets one second and
		// times out. What this spec checks is that the eviction was admitted.
		oneSecond := int32(1)
		Expect(reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{TimeoutSeconds: &oneSecond})).To(
			MatchError(ContainSubstring("still terminating")))

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.DeletionTimestamp).ToNot(BeNil(),
			"a budget with room must not stop the drain")
	})

	It("evicts the other Pods on the node while a budget denies one", func() {
		allowDisruptions(0)
		// Sorts after the guarded Pod, so the drain reaches it after the denial.
		other := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "unguarded-" + suffix,
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "unguarded-" + suffix,
					UID:        types.UID("33333333-3333-3333-3333-333333333333"),
					Controller: asBool(true),
				}},
			},
			Spec: corev1.PodSpec{
				NodeName:   node.Name,
				Containers: []corev1.Container{{Name: "unguarded", Image: "unguarded:latest"}},
			},
		}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, other, client.GracePeriodSeconds(0)))).To(Succeed())
		})

		Expect(reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{TimeoutSeconds: ptr(int32(1))})).
			To(HaveOccurred())

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(other), other)).To(Succeed())
		Expect(other.DeletionTimestamp).ToNot(BeNil(),
			"a denial for one Pod must not stop the eviction of the others, since a budget can wait on them")
	})

	Context("when a NodeOp cordons and drains the node", func() {
		var nodeOp *kairosiov1alpha1.NodeOp

		reconcileNodeOp := func() error {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(nodeOp)})
			return err
		}

		// startDrain makes the reboot Pod ready.  The next reconcile then
		// cordons and drains the node.
		startDrain := func(timeoutSeconds int32) {
			GinkgoHelper()
			nodeOp = &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: "drain-pdb-" + suffix, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					NodeSelector:    &metav1.LabelSelector{MatchLabels: map[string]string{"drain-pdb": suffix}},
					RebootOnSuccess: asBool(true),
					Cordon:          asBool(true),
					DrainOptions: &kairosiov1alpha1.DrainOptions{
						Enabled:        asBool(true),
						TimeoutSeconds: ptr(timeoutSeconds),
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, nodeOp))).To(Succeed())
			})
			Expect(reconcileNodeOp()).To(Succeed())
			markRebootPodsRunning(ctx, nodeOp)
		}

		nodeCordoned := func() bool {
			GinkgoHelper()
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(Succeed())
			return node.Spec.Unschedulable
		}

		It("keeps the node cordoned while a budget blocks the drain, and finishes once the budget allows it", func() {
			allowDisruptions(0)
			startDrain(60)

			reconciled := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				reconciled <- reconcileNodeOp()
			}()
			Consistently(reconciled, "4s", "500ms").ShouldNot(Receive(),
				"a budget denial with time left is a drain still in progress, not a failed drain")
			Expect(nodeCordoned()).To(BeTrue(),
				"an uncordoned node takes back the Pods the drain evicted, so the drain never finishes")

			allowDisruptions(1)
			Eventually(func() *metav1.Time {
				pod := &corev1.Pod{}
				Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
				return pod.DeletionTimestamp
			}, "10s").ShouldNot(BeNil(), "the drain must retry the eviction once the budget has room")
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podKey.Name, Namespace: podKey.Namespace}}
			Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())

			Eventually(reconciled, "20s").Should(Receive(BeNil()))
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: nodeOp.Name})).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1), "the upgrade Job must start once the drain finishes")
			Expect(nodeCordoned()).To(BeTrue())
		})

		It("uncordons the node when a budget blocks the drain until the timeout", func() {
			allowDisruptions(0)
			startDrain(1)

			Expect(reconcileNodeOp()).To(HaveOccurred())
			Expect(nodeCordoned()).To(BeFalse(), "a drain that gives up leaves the node schedulable")
		})
	})
})
