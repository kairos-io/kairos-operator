package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	"github.com/kairos-io/kairos-operator/internal/utils"
)

// longName returns a valid object name (DNS subdomain, up to 253 characters)
// that is too long to be used as a label value (capped at 63).
func longName(prefix string) string {
	name := fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixNano(), strings.Repeat("x", 40))
	Expect(len(name)).To(BeNumerically(">", utils.KubernetesLabelValueLengthLimit))
	return name
}

var _ = Describe("Names longer than a label value", func() {
	const namespace = "default"

	Context("When a NodeOp name does not fit in a label value", func() {
		var (
			ctx        context.Context
			nodeName   string
			nodeOpName string
			node       *corev1.Node
			nodeOp     *kairosiov1alpha1.NodeOp
		)

		BeforeEach(func() {
			ctx = context.Background()
			nodeName = fmt.Sprintf("test-node-%d", time.Now().UnixNano())
			nodeOpName = longName("upgrade-to-hadron")

			node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: map[string]string{"kairos.io/managed": "true"},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			nodeOp = &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: nodeOpName, Namespace: namespace},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kairos.io/managed": "true"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed(),
				"the API server accepts a name this long, so the operator has to as well")
		})

		AfterEach(func() {
			_ = k8sClient.Delete(ctx, nodeOp)
			_ = k8sClient.Delete(ctx, node)
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace(namespace))).To(Succeed())
			propagation := metav1.DeletePropagationBackground
			for i := range jobList.Items {
				_ = k8sClient.Delete(ctx, &jobList.Items[i], &client.DeleteOptions{PropagationPolicy: &propagation})
			}
		})

		It("still creates the per-node Job", func() {
			_, err := (&NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}).
				Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: nodeOpName, Namespace: namespace},
				})
			Expect(err).NotTo(HaveOccurred())

			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(namespace),
				client.MatchingLabels{labelKeyNodeOp: utils.TruncateLabelValue(nodeOpName)},
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))

			job := jobList.Items[0]
			Expect(job.Labels[labelKeyNodeOp]).To(HaveLen(utils.KubernetesLabelValueLengthLimit))
			Expect(job.Labels[labelKeyNode]).To(Equal(nodeName))
			Expect(job.OwnerReferences).To(HaveLen(1))
			Expect(job.OwnerReferences[0].Name).To(Equal(nodeOpName),
				"the owner reference keeps the untruncated name")
		})

		It("maps a child Pod back to the untruncated NodeOp name", func() {
			r := &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name:      "reboot-pod",
				Namespace: namespace,
				Labels: map[string]string{
					labelKeyReboot: "true",
					labelKeyNodeOp: utils.TruncateLabelValue(nodeOpName),
				},
			}}
			Expect(controllerutil.SetControllerReference(nodeOp, pod, r.Scheme)).To(Succeed())

			reqs := r.findNodeOpsForRebootPod(ctx, pod)
			Expect(reqs).To(HaveLen(1))
			Expect(reqs[0].NamespacedName.Name).To(Equal(nodeOpName))

			pod.Labels = map[string]string{
				labelKeyPreflight: "true",
				labelKeyNodeOp:    utils.TruncateLabelValue(nodeOpName),
			}
			reqs = r.findNodeOpsForPreflightPod(ctx, pod)
			Expect(reqs).To(HaveLen(1))
			Expect(reqs[0].NamespacedName.Name).To(Equal(nodeOpName))
		})
	})

	Context("When a node name does not fit in a label value", func() {
		var (
			ctx      context.Context
			nodeName string
			node     *corev1.Node
		)

		BeforeEach(func() {
			ctx = context.Background()
			Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", namespace)).To(Succeed())
			nodeName = longName("worker.rack12.dc-frankfurt.prod.example.com")

			node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed(),
				"the API server accepts a node name this long")
		})

		AfterEach(func() {
			Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())
			_ = k8sClient.Delete(ctx, node)
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace(namespace))).To(Succeed())
			propagation := metav1.DeletePropagationBackground
			for i := range jobList.Items {
				_ = k8sClient.Delete(ctx, &jobList.Items[i], &client.DeleteOptions{PropagationPolicy: &propagation})
			}
		})

		It("creates the labeler Job once and finds it again", func() {
			r := &NodeLabelerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: nodeName}}

			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(namespace),
				client.MatchingLabels{labelKeyJobNode: utils.TruncateLabelValue(nodeName)},
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(jobList.Items[0].Spec.Template.Spec.NodeName).To(Equal(nodeName),
				"the Pod is still pinned to the untruncated node name")

			// The second reconcile must recognise the Job it just created.
			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.List(ctx, jobList, client.InNamespace(namespace))).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
		})
	})
})
