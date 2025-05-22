package e2e

import (
	"context"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Node Labeler E2E", func() {

	FIt("should label only the node with /etc/kairos-release", func() {
		By("injecting /etc/kairos-release into one node")
		out, err := exec.Command("kind", "get", "nodes", "--name", clusterName).CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		nodes := strings.Fields(string(out))
		Expect(len(nodes)).To(Equal(2))
		// Inject kairos-release into the first node
		cmd := exec.Command("docker", "exec", nodes[0], "bash", "-c", "echo 'kairos' > /etc/kairos-release")
		out, err = cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		By("deploying the operator and node labeler")
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-k", "config/default")
		out, err = cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		By("waiting for operator pod to be running")
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods("operator-system").List(context.TODO(), metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=kairos-operator,app.kubernetes.io/component=operator",
			})
			if err != nil {
				return false
			}
			if len(pods.Items) == 0 {
				return false
			}
			pod := pods.Items[0]
			return pod.Status.Phase == corev1.PodRunning &&
				len(pod.Status.ContainerStatuses) > 0 &&
				pod.Status.ContainerStatuses[0].Ready
		}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "Operator pod should be running")

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
		cmd = exec.Command("docker", "exec", nodes[0], "hostname")
		out, err = cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		expectedManagedNode := strings.TrimSpace(string(out))

		var labeled, unlabeled int
		for _, node := range nodelist.Items {
			if node.Labels["kairos.io/managed"] == "true" {
				labeled++
				Expect(node.Name).To(Equal(expectedManagedNode), "The node labeled as managed should be the one with kairos-release")
			} else {
				unlabeled++
			}
		}
		Expect(labeled).To(Equal(1))
		Expect(unlabeled).To(Equal(1))
	})
})
