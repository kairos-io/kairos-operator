package e2e

import (
	"context"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // dot-imports are standard for ginkgo tests
	. "github.com/onsi/gomega"    //nolint:revive // dot-imports are standard for ginkgo tests
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Node Labeler E2E", func() {
	It("should label only the node with /etc/kairos-release", func() {
		By("waiting for labeler jobs to be created")
		Eventually(func() bool {
			jobs, err := clientset.BatchV1().Jobs("operator-system").List(context.TODO(), metav1.ListOptions{
				LabelSelector: "app=kairos-node-labeler",
			})
			if err != nil {
				return false
			}
			return len(jobs.Items) == 2
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Two labeler jobs should be created")

		By("waiting for labeler jobs to complete successfully")
		Eventually(func() bool {
			jobs, err := clientset.BatchV1().Jobs("operator-system").List(context.TODO(), metav1.ListOptions{
				LabelSelector: "app=kairos-node-labeler",
			})
			if err != nil {
				return false
			}
			if len(jobs.Items) != 2 {
				return false
			}
			for _, job := range jobs.Items {
				if job.Status.Succeeded == 0 {
					return false
				}
			}
			return true
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Both labeler jobs should complete successfully")

		By("checking node labels")
		nodelist, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())

		// Get the name of the node where we injected kairos-release
		cmd := exec.Command("docker", "exec", kairosNode, "hostname")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		expectedManagedNode := strings.TrimSpace(string(out))

		var labeled, unlabeled int
		for _, node := range nodelist.Items {
			if node.Labels["kairos.io/managed"] == "true" {
				labeled++
				Expect(node.Name).To(Equal(expectedManagedNode),
					"The node labeled as managed should be the one with kairos-release")
			} else {
				unlabeled++
			}
		}
		Expect(labeled).To(Equal(1))
		Expect(unlabeled).To(Equal(1))
	})

	It("should create a DaemonSet that runs only on Kairos nodes", func() {
		By("verifying the DaemonSet exists in the operator namespace")
		Eventually(func() error {
			_, err := clientset.AppsV1().DaemonSets(namespace).Get(context.TODO(), "kairos-node-labeler", metav1.GetOptions{})
			return err
		}, 2*time.Minute, 5*time.Second).Should(Succeed(), "kairos-node-labeler DaemonSet should be created")

		ds, err := clientset.AppsV1().DaemonSets(namespace).Get(context.TODO(), "kairos-node-labeler", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())

		By("verifying the DaemonSet targets only Kairos nodes via node selector")
		Expect(ds.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("kairos.io/managed", "true"))

		By("verifying the DaemonSet runs the labeler in loop mode")
		Expect(ds.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(ds.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--every"))

		By("waiting for the DaemonSet pod to be running on the Kairos node")
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
				LabelSelector: "app=kairos-node-labeler,mode=daemon",
			})
			if err != nil || len(pods.Items) == 0 {
				return false
			}
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					return true
				}
			}
			return false
		}, 3*time.Minute, 5*time.Second).Should(BeTrue(), "DaemonSet pod should be running on the Kairos node")

		By("verifying the DaemonSet pod runs only on the Kairos node")
		cmd := exec.Command("docker", "exec", kairosNode, "hostname")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		kairosNodeName := strings.TrimSpace(string(out))

		pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app=kairos-node-labeler,mode=daemon",
		})
		Expect(err).NotTo(HaveOccurred())
		for _, pod := range pods.Items {
			Expect(pod.Spec.NodeName).To(Equal(kairosNodeName),
				"DaemonSet pods must only run on Kairos nodes")
		}
	})
})
