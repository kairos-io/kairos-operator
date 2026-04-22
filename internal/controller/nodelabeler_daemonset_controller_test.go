package controller

import (
	"context"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("NodeLabelerDaemonSet Controller", func() {
	var (
		ctx       context.Context
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "default"
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", namespace)).To(Succeed())
		Expect(os.Setenv("NODE_LABELER_IMAGE", "quay.io/kairos/operator-node-labeler:v0.0.1")).To(Succeed())
	})

	AfterEach(func() {
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())
		Expect(os.Unsetenv("NODE_LABELER_IMAGE")).To(Succeed())

		ds := &appsv1.DaemonSet{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, ds)
		if err == nil {
			Expect(k8sClient.Delete(ctx, ds)).To(Succeed())
		}
	})

	reconcileOnce := func(ctx context.Context) error {
		r := &NodeLabelerDaemonSetReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      kairosNodeLabelerDaemonSetName,
				Namespace: namespace,
			},
		})
		return err
	}

	It("should create a DaemonSet targeting Kairos nodes", func() {
		By("reconciling")
		Expect(reconcileOnce(ctx)).To(Succeed())

		By("verifying the DaemonSet was created")
		ds := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, ds)).To(Succeed())

		By("verifying the node selector targets only Kairos nodes")
		Expect(ds.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("kairos.io/managed", "true"))

		By("verifying it tolerates all taints")
		Expect(ds.Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
			Operator: corev1.TolerationOpExists,
		}))

		By("verifying the container spec")
		Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
		container := ds.Spec.Template.Spec.Containers[0]
		Expect(container.Name).To(Equal("node-labeler"))
		Expect(container.Image).To(Equal("quay.io/kairos/operator-node-labeler:v0.0.1"))
		Expect(container.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))

		By("verifying the labeler runs in loop mode")
		Expect(container.Args).To(ContainElements("--every", "60"))

		By("verifying the NODE_NAME env var is injected from the pod spec")
		Expect(container.Env).To(ContainElement(corev1.EnvVar{
			Name: "NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "spec.nodeName",
				},
			},
		}))

		By("verifying the security context")
		Expect(container.SecurityContext).NotTo(BeNil())
		Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
		Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(1000)))

		By("verifying the host /etc volume mount")
		Expect(container.VolumeMounts).To(HaveLen(1))
		Expect(container.VolumeMounts[0].Name).To(Equal("host-etc"))
		Expect(container.VolumeMounts[0].MountPath).To(Equal("/host/etc"))
		Expect(container.VolumeMounts[0].ReadOnly).To(BeTrue())

		By("verifying the host /etc volume")
		Expect(ds.Spec.Template.Spec.Volumes).To(HaveLen(1))
		Expect(ds.Spec.Template.Spec.Volumes[0].Name).To(Equal("host-etc"))
		Expect(ds.Spec.Template.Spec.Volumes[0].HostPath.Path).To(Equal("/etc"))
	})

	It("should not create a duplicate DaemonSet on subsequent reconciliations", func() {
		Expect(reconcileOnce(ctx)).To(Succeed())
		Expect(reconcileOnce(ctx)).To(Succeed())

		dsList := &appsv1.DaemonSetList{}
		Expect(k8sClient.List(ctx, dsList,
			client.InNamespace(namespace),
			client.MatchingLabels(map[string]string{"app": "kairos-node-labeler"}),
		)).To(Succeed())
		Expect(dsList.Items).To(HaveLen(1))
	})

	It("should update the DaemonSet image when the operator restarts with a new image", func() {
		r := &NodeLabelerDaemonSetReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}

		By("simulating the first operator startup with v0.0.1")
		Expect(os.Setenv("NODE_LABELER_IMAGE", "quay.io/kairos/operator-node-labeler:v0.0.1")).To(Succeed())
		Expect(r.ensureDaemonSetOnStartup(ctx, namespace)).To(Succeed())

		By("simulating an operator upgrade: restart with v0.0.2")
		Expect(os.Setenv("NODE_LABELER_IMAGE", "quay.io/kairos/operator-node-labeler:v0.0.2")).To(Succeed())
		Expect(r.ensureDaemonSetOnStartup(ctx, namespace)).To(Succeed())

		By("verifying the DaemonSet now uses the new image")
		ds := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, ds)).To(Succeed())
		Expect(ds.Spec.Template.Spec.Containers[0].Image).To(Equal("quay.io/kairos/operator-node-labeler:v0.0.2"))
	})

	It("should recreate the DaemonSet if it is deleted", func() {
		By("creating the DaemonSet via reconciliation")
		Expect(reconcileOnce(ctx)).To(Succeed())

		By("deleting the DaemonSet")
		ds := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, ds)).To(Succeed())
		Expect(k8sClient.Delete(ctx, ds)).To(Succeed())

		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, &appsv1.DaemonSet{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		By("reconciling again")
		Expect(reconcileOnce(ctx)).To(Succeed())

		By("verifying the DaemonSet was recreated")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kairosNodeLabelerDaemonSetName, Namespace: namespace}, &appsv1.DaemonSet{})).To(Succeed())
	})
})
