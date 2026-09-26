package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("ensureClusterRBAC", func() {
	var reconciler *NodeOpReconciler

	readRole := func() *rbacv1.ClusterRole {
		role := &rbacv1.ClusterRole{}
		Expect(reconciler.Get(context.Background(), ktypes.NamespacedName{Name: rebootClusterRoleName}, role)).To(Succeed())
		return role
	}

	When("the ClusterRole does not exist yet", func() {
		It("creates it with the rules this operator version declares", func() {
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

			Expect(reconciler.ensureClusterRBAC(context.Background())).To(Succeed())
			Expect(readRole().Rules).To(Equal(rebootClusterRoleRules()))
		})
	})

	When("an older operator version left a narrower ClusterRole behind", func() {
		It("converges the rules on the ones this version declares", func() {
			stale := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: rebootClusterRoleName},
				Rules: []rbacv1.PolicyRule{
					{APIGroups: []string{""}, Resources: []string{rbacResourcePods}, Verbs: []string{rbacVerbGet}},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(stale).Build()
			reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

			Expect(reconciler.ensureClusterRBAC(context.Background())).To(Succeed())
			Expect(readRole().Rules).To(Equal(rebootClusterRoleRules()))
		})
	})

	When("an administrator widened the ClusterRole by hand", func() {
		It("still converges it, so the grant matches what the reboot Pod needs", func() {
			widened := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: rebootClusterRoleName},
				Rules: []rbacv1.PolicyRule{
					{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(widened).Build()
			reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

			Expect(reconciler.ensureClusterRBAC(context.Background())).To(Succeed())
			Expect(readRole().Rules).To(Equal(rebootClusterRoleRules()))
		})
	})

	When("the ClusterRole already carries the declared rules", func() {
		It("leaves the object untouched", func() {
			current := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: rebootClusterRoleName},
				Rules:      rebootClusterRoleRules(),
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(current).Build()
			reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

			before := readRole().ResourceVersion
			Expect(reconciler.ensureClusterRBAC(context.Background())).To(Succeed())
			Expect(readRole().ResourceVersion).To(Equal(before))
		})
	})

	When("a reboot Pod's ServiceAccount is prepared", func() {
		It("converges the ClusterRole on the way, not only at operator start-up", func() {
			stale := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: rebootClusterRoleName},
				Rules: []rbacv1.PolicyRule{
					{APIGroups: []string{""}, Resources: []string{rbacResourcePods}, Verbs: []string{rbacVerbGet}},
				},
			}
			nodeOp := newNodeOpForRBAC("upgrade", "kairos-system")
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(stale, nodeOp).Build()
			reconciler = &NodeOpReconciler{Client: cl, Scheme: scheme.Scheme}

			Expect(reconciler.ensureNodeOpServiceAccount(context.Background(), nodeOp)).To(Succeed())
			Expect(readRole().Rules).To(Equal(rebootClusterRoleRules()))
		})
	})
})

func newNodeOpForRBAC(name, namespace string) *kairosiov1alpha1.NodeOp {
	return &kairosiov1alpha1.NodeOp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       ktypes.UID(name + "-uid"),
		},
	}
}

var _ = Describe("ensureClusterRBAC before the manager is started", func() {
	// SetupWithManager converges the ClusterRole while the manager is still
	// being built, so the read cannot go through the manager's cache: an
	// unstarted cache answers every Get with ErrCacheNotStarted, and the
	// caller there is an os.Exit(1). The operator Pod would then crash-loop
	// and never become ready.
	It("reads the ClusterRole without the cache the manager has not started", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  scheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler := &NodeOpReconciler{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			APIReader: mgr.GetAPIReader(),
		}

		Expect(reconciler.ensureClusterRBAC(context.Background())).To(Succeed())

		role := &rbacv1.ClusterRole{}
		Expect(k8sClient.Get(context.Background(),
			ktypes.NamespacedName{Name: rebootClusterRoleName}, role)).To(Succeed())
		Expect(role.Rules).To(Equal(rebootClusterRoleRules()))

		Expect(k8sClient.Delete(context.Background(), role)).To(Succeed())
	})
})
