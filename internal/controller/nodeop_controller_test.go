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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOp Controller", func() {
	const (
		NodeOpName      = "test-nodeop"
		NodeOpNamespace = "default"
		timeout         = time.Second * 10
		interval        = time.Millisecond * 250
		kindNodeOp      = "NodeOp"
	)

	Context("When creating a NodeOp", func() {
		It("Should create successfully", func() {
			By("Creating a new NodeOp")
			ctx := context.Background()
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      NodeOpName,
					Namespace: NodeOpNamespace,
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).Should(Succeed())

			// Let's make sure our NodeOp was created
			nodeOpLookupKey := types.NamespacedName{
				Name:      NodeOpName,
				Namespace: NodeOpNamespace,
			}
			createdNodeOp := &kairosiov1alpha1.NodeOp{}

			// We'll need to retry getting this newly created NodeOp, given that creation may not immediately happen.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nodeOpLookupKey, createdNodeOp)
				return err == nil
			}, timeout, interval).Should(BeTrue())

			// Let's make sure our NodeOp has the correct spec
			Expect(createdNodeOp.Spec.Command).Should(Equal([]string{"echo", "test"}))
			Expect(createdNodeOp.Spec.HostMountPath).Should(Equal("/host"))  // Default value
			Expect(createdNodeOp.Spec.Image).Should(Equal("busybox:latest")) // Default value
		})
	})

	Context("When reconciling a resource", func() {
		var (
			resourceName    string
			nodeName        string
			ctx             context.Context
			nodeop          *kairosiov1alpha1.NodeOp
			createdResource *kairosiov1alpha1.NodeOp
		)

		BeforeEach(func() {
			ctx = context.Background()
			// Set operator namespace to default for testing
			Expect(os.Setenv("OPERATOR_NAMESPACE", "default")).To(Succeed())
			// Generate a unique name for this test
			resourceName = fmt.Sprintf("test-resource-%d", time.Now().UnixNano())
			nodeName = fmt.Sprintf("test-node-%d", time.Now().UnixNano())
			nodeop = &kairosiov1alpha1.NodeOp{}
			createdResource = &kairosiov1alpha1.NodeOp{}

			By("creating the custom resource for the Kind NodeOp")
			resource := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())

			// Get the created resource to ensure TypeMeta is set and get the actual UID
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, createdResource)).To(Succeed())

			// Set TypeMeta fields
			createdResource.TypeMeta = metav1.TypeMeta{
				APIVersion: "kairos.io/v1alpha1",
				Kind:       "NodeOp",
			}
			Expect(k8sClient.Update(ctx, createdResource)).To(Succeed())

			// Create a test node with unique name
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up NodeOp
			resource := &kairosiov1alpha1.NodeOp{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			// Clean up Jobs owned by this NodeOp
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"))).To(Succeed())
			for _, job := range jobList.Items {
				// Check if this Job is owned by our NodeOp
				for _, ownerRef := range job.OwnerReferences {
					if ownerRef.Kind == kindNodeOp && ownerRef.Name == resourceName {
						Expect(k8sClient.Delete(ctx, &job)).To(Succeed())
						break
					}
				}
			}

			// Clean up Node
			node := &corev1.Node{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err == nil {
				Expect(k8sClient.Delete(ctx, node)).To(Succeed())
			}
		})

		It("should create Jobs for each node and update status", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconciliation should create Jobs
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Jobs were created
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList, client.InNamespace("default"))
			Expect(err).NotTo(HaveOccurred())

			// Count only jobs owned by our test's NodeOp
			var ownedJobs int
			for _, job := range jobList.Items {
				for _, ownerRef := range job.OwnerReferences {
					if ownerRef.Kind == kindNodeOp && ownerRef.Name == resourceName {
						ownedJobs++
						break
					}
				}
			}
			Expect(ownedJobs).To(HaveLen(1))

			// Verify Job has correct owner reference
			job := jobList.Items[0]
			Expect(job.OwnerReferences).To(HaveLen(1), fmt.Sprintf("Job %s has %d owner references", job.Name, len(job.OwnerReferences)))
			Expect(job.OwnerReferences[0].Kind).To(Equal(kindNodeOp))
			Expect(job.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(job.OwnerReferences[0].APIVersion).To(Equal("kairos.io/v1alpha1"))
			Expect(job.OwnerReferences[0].UID).To(Equal(createdResource.UID))

			// Verify NodeOp status was updated
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeop)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeop.Status.NodeStatuses).ToNot(BeEmpty())

			// Update Job status to simulate completion
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())

			// Reconcile again to update NodeOp status
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify NodeOp status was updated to reflect Job completion
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeop)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeop.Status.Phase).To(Equal("Completed"))
		})

		It("should handle Job failures", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconciliation should create Jobs
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the created Job
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList, client.InNamespace("default"))
			Expect(err).NotTo(HaveOccurred())

			// Count only jobs owned by our test's NodeOp
			var ownedJobs []batchv1.Job
			for _, job := range jobList.Items {
				for _, ownerRef := range job.OwnerReferences {
					if ownerRef.Kind == kindNodeOp && ownerRef.Name == resourceName {
						ownedJobs = append(ownedJobs, job)
						break
					}
				}
			}
			Expect(ownedJobs).To(HaveLen(1))

			// Update Job status to simulate failure
			job := ownedJobs[0]
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())

			// Reconcile again to update NodeOp status
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the job status after reconciliation
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      job.Name,
				Namespace: job.Namespace,
			}, &job)
			Expect(err).NotTo(HaveOccurred())

			// Get the NodeOp status after reconciliation
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeop)
			Expect(err).NotTo(HaveOccurred())

			// Verify NodeOp status was updated to reflect Job failure
			Expect(nodeop.Status.Phase).To(Equal("Failed"))
		})
	})
})
