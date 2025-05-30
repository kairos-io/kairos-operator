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
	. "github.com/onsi/gomega/gstruct"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
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
			Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())
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
			// Clean up environment variables first
			Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())

			// Clean up NodeOp with retry
			Eventually(func() error {
				resource := &kairosiov1alpha1.NodeOp{}
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				}, resource)
				if err != nil {
					if client.IgnoreNotFound(err) != nil {
						return err
					}
					return nil
				}
				return k8sClient.Delete(ctx, resource)
			}, timeout, interval).Should(Succeed())

			// Clean up Jobs owned by this NodeOp with retry
			Eventually(func() error {
				jobList := &batchv1.JobList{}
				if err := k8sClient.List(ctx, jobList, client.InNamespace("default")); err != nil {
					return err
				}
				for _, job := range jobList.Items {
					// Check if this Job is owned by our NodeOp
					for _, ownerRef := range job.OwnerReferences {
						if ownerRef.Kind == kindNodeOp && ownerRef.Name == resourceName {
							// Add propagation policy to delete child pods
							propagationPolicy := metav1.DeletePropagationBackground
							deleteOpts := &client.DeleteOptions{
								PropagationPolicy: &propagationPolicy,
							}
							if err := k8sClient.Delete(ctx, &job, deleteOpts); err != nil {
								return err
							}
							break
						}
					}
				}
				return nil
			}, timeout, interval).Should(Succeed())

			// Clean up Node with retry
			Eventually(func() error {
				node := &corev1.Node{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
				if err != nil {
					if client.IgnoreNotFound(err) != nil {
						return err
					}
					return nil
				}
				return k8sClient.Delete(ctx, node)
			}, timeout, interval).Should(Succeed())
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
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
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
			Expect(ownedJobs).To(Equal(1))

			// Verify Job has correct owner reference
			job := jobList.Items[0]
			Expect(job.OwnerReferences).To(HaveLen(1), fmt.Sprintf("Job %s has %d owner references", job.Name, len(job.OwnerReferences)))
			Expect(job.OwnerReferences[0].Kind).To(Equal(kindNodeOp))
			Expect(job.OwnerReferences[0].Name).To(Equal(resourceName))
			Expect(job.OwnerReferences[0].APIVersion).To(Equal("operator.kairos.io/v1alpha1"))
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
			job.Status.Succeeded = 0
			job.Status.Active = 0
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

		It("should cordon and drain node when specified in NodeOp spec", func() {
			By("Creating a NodeOp with cordon and drain enabled")
			cordonDrainNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-cordon", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
					Cordon:  true,
					DrainOptions: &kairosiov1alpha1.DrainOptions{
						Enabled:          true,
						Force:            false,
						IgnoreDaemonSets: true,
					},
				},
			}
			Expect(k8sClient.Create(ctx, cordonDrainNodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cordonDrainNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying node is cordoned")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should be cordoned")

			By("Verifying job was created")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": cordonDrainNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should have created one job")

			By("Simulating job completion")
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling again to process job completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cordonDrainNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying node is uncordoned after job completion")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should be uncordoned after job completion")
		})

		It("should create a reboot pod when RebootOnSuccess is true and job completes successfully", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			rebootNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName + "-reboot",
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: true,
					Cordon:          true,
				},
			}
			Expect(k8sClient.Create(ctx, rebootNodeOp)).To(Succeed())

			By("Verifying node starts in schedulable state")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should start in schedulable state")

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconciliation should create Jobs
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootNodeOp.Name,
					Namespace: rebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify Job was created
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			// Simulate job completion
			job := jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, &job)).To(Succeed())

			// Reconcile again to trigger reboot pod creation
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootNodeOp.Name,
					Namespace: rebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify reboot pod was created
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1))

			// Verify reboot pod configuration
			rebootPod := podList.Items[0]
			Expect(rebootPod.Spec.NodeName).To(Equal(nodeName))
			Expect(rebootPod.Spec.Containers).To(HaveLen(1))
			Expect(rebootPod.Spec.Containers[0].Image).To(Equal("bitnami/kubectl:latest"))
			Expect(rebootPod.Spec.Containers[0].Command).To(ContainElement(ContainSubstring("kubectl patch pod $POD_NAME -p")))
			Expect(rebootPod.Spec.Containers[0].SecurityContext.Privileged).To(PointTo(BeTrue()))
			Expect(rebootPod.Spec.Volumes).To(BeEmpty())
			Expect(rebootPod.Spec.ServiceAccountName).To(Equal("nodeop-reboot"))

			// Verify node is still cordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should remain cordoned until reboot is completed")

			// Simulate reboot pod setting pending state
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "pending",
			}
			Expect(k8sClient.Update(ctx, &rebootPod)).To(Succeed())

			// Reconcile again - node should still be cordoned
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootNodeOp.Name,
					Namespace: rebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify node is still cordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should remain cordoned until reboot is completed")

			// Simulate reboot pod completing
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "completed",
			}
			Expect(k8sClient.Update(ctx, &rebootPod)).To(Succeed())

			// Reconcile again - node should be uncordoned
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootNodeOp.Name,
					Namespace: rebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify node is uncordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should be uncordoned after reboot is completed")

			// Verify the reboot pod has completed state
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      rebootPod.Name,
				Namespace: rebootPod.Namespace,
			}, &rebootPod)).To(Succeed())
			Expect(rebootPod.Annotations).To(HaveKeyWithValue("kairos.io/reboot-state", "completed"))

			// Verify service account was created
			sa := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "nodeop-reboot",
				Namespace: "default",
			}, sa)).To(Succeed())

			// Verify role was created
			role := &rbacv1.Role{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "nodeop-reboot",
				Namespace: "default",
			}, role)).To(Succeed())
			Expect(role.Rules).To(HaveLen(1))
			Expect(role.Rules[0].Resources).To(ContainElement("pods"))
			Expect(role.Rules[0].Verbs).To(ContainElements("get", "patch"))

			// Verify role binding was created
			roleBinding := &rbacv1.RoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      "nodeop-reboot",
				Namespace: "default",
			}, roleBinding)).To(Succeed())
			Expect(roleBinding.Subjects).To(HaveLen(1))
			Expect(roleBinding.Subjects[0].Name).To(Equal("nodeop-reboot"))
			Expect(roleBinding.RoleRef.Name).To(Equal("nodeop-reboot"))
		})
	})
})
