package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("NodeLabelerReconciler ensureServiceAccount", func() {
	const (
		oldNamespace = "kairos-operator-system"
		newNamespace = "kairos-system"
	)

	var (
		rbacCtx    context.Context
		fakeClient client.Client
		reconciler *NodeLabelerReconciler
	)

	// grants reports whether the node-labeler ClusterRoleBinding lists the
	// ServiceAccount of the given namespace. That, rather than a particular
	// subject index, is what decides whether the labeler Pods can write labels.
	grants := func(namespace string) bool {
		binding := &rbacv1.ClusterRoleBinding{}
		Expect(fakeClient.Get(rbacCtx, types.NamespacedName{Name: nodeLabelerServiceAccount}, binding)).To(Succeed())
		for _, subject := range binding.Subjects {
			if subject == nodeLabelerSubject(namespace) {
				return true
			}
		}
		return false
	}

	clusterRole := func() *rbacv1.ClusterRole {
		role := &rbacv1.ClusterRole{}
		Expect(fakeClient.Get(rbacCtx, types.NamespacedName{Name: nodeLabelerServiceAccount}, role)).To(Succeed())
		return role
	}

	bindingResourceVersion := func() string {
		binding := &rbacv1.ClusterRoleBinding{}
		Expect(fakeClient.Get(rbacCtx, types.NamespacedName{Name: nodeLabelerServiceAccount}, binding)).To(Succeed())
		return binding.ResourceVersion
	}

	// withObjects builds a reconciler over a cluster that already holds the
	// given objects, which is how an operator reinstall sees the world: the
	// cluster-scoped RBAC has no owner reference and no Helm release label, so
	// it outlives the release that created it.
	withObjects := func(objs ...client.Object) {
		fakeClient = fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(objs...).
			Build()
		reconciler = &NodeLabelerReconciler{Client: fakeClient, Scheme: scheme.Scheme}
	}

	existingBinding := func(namespace string) *rbacv1.ClusterRoleBinding {
		return &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount},
			Subjects:   []rbacv1.Subject{nodeLabelerSubject(namespace)},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     clusterRoleKind,
				Name:     nodeLabelerServiceAccount,
			},
		}
	}

	BeforeEach(func() {
		rbacCtx = context.Background()
	})

	Context("on a cluster that holds no node-labeler RBAC yet", func() {
		BeforeEach(func() {
			withObjects()
		})

		It("creates the ClusterRole with the declared rules and binds this namespace", func() {
			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(clusterRole().Rules).To(Equal(nodeLabelerClusterRoleRules()))
			Expect(grants(newNamespace)).To(BeTrue())
		})
	})

	Context("when the operator is reinstalled into a different namespace", func() {
		BeforeEach(func() {
			withObjects(existingBinding(oldNamespace))
		})

		It("grants the ServiceAccount of the namespace it now runs in", func() {
			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(grants(newNamespace)).To(BeTrue(),
				"the node-labeler Job and DaemonSet both run as this ServiceAccount, so without the grant "+
					"no node ever gets kairos.io/managed and every NodeOp matches zero nodes while reporting success")
		})

		It("keeps the grant of the namespace that created the binding", func() {
			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(grants(oldNamespace)).To(BeTrue(),
				"a second operator instance elsewhere is still granted by that subject, and dropping it "+
					"would revoke a running installation's only grant")
		})
	})

	Context("when the binding already grants this namespace", func() {
		BeforeEach(func() {
			withObjects(existingBinding(newNamespace))
		})

		It("does not write the binding again", func() {
			before := bindingResourceVersion()

			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(bindingResourceVersion()).To(Equal(before))
		})
	})

	Context("when the ClusterRole in the cluster no longer matches this operator version", func() {
		narrowed := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get"},
			},
		}

		BeforeEach(func() {
			withObjects(&rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount},
				Rules:      narrowed,
			})
		})

		It("converges the rules on what this version declares", func() {
			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(clusterRole().Rules).To(Equal(nodeLabelerClusterRoleRules()),
				"the ClusterRole outlives the operator Pod, so an upgrade that needs a new verb must not "+
					"keep the grant the first version ever installed wrote")
		})
	})

	Context("when the ClusterRole already matches", func() {
		BeforeEach(func() {
			withObjects(&rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount},
				Rules:      nodeLabelerClusterRoleRules(),
			})
		})

		It("does not write the ClusterRole again", func() {
			before := clusterRole().ResourceVersion

			Expect(reconciler.ensureServiceAccount(rbacCtx, newNamespace)).To(Succeed())

			Expect(clusterRole().ResourceVersion).To(Equal(before))
		})
	})
})
