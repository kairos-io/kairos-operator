package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// roleHidingReader answers NotFound for the first hideRoleReads Role reads and
// delegates everything else. That is what a reader that has not caught up with
// the cluster looks like from inside ensureRebootRole, and it is the only way
// the function can reach its create path while the Role is already there.
type roleHidingReader struct {
	client.Reader
	hideRoleReads int
	roleReads     int
}

func (r *roleHidingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isRole := obj.(*rbacv1.Role); isRole {
		r.roleReads++
		if r.roleReads <= r.hideRoleReads {
			return apierrors.NewNotFound(schema.GroupResource{Group: rbacv1.GroupName, Resource: "roles"}, key.Name)
		}
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// roleWriteSpy counts the Role updates ensureRebootRole issues, and can make
// the first of them lose to a writer that moved the object in between.
type roleWriteSpy struct {
	client.Client
	roleUpdates   int
	conflictFirst int
}

func (c *roleWriteSpy) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, isRole := obj.(*rbacv1.Role); isRole {
		c.roleUpdates++
		if c.roleUpdates <= c.conflictFirst {
			return apierrors.NewConflict(
				schema.GroupResource{Group: rbacv1.GroupName, Resource: "roles"},
				obj.GetName(),
				fmt.Errorf("the object has been modified"),
			)
		}
	}
	return c.Client.Update(ctx, obj, opts...)
}

var _ = Describe("ensureRebootRBAC", func() {
	var (
		ctx        context.Context
		reconciler *NodeOpReconciler
		nodeOp     *kairosiov1alpha1.NodeOp
		namespace  string
		roleKey    types.NamespacedName
		counter    int
	)

	// role reads the reboot Role from the API server.
	role := func() *rbacv1.Role {
		GinkgoHelper()
		fetched := &rbacv1.Role{}
		Expect(k8sClient.Get(ctx, roleKey, fetched)).To(Succeed())
		return fetched
	}

	// createNarrowRole puts a Role under the reboot Role's name carrying less
	// than this operator version grants, which is what an operator version
	// that only knew about the upgrade Job would have written.
	createNarrowRole := func() {
		GinkgoHelper()
		Expect(k8sClient.Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: roleKey.Name, Namespace: roleKey.Namespace},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get"}},
			},
		})).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		counter++
		namespace = fmt.Sprintf("reboot-rbac-%d", counter)

		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		})).To(Succeed())

		nodeOp = &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: "upgrade", Namespace: namespace},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   "busybox",
				Command: []string{"true"},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		roleKey = types.NamespacedName{Name: rebootRBACName(nodeOp), Namespace: namespace}
		reconciler = &NodeOpReconciler{
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			APIReader: k8sClient,
		}
	})

	It("creates the Role, the ServiceAccount and the RoleBinding the reboot Pod runs under", func() {
		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(role().Rules).To(Equal(rebootRoleRules()))
		Expect(role().OwnerReferences).To(HaveLen(1))
		Expect(role().OwnerReferences[0].Name).To(Equal(nodeOp.Name))

		Expect(k8sClient.Get(ctx, roleKey, &corev1.ServiceAccount{})).To(Succeed())
		Expect(k8sClient.Get(ctx, roleKey, &rbacv1.RoleBinding{})).To(Succeed())
	})

	It("converges a Role whose rules an earlier operator version wrote", func() {
		createNarrowRole()

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(role().Rules).To(Equal(rebootRoleRules()))
	})

	It("does not write a Role that already carries these rules", func() {
		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())
		spy := &roleWriteSpy{Client: k8sClient}
		reconciler.Client = spy

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		// Kubernetes drops a no-op update without bumping the resource
		// version, so the cost of writing unconditionally is not visible on
		// the object: it is one more API call per reconcile, per NodeOp.
		Expect(spy.roleUpdates).To(BeZero())
	})

	It("converges a Role a concurrent writer moved under it", func() {
		createNarrowRole()
		spy := &roleWriteSpy{Client: k8sClient, conflictFirst: 1}
		reconciler.Client = spy

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(role().Rules).To(Equal(rebootRoleRules()))
		Expect(spy.roleUpdates).To(Equal(2))
	})

	It("converges the Role it lost the create race for", func() {
		createNarrowRole()
		reconciler.APIReader = &roleHidingReader{Reader: k8sClient, hideRoleReads: 1}

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(role().Rules).To(Equal(rebootRoleRules()))
	})

	It("gives up instead of looping when it never gets to see the Role", func() {
		createNarrowRole()
		reader := &roleHidingReader{Reader: k8sClient, hideRoleReads: ensureRebootRoleAttempts + 1}
		reconciler.APIReader = reader

		err := reconciler.ensureRebootRBAC(ctx, nodeOp)

		Expect(err).To(MatchError(ContainSubstring("after 5 attempts")))
		Expect(reader.roleReads).To(Equal(ensureRebootRoleAttempts))
	})
})
