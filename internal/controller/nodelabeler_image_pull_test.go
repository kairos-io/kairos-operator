package controller

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The node-labeler image is a different image from the manager, and the Job and
// the DaemonSet that run it are built by the operator rather than templated by
// the chart. These specs pin the two chart values that reach them.
var _ = Describe("Node-labeler image pull settings", func() {
	var (
		node *corev1.Node
		r    *NodeLabelerReconciler
		d    *NodeLabelerDaemonSetReconciler
	)

	BeforeEach(func() {
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
		r = &NodeLabelerReconciler{}
		d = &NodeLabelerDaemonSetReconciler{}
	})

	AfterEach(func() {
		Expect(os.Unsetenv(nodeLabelerPullPolicyEnv)).To(Succeed())
		Expect(os.Unsetenv(nodeLabelerPullSecretsEnv)).To(Succeed())
	})

	podSpecs := func() []corev1.PodSpec {
		return []corev1.PodSpec{
			r.createNodeLabelerJob(node, "kairos-system").Spec.Template.Spec,
			d.buildDaemonSet("kairos-system").Spec.Template.Spec,
		}
	}

	It("keeps IfNotPresent and no pull secrets when the chart sets neither", func() {
		for _, spec := range podSpecs() {
			Expect(spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
			Expect(spec.ImagePullSecrets).To(BeEmpty())
		}
	})

	It("uses the pull policy the chart asks for", func() {
		Expect(os.Setenv(nodeLabelerPullPolicyEnv, string(corev1.PullAlways))).To(Succeed())

		for _, spec := range podSpecs() {
			Expect(spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
		}
	})

	It("falls back to IfNotPresent when the pull policy is not a known one", func() {
		Expect(os.Setenv(nodeLabelerPullPolicyEnv, "Sometimes")).To(Succeed())

		for _, spec := range podSpecs() {
			Expect(spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
		}
	})

	It("carries the chart's imagePullSecrets onto both Pods", func() {
		Expect(os.Setenv(nodeLabelerPullSecretsEnv, "regcred, mirror-creds ,")).To(Succeed())

		for _, spec := range podSpecs() {
			Expect(spec.ImagePullSecrets).To(Equal([]corev1.LocalObjectReference{
				{Name: "regcred"},
				{Name: "mirror-creds"},
			}))
		}
	})
})
