package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("Operator owner reference on node-labeler objects", func() {
	const (
		ns         = "kairos-operator"
		deployName = "kairos-operator"
		deployUID  = types.UID("deploy-uid")
		rsName     = "kairos-operator-abc123"
		podName    = "kairos-operator-abc123-xyz"
	)

	var (
		ctx       context.Context
		operatorP *corev1.Pod
		operatorR *appsv1.ReplicaSet
	)

	isTrue := func() *bool { b := true; return &b }

	wantOwner := metav1.OwnerReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Name:       deployName,
		UID:        deployUID,
	}

	newClient := func(extra ...client.Object) client.Client {
		objs := append([]client.Object{operatorP, operatorR}, extra...)
		return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
	}

	BeforeEach(func() {
		ctx = context.Background()
		GinkgoT().Setenv(controllerPodNamespaceEnv, ns)
		GinkgoT().Setenv(controllerPodNameEnv, podName)

		operatorR = &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: rsName, Namespace: ns, UID: "rs-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: deployName, UID: deployUID, Controller: isTrue(),
			}},
		}}
		operatorP = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: podName, Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, UID: "rs-uid", Controller: isTrue(),
			}},
		}}
	})

	It("resolves the Deployment through the operator Pod and ReplicaSet", func() {
		ref, err := operatorOwnerReference(ctx, newClient())
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(Equal(&wantOwner))
	})

	It("returns no reference when CONTROLLER_POD_NAME is unset", func() {
		GinkgoT().Setenv(controllerPodNameEnv, "")
		ref, err := operatorOwnerReference(ctx, newClient())
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(BeNil())
	})

	It("returns no reference when the operator Pod has no ReplicaSet controller", func() {
		operatorP.OwnerReferences = nil
		ref, err := operatorOwnerReference(ctx, newClient())
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(BeNil())
	})

	It("fails when the operator Pod cannot be read, so the startup task retries", func() {
		c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		_, err := operatorOwnerReference(ctx, c)
		Expect(err).To(HaveOccurred())
	})

	It("owns the DaemonSet it creates and adopts one left by an older operator", func() {
		By("creating it")
		c := newClient()
		r := &NodeLabelerDaemonSetReconciler{Client: c, Scheme: scheme.Scheme}
		Expect(r.ensureDaemonSetOnStartup(ctx, c, ns)).To(Succeed())
		ds := &appsv1.DaemonSet{}
		Expect(c.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: ns}, ds)).To(Succeed())
		Expect(ds.OwnerReferences).To(ConsistOf(wantOwner))

		By("creating it from Reconcile")
		c = newClient()
		r = &NodeLabelerDaemonSetReconciler{Client: c, Scheme: scheme.Scheme}
		Expect(r.ensureDaemonSet(ctx, ns)).To(Succeed())
		Expect(c.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: ns}, ds)).To(Succeed())
		Expect(ds.OwnerReferences).To(ConsistOf(wantOwner))

		By("adopting an unowned one on startup")
		old := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: kairosNodeLabelerDaemonSetName, Namespace: ns}}
		c = newClient(old)
		r = &NodeLabelerDaemonSetReconciler{Client: c, Scheme: scheme.Scheme}
		Expect(r.ensureDaemonSetOnStartup(ctx, c, ns)).To(Succeed())
		Expect(c.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: ns}, ds)).To(Succeed())
		Expect(ds.OwnerReferences).To(ConsistOf(wantOwner))
	})

	It("owns the ServiceAccount it creates and adopts one left by an older operator", func() {
		sa := &corev1.ServiceAccount{}
		key := types.NamespacedName{Name: nodeLabelerServiceAccount, Namespace: ns}

		c := newClient()
		r := &NodeLabelerReconciler{Client: c, Scheme: scheme.Scheme}
		Expect(r.ensureServiceAccount(ctx, ns)).To(Succeed())
		Expect(c.Get(ctx, key, sa)).To(Succeed())
		Expect(sa.OwnerReferences).To(ConsistOf(wantOwner))

		By("adopting an unowned one, and not duplicating the reference on the next start")
		c = newClient(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: nodeLabelerServiceAccount, Namespace: ns}})
		r = &NodeLabelerReconciler{Client: c, Scheme: scheme.Scheme}
		Expect(r.ensureServiceAccount(ctx, ns)).To(Succeed())
		Expect(r.ensureServiceAccount(ctx, ns)).To(Succeed())
		Expect(c.Get(ctx, key, sa)).To(Succeed())
		Expect(sa.OwnerReferences).To(ConsistOf(wantOwner))
	})

	It("owns the per-node Job", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "kairos-node"}}
		c := newClient(node)
		r := &NodeLabelerReconciler{Client: c, Scheme: scheme.Scheme}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
		Expect(err).NotTo(HaveOccurred())

		jobs := &batchv1.JobList{}
		Expect(c.List(ctx, jobs, client.InNamespace(ns))).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		Expect(jobs.Items[0].OwnerReferences).To(ConsistOf(wantOwner))
	})
})
