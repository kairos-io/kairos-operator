/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var _ = Describe("NodeLabeler Controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	Context("When reconciling a node", func() {
		var (
			nodeName string
			ctx      context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			// Set operator namespace to default for testing
			os.Setenv("OPERATOR_NAMESPACE", "default")
			// Generate a unique name for this test
			nodeName = fmt.Sprintf("test-node-%d", time.Now().UnixNano())

			// Create a test node
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up Node
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err == nil {
				Expect(k8sClient.Delete(ctx, node)).To(Succeed())
			}

			// Clean up Jobs
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"))).To(Succeed())
			for _, job := range jobList.Items {
				if job.Labels["node"] == nodeName {
					Expect(k8sClient.Delete(ctx, &job)).To(Succeed())
				}
			}
		})

		It("should create a node-labeler job for a new node", func() {
			By("Reconciling the created node")
			controllerReconciler := &NodeLabelerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconciliation should create a Job
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Job was created
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"node": nodeName,
					"app":  "kairos-node-labeler",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))
			foundJob := &jobList.Items[0]

			// Verify Job has correct labels
			Expect(foundJob.Labels).To(HaveKeyWithValue("app", "kairos-node-labeler"))
			Expect(foundJob.Labels).To(HaveKeyWithValue("node", nodeName))

			// Verify Job has correct container configuration
			Expect(foundJob.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := foundJob.Spec.Template.Spec.Containers[0]
			Expect(container.Name).To(Equal("node-labeler"))
			Expect(container.Image).To(Equal("kairos/node-labeler:latest"))
			Expect(container.ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))

			// Verify security context
			Expect(container.SecurityContext).NotTo(BeNil())
			Expect(*container.SecurityContext.RunAsNonRoot).To(BeTrue())
			Expect(*container.SecurityContext.RunAsUser).To(Equal(int64(1000)))

			// Verify volume mounts
			Expect(container.VolumeMounts).To(HaveLen(2))
			Expect(container.VolumeMounts[0].Name).To(Equal("kairos-release"))
			Expect(container.VolumeMounts[1].Name).To(Equal("os-release"))

			// Verify volumes
			Expect(foundJob.Spec.Template.Spec.Volumes).To(HaveLen(2))
			Expect(foundJob.Spec.Template.Spec.Volumes[0].Name).To(Equal("kairos-release"))
			Expect(foundJob.Spec.Template.Spec.Volumes[1].Name).To(Equal("os-release"))
		})

		It("should not create a new job if one already exists", func() {
			By("Creating an initial job")
			controllerReconciler := &NodeLabelerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconciliation to create the job
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the initial job count
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"node": nodeName,
					"app":  "kairos-node-labeler",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			initialJobCount := len(jobList.Items)

			// Reconcile again
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeName,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the new job count
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"node": nodeName,
					"app":  "kairos-node-labeler",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(jobList.Items)).To(Equal(initialJobCount))
		})
	})
})
