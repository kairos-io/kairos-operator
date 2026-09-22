package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("drainNode grace period", func() {
	const drainedNodeName = "drain-grace-node"

	var (
		node      *corev1.Node
		requested []*client.DeleteOptions
		reconcler *NodeOpReconciler
	)

	// asInt32 mirrors asBool: DrainOptions.GracePeriodSeconds is a *int32.
	asInt32 := func(i int32) *int32 { return &i }

	BeforeEach(func() {
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: drainedNodeName}}

		// A Pod with a controller owner, so drainNode does not skip it when
		// DrainOptions.Force is false.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workload",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "workload",
					UID:        "11111111-1111-1111-1111-111111111111",
					Controller: asBool(true),
				}},
			},
			Spec: corev1.PodSpec{NodeName: drainedNodeName},
		}

		requested = nil
		c := fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(node, pod).
			WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					// Record what the DELETE request actually carries, which is
					// the only place a grace period can take effect.
					o := &client.DeleteOptions{}
					o.ApplyOptions(opts)
					requested = append(requested, o)
					return cl.Delete(ctx, obj, opts...)
				},
			}).
			Build()

		reconcler = &NodeOpReconciler{Client: c, Scheme: scheme.Scheme}
	})

	drain := func(opts *kairosiov1alpha1.DrainOptions) {
		Expect(reconcler.drainNode(context.Background(), node, opts)).To(Succeed())
		Expect(requested).To(HaveLen(1), "the workload Pod should have been evicted")
	}

	It("sends the requested grace period with the eviction", func() {
		drain(&kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(30)})
		Expect(requested[0].GracePeriodSeconds).ToNot(BeNil())
		Expect(*requested[0].GracePeriodSeconds).To(Equal(int64(30)))
	})

	It("sends a zero grace period, rather than treating it as unset", func() {
		drain(&kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(0)})
		Expect(requested[0].GracePeriodSeconds).ToNot(BeNil())
		Expect(*requested[0].GracePeriodSeconds).To(Equal(int64(0)))
	})

	It("sends no grace period when none is configured", func() {
		drain(&kairosiov1alpha1.DrainOptions{})
		Expect(requested[0].GracePeriodSeconds).To(BeNil())
	})

	It("sends no grace period when a negative one is configured", func() {
		// The CRD documents a negative value as "use the grace period the Pod
		// declares", which is what omitting it from the request produces.
		drain(&kairosiov1alpha1.DrainOptions{GracePeriodSeconds: asInt32(-1)})
		Expect(requested[0].GracePeriodSeconds).To(BeNil())
	})
})
