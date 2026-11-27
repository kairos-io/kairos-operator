package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOp tolerations", func() {
	var (
		reconciler *NodeOpReconciler
		nodeOp     *kairosiov1alpha1.NodeOp
		node       corev1.Node
	)

	seconds := func(v int64) *int64 { return &v }

	customTaint := corev1.Toleration{
		Key:      "dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "edge",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	controlPlane := corev1.Toleration{
		Key:      "node-role.kubernetes.io/control-plane",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	notReadyInfinite := corev1.Toleration{
		Key:      corev1.TaintNodeNotReady,
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoExecute,
	}
	unreachableInfinite := corev1.Toleration{
		Key:      corev1.TaintNodeUnreachable,
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoExecute,
	}

	BeforeEach(func() {
		reconciler = &NodeOpReconciler{}
		node = corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
		nodeOp = &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: "tolerations", Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command:     []string{"true"},
				Tolerations: []corev1.Toleration{customTaint, controlPlane},
				Preflight:   &kairosiov1alpha1.PreflightSpec{Command: []string{"true"}},
			},
		}
	})

	It("sets them on the standard Job pod template", func() {
		spec := reconciler.createStandardJobSpec(nodeOp, node, 6)
		Expect(spec.Template.Spec.Tolerations).To(Equal([]corev1.Toleration{customTaint, controlPlane}))
	})

	It("sets them on the reboot Job pod template", func() {
		spec := reconciler.createRebootJobSpec(nodeOp, node, 6)
		Expect(spec.Template.Spec.Tolerations).To(Equal([]corev1.Toleration{customTaint, controlPlane}))
	})

	It("sets them on the preflight Pod", func() {
		pod := reconciler.buildPreflightPod(nodeOp, node)
		Expect(pod.Spec.Tolerations).To(Equal([]corev1.Toleration{customTaint, controlPlane}))
	})

	It("leaves the Job and preflight Pod without tolerations when none are set", func() {
		nodeOp.Spec.Tolerations = nil
		Expect(reconciler.createStandardJobSpec(nodeOp, node, 6).Template.Spec.Tolerations).To(BeNil())
		Expect(reconciler.createRebootJobSpec(nodeOp, node, 6).Template.Spec.Tolerations).To(BeNil())
		Expect(reconciler.buildPreflightPod(nodeOp, node).Spec.Tolerations).To(BeNil())
	})

	Describe("rebootPodTolerations", func() {
		It("keeps only the infinite NoExecute tolerations when none are set", func() {
			Expect(rebootPodTolerations(nil)).To(Equal([]corev1.Toleration{notReadyInfinite, unreachableInfinite}))
		})

		It("appends the NodeOp tolerations after the infinite ones", func() {
			Expect(rebootPodTolerations([]corev1.Toleration{customTaint, controlPlane})).To(Equal(
				[]corev1.Toleration{notReadyInfinite, unreachableInfinite, customTaint, controlPlane}))
		})

		It("does not duplicate tolerations", func() {
			Expect(rebootPodTolerations([]corev1.Toleration{customTaint, notReadyInfinite, customTaint})).To(Equal(
				[]corev1.Toleration{notReadyInfinite, unreachableInfinite, customTaint}))
		})

		It("drops a NodeOp NoExecute toleration for not-ready or unreachable that would bound the reboot", func() {
			bounded := []corev1.Toleration{
				{
					Key:               corev1.TaintNodeNotReady,
					Operator:          corev1.TolerationOpExists,
					Effect:            corev1.TaintEffectNoExecute,
					TolerationSeconds: seconds(300),
				},
				{
					Key:               corev1.TaintNodeUnreachable,
					Operator:          corev1.TolerationOpExists,
					Effect:            corev1.TaintEffectNoExecute,
					TolerationSeconds: seconds(300),
				},
			}
			Expect(rebootPodTolerations(bounded)).To(Equal([]corev1.Toleration{notReadyInfinite, unreachableInfinite}))
		})

		It("drops a wildcard toleration with tolerationSeconds that would bound the reboot", func() {
			bounded := []corev1.Toleration{
				{
					Operator:          corev1.TolerationOpExists,
					Effect:            corev1.TaintEffectNoExecute,
					TolerationSeconds: seconds(300),
				},
				{
					Operator:          corev1.TolerationOpExists,
					TolerationSeconds: seconds(300),
				},
				{
					Key:               corev1.TaintNodeNotReady,
					Operator:          corev1.TolerationOpExists,
					TolerationSeconds: seconds(300),
				},
			}
			Expect(rebootPodTolerations(bounded)).To(Equal([]corev1.Toleration{notReadyInfinite, unreachableInfinite}))
		})

		It("keeps tolerations that cannot bound the reboot", func() {
			wildcardForever := corev1.Toleration{Operator: corev1.TolerationOpExists}
			otherNoExecute := corev1.Toleration{
				Key:               "dedicated",
				Operator:          corev1.TolerationOpExists,
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: seconds(300),
			}
			Expect(rebootPodTolerations([]corev1.Toleration{wildcardForever, otherNoExecute})).To(Equal(
				[]corev1.Toleration{notReadyInfinite, unreachableInfinite, wildcardForever, otherNoExecute}))
		})

		It("does not modify the NodeOp tolerations", func() {
			Expect(rebootPodTolerations(nodeOp.Spec.Tolerations)).To(HaveLen(4))
			Expect(nodeOp.Spec.Tolerations).To(Equal([]corev1.Toleration{customTaint, controlPlane}))
		})
	})

	It("is stored by the API server", func() {
		stored := nodeOp.DeepCopy()
		stored.Name = "tolerations-" + randStringRunes(6)
		stored.Spec.Image = "busybox"
		Expect(k8sClient.Create(ctx, stored)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, stored)).To(Succeed()) })

		got := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: stored.Name, Namespace: stored.Namespace}, got)).To(Succeed())
		Expect(got.Spec.Tolerations).To(Equal([]corev1.Toleration{customTaint, controlPlane}))
	})
})
