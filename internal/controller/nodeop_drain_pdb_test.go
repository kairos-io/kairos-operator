package controller

import (
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
		node       *corev1.Node
		podKey     types.NamespacedName
		pdb        *policyv1.PodDisruptionBudget
		reconciler *NodeOpReconciler
	)

	BeforeEach(func() {
		suffix := randStringRunes(5)
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "drain-pdb-" + suffix}}
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

		err := reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{})
		Expect(err).To(HaveOccurred(), "a drain blocked by a budget must report the failure, so the reconcile retries")

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.DeletionTimestamp).To(BeNil(),
			"the guarded Pod must survive a drain its PodDisruptionBudget forbids")
	})

	It("evicts a Pod whose budget has room", func() {
		allowDisruptions(1)

		Expect(reconciler.drainNode(ctx, node, &kairosiov1alpha1.DrainOptions{})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, podKey, pod)).To(Succeed())
		Expect(pod.DeletionTimestamp).ToNot(BeNil(),
			"a budget with room must not stop the drain")
	})
})
