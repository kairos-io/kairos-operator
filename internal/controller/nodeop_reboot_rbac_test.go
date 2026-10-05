package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

// roleUpdateCounter counts the Role updates made through it, so a test can
// check that a correct Role is left untouched.
type roleUpdateCounter struct {
	client.Client
	roleUpdates int
}

func (c *roleUpdateCounter) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, isRole := obj.(*rbacv1.Role); isRole {
		c.roleUpdates++
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

	// createOutdatedRole creates the reboot Role with fewer permissions than
	// this operator version gives it, as an older operator version could have.
	createOutdatedRole := func() {
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

	It("updates a Role that has other permissions", func() {
		createOutdatedRole()

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(role().Rules).To(Equal(rebootRoleRules()))
	})

	It("leaves a Role with the right permissions untouched", func() {
		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())
		updates := &roleUpdateCounter{Client: k8sClient}
		reconciler.Client = updates

		Expect(reconciler.ensureRebootRBAC(ctx, nodeOp)).To(Succeed())

		Expect(updates.roleUpdates).To(BeZero())
	})
})
