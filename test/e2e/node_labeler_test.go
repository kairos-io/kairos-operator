package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Node Labeler E2E", func() {

	FIt("should label only the node with /etc/kairos-release", func() {
		By("injecting /etc/kairos-release into one node")
		out, err := exec.Command("kind", "get", "nodes", "--name", clusterName).CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		nodes := strings.Fields(string(out))
		Expect(len(nodes)).To(Equal(3))
		// Inject kairos-release into the first node
		cmd := exec.Command("docker", "exec", nodes[0], "bash", "-c", "echo 'kairos' > /etc/kairos-release")
		_, err = cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())

		By("deploying the operator and node labeler")
		cmd = exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-k", "config/default")
		out, err = cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))

		By("creating a Job for each node")
		nodelist, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())

		for _, node := range nodelist.Items {
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("kairos-node-labeler-%s", node.Name),
					Namespace: "operator-system",
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeName:      node.Name,
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:  "node-labeler",
									Image: "kairos/node-labeler:latest",
									SecurityContext: &corev1.SecurityContext{
										RunAsNonRoot: &[]bool{true}[0],
										RunAsUser:    &[]int64{1000}[0],
									},
									Env: []corev1.EnvVar{
										{
											Name: "NODE_NAME",
											ValueFrom: &corev1.EnvVarSource{
												FieldRef: &corev1.ObjectFieldSelector{
													FieldPath: "spec.nodeName",
												},
											},
										},
									},
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "kairos-release",
											MountPath: "/etc/kairos-release",
											ReadOnly:  true,
										},
										{
											Name:      "os-release",
											MountPath: "/etc/os-release",
											ReadOnly:  true,
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "kairos-release",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{
											Path: "/etc/kairos-release",
										},
									},
								},
								{
									Name: "os-release",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{
											Path: "/etc/os-release",
										},
									},
								},
							},
							ServiceAccountName: "operator-kairos-node-labeler",
						},
					},
				},
			}
			_, err = clientset.BatchV1().Jobs("operator-system").Create(context.TODO(), job, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
		}

		By("waiting for Jobs to complete")
		Eventually(func() bool {
			jobs, err := clientset.BatchV1().Jobs("operator-system").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				return false
			}
			for _, job := range jobs.Items {
				if job.Status.Succeeded == 0 {
					return false
				}
			}
			return true
		}, 2*time.Minute, 5*time.Second).Should(BeTrue())

		By("checking node labels")
		nodelist, err = clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		var labeled, unlabeled int
		for _, node := range nodelist.Items {
			if node.Labels["kairos.io/managed"] == "true" {
				labeled++
			} else {
				unlabeled++
			}
		}
		Expect(labeled).To(Equal(1))
		Expect(unlabeled).To(Equal(1))
	})
})
