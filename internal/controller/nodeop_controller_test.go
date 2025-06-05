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
		NodeOpNamespace = "default"
		timeout         = time.Second * 10
		interval        = time.Millisecond * 250
		kindNodeOp      = "NodeOp"
	)
	var (
		NodeOpName string
	)

	Context("When creating a NodeOp", func() {
		BeforeEach(func() {
			NodeOpName = fmt.Sprintf("test-nodeop-%d", time.Now().UnixNano())
		})
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
			job := &jobList.Items[0]
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
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

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
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
					Cordon:          asBool(true),
					DrainOptions: &kairosiov1alpha1.DrainOptions{
						Enabled:          asBool(true),
						Force:            asBool(false),
						IgnoreDaemonSets: asBool(true),
					},
				},
			}
			Expect(k8sClient.Create(ctx, cordonDrainNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, cordonDrainNodeOp)
				}, timeout, interval).Should(Succeed())
			})

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

			By("Simulating reboot pod completion")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": cordonDrainNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1))

			rebootPod := podList.Items[0]
			rebootPod.Status.Phase = corev1.PodSucceeded
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "completed",
			}
			Expect(k8sClient.Status().Update(ctx, &rebootPod)).To(Succeed())
			Expect(k8sClient.Update(ctx, &rebootPod)).To(Succeed())

			By("Reconciling again to process reboot completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      cordonDrainNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying node is uncordoned after job and reboot completion")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should be uncordoned after job and reboot completion")
		})

		It("should create a reboot pod when RebootOnSuccess is true and job completes successfully", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			rebootNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-reboot", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
					Cordon:          asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, rebootNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, rebootNodeOp)
				}, timeout, interval).Should(Succeed())
			})

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
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

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
			Expect(rebootPod.Spec.Containers[0].Image).To(Equal("quay.io/kairos/kairos-operator:latest"))
			Expect(rebootPod.Spec.Containers[0].Command).To(ContainElement(ContainSubstring("kubectl patch pod $POD_NAME -p")))
			Expect(rebootPod.Spec.Containers[0].SecurityContext.Privileged).To(PointTo(BeTrue()))
			Expect(rebootPod.Spec.Volumes).To(HaveLen(1))
			Expect(rebootPod.Spec.Volumes[0].Name).To(Equal("sentinel-volume"))
			Expect(rebootPod.Spec.ServiceAccountName).To(Equal(fmt.Sprintf("%s-reboot", rebootNodeOp.Name)))

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
			rebootPod.Status.Phase = corev1.PodSucceeded
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "completed",
			}
			Expect(k8sClient.Status().Update(ctx, &rebootPod)).To(Succeed())
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
				Name:      fmt.Sprintf("%s-reboot", rebootNodeOp.Name),
				Namespace: "default",
			}, sa)).To(Succeed())

			// Verify cluster role binding was created
			crb := &rbacv1.ClusterRoleBinding{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: fmt.Sprintf("nodeop-reboot-%s", rebootNodeOp.Name),
			}, crb)).To(Succeed())
			Expect(crb.Subjects).To(HaveLen(1))
			Expect(crb.Subjects[0].Name).To(Equal(fmt.Sprintf("%s-reboot", rebootNodeOp.Name)))
			Expect(crb.RoleRef.Name).To(Equal("nodeop-reboot"))
		})

		It("should NOT create reboot pods when RebootOnSuccess is false", func() {
			By("Creating a NodeOp with RebootOnSuccess=false")
			noRebootNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-no-reboot", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, noRebootNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, noRebootNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      noRebootNodeOp.Name,
					Namespace: noRebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no reboot pods were created")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": noRebootNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(BeEmpty(), "No reboot pods should be created when RebootOnSuccess is false")

			By("Verifying regular Job was created")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": noRebootNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Regular job should be created")

			By("Verifying Job does NOT have InitContainers")
			job := &jobList.Items[0]
			Expect(job.Spec.Template.Spec.InitContainers).To(BeEmpty(), "Job should not have InitContainers when RebootOnSuccess is false")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have exactly one main container")
			Expect(job.Spec.Template.Spec.Containers[0].Name).To(Equal("nodeop"))
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"echo", "test"}))

			By("Simulating job completion and verifying rebootStatus is 'not-requested'")
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			// Reconcile again to process job completion
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      noRebootNodeOp.Name,
					Namespace: noRebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify NodeOp status shows rebootStatus as "not-requested"
			updatedNodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      noRebootNodeOp.Name,
				Namespace: noRebootNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			Expect(updatedNodeOp.Status.NodeStatuses).NotTo(BeEmpty())
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("not-requested"), "RebootStatus should be 'not-requested' when RebootOnSuccess is false")
				Expect(nodeStatus.Phase).To(Equal("Completed"))
			}
		})

		It("should create reboot pods BEFORE Jobs when RebootOnSuccess is true", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			rebootFirstNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-reboot-first", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, rebootFirstNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, rebootFirstNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp for the first time")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootFirstNodeOp.Name,
					Namespace: rebootFirstNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying reboot pods are created immediately")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootFirstNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1), "Reboot pod should be created before Jobs when RebootOnSuccess is true")

			By("Verifying Jobs are also created in the same reconciliation")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootFirstNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Job should also be created")

			By("Verifying Job has InitContainer and sentinel container structure")
			job := &jobList.Items[0]
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1), "Job should have exactly one InitContainer for user command")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have exactly one main container for sentinel")

			// Verify InitContainer (user's workload)
			initContainer := job.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Name).To(Equal("nodeop"))
			Expect(initContainer.Command).To(Equal([]string{"echo", "test"}))
			Expect(initContainer.Image).To(Equal("busybox:latest"))
			Expect(initContainer.SecurityContext.Privileged).To(PointTo(BeTrue()))
			Expect(initContainer.VolumeMounts).To(HaveLen(1))
			Expect(initContainer.VolumeMounts[0].Name).To(Equal("host-root"))
			Expect(initContainer.VolumeMounts[0].MountPath).To(Equal("/host"))

			// Verify main Container (sentinel creator)
			mainContainer := job.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Name).To(Equal("sentinel-creator"))
			Expect(mainContainer.Image).To(Equal("busybox:latest"))
			Expect(mainContainer.Command).To(ContainElement(ContainSubstring("echo 'Job completed at $(date)'")))
			Expect(mainContainer.VolumeMounts).To(HaveLen(1))
			Expect(mainContainer.VolumeMounts[0].Name).To(Equal("sentinel-volume"))
			Expect(mainContainer.VolumeMounts[0].MountPath).To(Equal("/sentinel"))

			// Verify volumes
			Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(2))
			hostRootVolume := job.Spec.Template.Spec.Volumes[0]
			Expect(hostRootVolume.Name).To(Equal("host-root"))
			Expect(hostRootVolume.VolumeSource.HostPath.Path).To(Equal("/"))

			sentinelVolume := job.Spec.Template.Spec.Volumes[1]
			Expect(sentinelVolume.Name).To(Equal("sentinel-volume"))
			Expect(sentinelVolume.VolumeSource.HostPath.Path).To(Equal("/usr/local/.kairos"))
		})

		It("should update rebootStatus field correctly throughout the reboot lifecycle", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			statusNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-status", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, statusNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, statusNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Initial reconciliation - rebootStatus should be 'pending'")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      statusNodeOp.Name,
					Namespace: statusNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Check initial status
			updatedNodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      statusNodeOp.Name,
				Namespace: statusNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			Expect(updatedNodeOp.Status.NodeStatuses).NotTo(BeEmpty())
			// Check overall NodeOp status should be Running initially
			Expect(updatedNodeOp.Status.Phase).To(Equal("Running"), "Overall NodeOp status should be 'Running' initially")
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("pending"), "RebootStatus should be 'pending' initially when RebootOnSuccess is true")
				Expect(nodeStatus.Phase).To(Equal("Pending"))
			}

			By("Simulating job completion - rebootStatus should remain 'pending'")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": statusNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			// Reconcile to process job completion
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      statusNodeOp.Name,
					Namespace: statusNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Check status after job completion
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      statusNodeOp.Name,
				Namespace: statusNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			// Overall NodeOp should still be Running, not Completed, because reboot pod hasn't completed yet
			Expect(updatedNodeOp.Status.Phase).To(Equal("Running"), "Overall NodeOp status should remain 'Running' when job completes but reboot pod is still pending")
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("pending"), "RebootStatus should remain 'pending' after job completion but before reboot completion")
				Expect(nodeStatus.Phase).To(Equal("Completed"))
			}

			By("Simulating reboot pod completion - rebootStatus should become 'completed'")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": statusNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1))

			rebootPod := podList.Items[0]
			rebootPod.Status.Phase = corev1.PodSucceeded
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "completed",
			}
			Expect(k8sClient.Status().Update(ctx, &rebootPod)).To(Succeed())
			Expect(k8sClient.Update(ctx, &rebootPod)).To(Succeed())

			// Reconcile to process reboot completion
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      statusNodeOp.Name,
					Namespace: statusNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Check final status
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      statusNodeOp.Name,
				Namespace: statusNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			// Now the overall NodeOp should be Completed since both job and reboot pod are done
			Expect(updatedNodeOp.Status.Phase).To(Equal("Completed"), "Overall NodeOp status should be 'Completed' only when both job and reboot pod are completed")
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("completed"), "RebootStatus should be 'completed' after reboot pod finishes successfully")
				Expect(nodeStatus.Phase).To(Equal("Completed"))
			}
		})

		It("should handle failed jobs by setting rebootStatus to 'cancelled' and cleaning up reboot pods", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			failedJobNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-failed", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, failedJobNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, failedJobNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Initial reconciliation")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      failedJobNodeOp.Name,
					Namespace: failedJobNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying reboot pod was created")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": failedJobNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1), "Reboot pod should be created initially")

			By("Simulating job failure")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": failedJobNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			job.Status.Succeeded = 0
			job.Status.Active = 0
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling to process job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      failedJobNodeOp.Name,
					Namespace: failedJobNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying rebootStatus is set to 'cancelled' for failed job")
			updatedNodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      failedJobNodeOp.Name,
				Namespace: failedJobNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			Expect(updatedNodeOp.Status.Phase).To(Equal("Failed"))
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("cancelled"), "RebootStatus should be 'cancelled' when job fails")
				Expect(nodeStatus.Phase).To(Equal("Failed"))
			}

			By("Verifying reboot pod was marked for deletion")
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": failedJobNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1)) // Not deleted in tests since we don't have kubelet running.
			Expect(podList.Items[0].DeletionTimestamp).NotTo(BeNil(), "Reboot pod should be marked for deletion when job fails")
		})

		It("should set rebootStatus to 'not-requested' when RebootOnSuccess=false and job fails", func() {
			By("Creating a NodeOp with RebootOnSuccess=false")
			noRebootFailedJobNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-no-reboot-failed", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"exit", "1"}, // This will cause the job to fail
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, noRebootFailedJobNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, noRebootFailedJobNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Initial reconciliation")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      noRebootFailedJobNodeOp.Name,
					Namespace: noRebootFailedJobNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no reboot pod was created")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": noRebootFailedJobNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(BeEmpty(), "No reboot pod should be created when RebootOnSuccess is false")

			By("Simulating job failure")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": noRebootFailedJobNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			job.Status.Succeeded = 0
			job.Status.Active = 0
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling to process job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      noRebootFailedJobNodeOp.Name,
					Namespace: noRebootFailedJobNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying rebootStatus is set to 'not-requested' for failed job when RebootOnSuccess=false")
			updatedNodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      noRebootFailedJobNodeOp.Name,
				Namespace: noRebootFailedJobNodeOp.Namespace,
			}, updatedNodeOp)).To(Succeed())

			Expect(updatedNodeOp.Status.Phase).To(Equal("Failed"))
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("not-requested"), "RebootStatus should be 'not-requested' when job fails and RebootOnSuccess=false")
				Expect(nodeStatus.Phase).To(Equal("Failed"))
			}
		})

		It("should apply custom BackoffLimit from NodeOp spec to created Jobs", func() {
			By("Creating a NodeOp with custom BackoffLimit")
			customBackoffLimit := int32(10)
			backoffNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-backoff", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:      []string{"echo", "test"},
					BackoffLimit: &customBackoffLimit,
				},
			}
			Expect(k8sClient.Create(ctx, backoffNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, backoffNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      backoffNodeOp.Name,
					Namespace: backoffNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with custom BackoffLimit")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": backoffNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			Expect(job.Spec.BackoffLimit).NotTo(BeNil(), "Job BackoffLimit should be set")
			Expect(*job.Spec.BackoffLimit).To(Equal(customBackoffLimit), "Job BackoffLimit should match NodeOp spec")
		})

		It("should use Kubernetes default BackoffLimit (6) when not specified in NodeOp", func() {
			By("Creating a NodeOp without BackoffLimit specified")
			defaultBackoffNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-default-backoff", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
					// BackoffLimit is intentionally not specified
				},
			}
			Expect(k8sClient.Create(ctx, defaultBackoffNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, defaultBackoffNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      defaultBackoffNodeOp.Name,
					Namespace: defaultBackoffNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with Kubernetes default BackoffLimit (6)")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": defaultBackoffNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			Expect(job.Spec.BackoffLimit).NotTo(BeNil(), "Job BackoffLimit should be set")
			Expect(*job.Spec.BackoffLimit).To(Equal(int32(6)), "Job BackoffLimit should default to Kubernetes default (6)")
		})

		It("should apply custom BackoffLimit to Jobs even when RebootOnSuccess is true", func() {
			By("Creating a NodeOp with custom BackoffLimit and RebootOnSuccess=true")
			customBackoffLimit := int32(15)
			// Create a unique node for this test
			testNodeName := fmt.Sprintf("%s-reboot-backoff-node", resourceName)
			testNode := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: testNodeName,
				},
			}
			Expect(k8sClient.Create(ctx, testNode)).To(Succeed())

			rebootBackoffNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-reboot-backoff", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					BackoffLimit:    &customBackoffLimit,
					RebootOnSuccess: asBool(true),
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/hostname": testNodeName},
					},
				},
			}
			Expect(k8sClient.Create(ctx, rebootBackoffNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp and Node
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, rebootBackoffNodeOp)
				}, timeout, interval).Should(Succeed())
				Eventually(func() error {
					return k8sClient.Delete(ctx, testNode)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootBackoffNodeOp.Name,
					Namespace: rebootBackoffNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with custom BackoffLimit even with reboot enabled")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootBackoffNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			Expect(job.Spec.BackoffLimit).NotTo(BeNil(), "Job BackoffLimit should be set")
			Expect(*job.Spec.BackoffLimit).To(Equal(customBackoffLimit), "Job BackoffLimit should match NodeOp spec even with RebootOnSuccess=true")

			By("Verifying Job structure is correct for reboot case")
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1), "Job should have InitContainer for user command")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have main container for sentinel")
		})
	})
})

var _ = Describe("NodeOp Controller - Concurrency and StopOnFailure", func() {
	const (
		timeout    = time.Second * 10
		interval   = time.Millisecond * 250
		kindNodeOp = "NodeOp"
	)

	var (
		ctx                  context.Context
		resourceName         string
		nodeNames            []string
		nodes                []*corev1.Node
		controllerReconciler *NodeOpReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		// Set operator namespace to default for testing
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())

		// Generate unique names for this test
		resourceName = fmt.Sprintf("test-concurrency-%d", time.Now().UnixNano())

		// Create multiple test nodes
		nodeNames = []string{
			fmt.Sprintf("test-node-1-%d", time.Now().UnixNano()),
			fmt.Sprintf("test-node-2-%d", time.Now().UnixNano()),
			fmt.Sprintf("test-node-3-%d", time.Now().UnixNano()),
		}

		nodes = make([]*corev1.Node, len(nodeNames))
		for i, nodeName := range nodeNames {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			nodes[i] = node
		}

		controllerReconciler = &NodeOpReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	AfterEach(func() {
		// Clean up environment variables
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())

		// Clean up NodeOp
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

		// Clean up Jobs
		Eventually(func() error {
			jobList := &batchv1.JobList{}
			if err := k8sClient.List(ctx, jobList, client.InNamespace("default")); err != nil {
				return err
			}
			for _, job := range jobList.Items {
				for _, ownerRef := range job.OwnerReferences {
					if ownerRef.Kind == kindNodeOp && ownerRef.Name == resourceName {
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

		// Clean up nodes
		for _, node := range nodes {
			Eventually(func() error {
				return k8sClient.Delete(ctx, node)
			}, timeout, interval).Should(Succeed())
		}
	})

	Context("When testing concurrency limits", func() {
		It("should create jobs on all nodes when concurrency is 0 (unlimited)", func() {
			By("Creating a NodeOp with concurrency=0")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:     []string{"echo", "test"},
					Concurrency: 0, // unlimited
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying jobs were created for all nodes")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(len(nodeNames)), "Should create jobs for all nodes")

			By("Verifying NodeOp status shows all nodes")
			Eventually(func() int {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				}, nodeOp)
				if err != nil {
					return 0
				}
				return len(nodeOp.Status.NodeStatuses)
			}, timeout, interval).Should(Equal(len(nodeNames)))
		})

		It("should limit concurrent jobs when concurrency is set to 1", func() {
			By("Creating a NodeOp with concurrency=1")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:     []string{"echo", "test"},
					Concurrency: 1,
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only one job was created initially")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should create only one job initially")

			By("Simulating first job completion")
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling again to trigger next job creation")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second job was created")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should have two jobs after first completion")
		})

		It("should respect concurrency limit of 2", func() {
			By("Creating a NodeOp with concurrency=2")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:     []string{"echo", "test"},
					Concurrency: 2,
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying exactly two jobs were created initially")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should create exactly two jobs initially")

			By("Simulating one job completion")
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling again to trigger third job creation")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying third job was created")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(3), "Should have three jobs after one completion")
		})
	})

	Context("When testing StopOnFailure feature", func() {
		It("should stop creating new jobs when StopOnFailure is true and a job fails", func() {
			By("Creating a NodeOp with StopOnFailure=true and concurrency=1")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:       []string{"echo", "test"},
					Concurrency:   1,
					StopOnFailure: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying one job was created")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should create one job initially")

			By("Simulating job failure")
			job := &jobList.Items[0]
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling again after job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no additional jobs were created")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should not create additional jobs after failure")

			By("Verifying NodeOp status shows failed phase")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeOp)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeOp.Status.Phase).To(Equal("Failed"))
		})

		It("should continue creating jobs when StopOnFailure is false and a job fails", func() {
			By("Creating a NodeOp with StopOnFailure=false and concurrency=1")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:       []string{"echo", "test"},
					Concurrency:   1,
					StopOnFailure: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying one job was created")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should create one job initially")

			By("Simulating job failure")
			job := &jobList.Items[0]
			job.Status.Failed = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling to process job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second job was created despite failure")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should create second job despite first failure")
		})
	})

	Context("When testing TargetNodes filtering", func() {
		It("should only create jobs on specified target nodes", func() {
			By("Creating a NodeOp targeting only first two nodes")
			// Assign label 'test-group: A' to first two nodes, 'test-group: B' to the third
			labelA := fmt.Sprintf("A-%s", resourceName)
			labelB := fmt.Sprintf("B-%s", resourceName)
			for i, node := range nodes {
				if i < 2 {
					node.Labels = map[string]string{"test-group": labelA}
				} else {
					node.Labels = map[string]string{"test-group": labelB}
				}
				Expect(k8sClient.Update(ctx, node)).To(Succeed())
			}

			nodeOp := &kairosiov1alpha1.NodeOp{
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
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"test-group": labelA},
					},
					Concurrency: 0, // unlimited
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying jobs were created only for target nodes")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should create jobs only for target nodes")

			By("Verifying the correct nodes have jobs")
			nodeNamesWithJobs := make(map[string]bool)
			for _, job := range jobList.Items {
				if nodeName, exists := job.Labels["kairos.io/node"]; exists {
					nodeNamesWithJobs[nodeName] = true
				}
			}
			Expect(nodeNamesWithJobs).To(HaveKey(nodeNames[0]))
			Expect(nodeNamesWithJobs).To(HaveKey(nodeNames[1]))
			Expect(nodeNamesWithJobs).NotTo(HaveKey(nodeNames[2]))
		})
	})

	Context("When testing combined features", func() {
		It("should respect both concurrency and target nodes", func() {
			By("Creating a NodeOp with concurrency=1 and targeting two nodes")
			// Assign label 'test-group: A' to first two nodes, 'test-group: B' to the third
			labelA := fmt.Sprintf("A-%s", resourceName)
			labelB := fmt.Sprintf("B-%s", resourceName)
			for i, node := range nodes {
				if i < 2 {
					node.Labels = map[string]string{"test-group": labelA}
				} else {
					node.Labels = map[string]string{"test-group": labelB}
				}
				Expect(k8sClient.Update(ctx, node)).To(Succeed())
			}

			nodeOp := &kairosiov1alpha1.NodeOp{
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
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"test-group": labelA},
					},
					Concurrency: 1,
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only one job was created initially")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should create only one job initially due to concurrency=1")

			By("Simulating job completion")
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling again to trigger second job creation")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying second job was created")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should have two jobs total for two target nodes")

			By("Verifying no third job is created")
			// Complete the second job
			for _, j := range jobList.Items {
				if j.Status.Succeeded == 0 {
					j.Status.Succeeded = 1
					Expect(k8sClient.Status().Update(ctx, &j)).To(Succeed())
					break
				}
			}

			// Reconcile again
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Should still have only 2 jobs (no third one for nodeNames[2])
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should not create job for third node not in target list")
		})
	})

	Context("When testing concurrency with reboot pending", func() {
		It("should consider jobs with pending reboot as running and not start new jobs", func() {
			By("Creating a NodeOp with concurrency=1 and RebootOnSuccess=true")
			nodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Concurrency:     1,
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

			By("Reconciling the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying one job was created initially")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should create one job initially")

			By("Simulating job completion (but reboot still pending)")
			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

			By("Reconciling to process job completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying job status shows completed but reboot pending")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeOp)
			Expect(err).NotTo(HaveOccurred())

			var nodeStatus kairosiov1alpha1.NodeStatus
			for _, status := range nodeOp.Status.NodeStatuses {
				nodeStatus = status
				break // We only have one node in this test
			}
			Expect(nodeStatus.Phase).To(Equal("Completed"))
			Expect(nodeStatus.RebootStatus).To(Equal("pending"))

			By("Reconciling again - no new jobs should be created while reboot is pending")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying no additional jobs were created while reboot is pending")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should not create additional jobs while reboot is pending")

			By("Simulating reboot completion")
			podList := &corev1.PodList{}
			err = k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1))

			rebootPod := podList.Items[0]
			rebootPod.Status.Phase = corev1.PodSucceeded
			rebootPod.Annotations = map[string]string{
				"kairos.io/reboot-state": "completed",
			}
			Expect(k8sClient.Status().Update(ctx, &rebootPod)).To(Succeed())
			Expect(k8sClient.Update(ctx, &rebootPod)).To(Succeed())

			By("Reconciling after reboot completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying reboot status is now completed")
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nodeOp)
			Expect(err).NotTo(HaveOccurred())

			for _, status := range nodeOp.Status.NodeStatuses {
				nodeStatus = status
				break // We only have one node in this test
			}
			Expect(nodeStatus.Phase).To(Equal("Completed"))
			Expect(nodeStatus.RebootStatus).To(Equal("completed"))

			By("Reconciling again - now new jobs should be allowed since reboot is completed")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      resourceName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying new job can now be created (if more target nodes exist)")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			// In this test we have 3 nodes total, so we should now have 2 jobs
			// (first one completed with reboot completed, second one just started)
			Expect(jobList.Items).To(HaveLen(2), "Should create second job after reboot is completed")
		})
	})
})
