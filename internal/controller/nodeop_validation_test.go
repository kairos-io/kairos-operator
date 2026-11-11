package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// A negative backoffLimit or a zero preflight deadline only fails when the
// controller builds the Job or the preflight Pod, which happens after the node
// is already cordoned (kairos-io/kairos#6034). The CRD must refuse them at
// admission so the controller never sees them.
var _ = Describe("NodeOp CRD validation", func() {
	newNodeOp := func(mutate func(*kairosiov1alpha1.NodeOp)) *kairosiov1alpha1.NodeOp {
		n := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "validation-" + randStringRunes(6),
				Namespace: "default",
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command: []string{"true"},
				Image:   "busybox",
			},
		}
		mutate(n)
		return n
	}
	i32 := func(v int32) *int32 { return &v }

	DescribeTable("backoffLimit",
		func(v int32, accepted bool) {
			n := newNodeOp(func(n *kairosiov1alpha1.NodeOp) { n.Spec.BackoffLimit = i32(v) })
			err := k8sClient.Create(ctx, n)
			if accepted {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(ctx, n)).To(Succeed())
				return
			}
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), fmt.Sprintf("want Invalid, got %v", err))
			Expect(err.Error()).To(ContainSubstring("backoffLimit"))
		},
		Entry("-1 is rejected", int32(-1), false),
		Entry("0 is accepted", int32(0), true),
		Entry("1 is accepted", int32(1), true),
	)

	DescribeTable("preflight.activeDeadlineSeconds",
		func(v int32, accepted bool) {
			n := newNodeOp(func(n *kairosiov1alpha1.NodeOp) {
				n.Spec.Preflight = &kairosiov1alpha1.PreflightSpec{
					Command:               []string{"true"},
					ActiveDeadlineSeconds: i32(v),
				}
			})
			err := k8sClient.Create(ctx, n)
			if accepted {
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Delete(ctx, n)).To(Succeed())
				return
			}
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), fmt.Sprintf("want Invalid, got %v", err))
			Expect(err.Error()).To(ContainSubstring("activeDeadlineSeconds"))
		},
		Entry("0 is rejected", int32(0), false),
		Entry("-1 is rejected", int32(-1), false),
		Entry("1 is accepted", int32(1), true),
	)
})
