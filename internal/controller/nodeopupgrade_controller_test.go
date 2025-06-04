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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("NodeOpUpgrade Controller", func() {
	Context("When reconciling a NodeOpUpgrade resource", func() {
		var (
			nodeOpUpgradeName string
			nodeOpUpgrade     *kairosiov1alpha1.NodeOpUpgrade
			ctx               context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			// Generate a unique name for this test
			nodeOpUpgradeName = fmt.Sprintf("test-nodeopupgrade-%d", time.Now().UnixNano())

			// Create a basic NodeOpUpgrade resource
			nodeOpUpgrade = &kairosiov1alpha1.NodeOpUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
					Image:           "quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1",
					UpgradeActive:   true,
					UpgradeRecovery: false,
					Force:           false,
					Concurrency:     1,
					StopOnFailure:   false,
				},
			}
		})

		AfterEach(func() {
			// Clean up NodeOpUpgrade
			nodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeOpUpgradeName, Namespace: "default"}, nodeOpUpgrade)
			if err == nil {
				Expect(k8sClient.Delete(ctx, nodeOpUpgrade)).To(Succeed())
			}

			// Clean up any created NodeOp resources
			nodeOpList := &kairosiov1alpha1.NodeOpList{}
			Expect(k8sClient.List(ctx, nodeOpList, client.InNamespace("default"))).To(Succeed())
			for _, nodeOp := range nodeOpList.Items {
				if strings.Contains(nodeOp.Name, nodeOpUpgradeName) {
					// Add propagation policy to delete child resources
					propagationPolicy := metav1.DeletePropagationBackground
					deleteOpts := &client.DeleteOptions{
						PropagationPolicy: &propagationPolicy,
					}
					Expect(k8sClient.Delete(ctx, &nodeOp, deleteOpts)).To(Succeed())
				}
			}
		})

		It("should create a NodeOp resource when NodeOpUpgrade is created", func() {
			By("Creating the NodeOpUpgrade resource")
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that a NodeOp was created")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			By("Verifying NodeOp has correct configuration")
			Expect(nodeOp.Spec.Image).To(Equal(nodeOpUpgrade.Spec.Image))
			Expect(nodeOp.Spec.Concurrency).To(Equal(nodeOpUpgrade.Spec.Concurrency))
			Expect(nodeOp.Spec.StopOnFailure).To(Equal(nodeOpUpgrade.Spec.StopOnFailure))
			Expect(nodeOp.Spec.TargetNodes).To(Equal(nodeOpUpgrade.Spec.TargetNodes))
			Expect(nodeOp.Spec.HostMountPath).To(Equal("/host"))
			Expect(nodeOp.Spec.Cordon).To(BeTrue())
			Expect(nodeOp.Spec.RebootOnSuccess).To(BeTrue())
			Expect(nodeOp.Spec.DrainOptions).NotTo(BeNil())
			Expect(nodeOp.Spec.DrainOptions.Enabled).To(BeTrue())

			By("Verifying NodeOp has correct labels")
			Expect(nodeOp.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "nodeopupgrade-controller"))
			Expect(nodeOp.Labels).To(HaveKeyWithValue("nodeopupgrade.kairos.io/name", nodeOpUpgradeName))

			By("Verifying NodeOp has correct owner reference")
			Expect(nodeOp.OwnerReferences).To(HaveLen(1))
			Expect(nodeOp.OwnerReferences[0].Kind).To(Equal("NodeOpUpgrade"))
			Expect(nodeOp.OwnerReferences[0].Name).To(Equal(nodeOpUpgradeName))

			By("Verifying NodeOpUpgrade status is updated")
			updatedNodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, updatedNodeOpUpgrade)).To(Succeed())
			Expect(updatedNodeOpUpgrade.Status.Phase).To(Equal("Initializing"))
		})

		It("should generate correct upgrade command for active partition only", func() {
			By("Creating the NodeOpUpgrade resource with active upgrade only")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = false
			nodeOpUpgrade.Spec.Force = false

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			Expect(nodeOp.Spec.Command).To(HaveLen(3))
			Expect(nodeOp.Spec.Command[0]).To(Equal("/bin/sh"))
			Expect(nodeOp.Spec.Command[1]).To(Equal("-c"))

			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent upgrade --source dir:/"))
			Expect(script).To(ContainSubstring("get_version()"))
			Expect(script).To(ContainSubstring("mount --rbind"))
			Expect(script).NotTo(ContainSubstring("--recovery"))
			Expect(script).NotTo(ContainSubstring("export FORCE=true"))
		})

		It("should generate correct upgrade command for recovery partition only", func() {
			By("Creating the NodeOpUpgrade resource with recovery upgrade only")
			nodeOpUpgrade.Spec.UpgradeActive = false
			nodeOpUpgrade.Spec.UpgradeRecovery = true
			nodeOpUpgrade.Spec.Force = false

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent upgrade --recovery --source dir:/"))
		})

		It("should generate correct upgrade command for both partitions", func() {
			By("Creating the NodeOpUpgrade resource with both upgrades")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = true
			nodeOpUpgrade.Spec.Force = false

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("# Upgrade recovery partition"))
			Expect(script).To(ContainSubstring("kairos-agent upgrade --recovery --source dir:/"))
			Expect(script).To(ContainSubstring("# Upgrade active partition"))
			Expect(script).To(ContainSubstring("kairos-agent upgrade --source dir:/"))
		})

		It("should generate correct upgrade command with force enabled", func() {
			By("Creating the NodeOpUpgrade resource with force enabled")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = false
			nodeOpUpgrade.Spec.Force = true

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			script := nodeOp.Spec.Command[2]
			Expect(script).NotTo(ContainSubstring("get_version()"))
			Expect(script).To(ContainSubstring("mount --rbind"))
			Expect(script).NotTo(ContainSubstring("--recovery"))
			Expect(script).NotTo(ContainSubstring("export FORCE=true"))
		})

		It("should update NodeOpUpgrade status when NodeOp status changes", func() {
			By("Creating the NodeOpUpgrade resource")
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling to create the NodeOp")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Updating the NodeOp status")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			// Simulate NodeOp status update
			nodeOp.Status.Phase = "Running"
			nodeOp.Status.NodeStatuses = map[string]kairosiov1alpha1.NodeStatus{
				"test-node": {
					Phase:   "Running",
					JobName: "test-job",
					Message: "Job is running",
				},
			}
			Expect(k8sClient.Status().Update(ctx, nodeOp)).To(Succeed())

			By("Reconciling again to sync status")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying NodeOpUpgrade status is updated")
			updatedNodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, updatedNodeOpUpgrade)).To(Succeed())
			Expect(updatedNodeOpUpgrade.Status.Phase).To(Equal("Running"))

			Expect(updatedNodeOpUpgrade.Status.Message).To(Equal("Upgrade operation is running"))
			Expect(updatedNodeOpUpgrade.Status.NodeStatuses).To(HaveKey("test-node"))
			Expect(updatedNodeOpUpgrade.Status.NodeStatuses["test-node"].Phase).To(Equal("Running"))
		})

		It("should not create a new NodeOp if one already exists", func() {
			By("Creating the NodeOpUpgrade resource")
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling to create the NodeOp")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying NodeOp was created")
			nodeOpList := &kairosiov1alpha1.NodeOpList{}
			Expect(k8sClient.List(ctx, nodeOpList, client.InNamespace("default"), client.MatchingLabels{
				"nodeopupgrade.kairos.io/name": nodeOpUpgradeName,
			})).To(Succeed())
			Expect(nodeOpList.Items).To(HaveLen(1))

			By("Reconciling again")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying only one NodeOp exists")
			err = k8sClient.List(ctx, nodeOpList, client.InNamespace("default"), client.MatchingLabels{
				"nodeopupgrade.kairos.io/name": nodeOpUpgradeName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeOpList.Items).To(HaveLen(1))
		})

		It("should set RebootOnSuccess correctly based on UpgradeActive", func() {
			By("Creating NodeOpUpgrade with UpgradeActive=true (should reboot)")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = false
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying RebootOnSuccess is true when UpgradeActive is true")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())
			Expect(nodeOp.Spec.RebootOnSuccess).To(BeTrue())

			By("Cleaning up for next test")
			Expect(k8sClient.Delete(ctx, nodeOpUpgrade)).To(Succeed())
			Expect(k8sClient.Delete(ctx, nodeOp)).To(Succeed())

			By("Creating NodeOpUpgrade with UpgradeActive=false (should not reboot)")
			nodeOpUpgrade2Name := fmt.Sprintf("test-nodeopupgrade-noreboot-%d", time.Now().UnixNano())
			nodeOpUpgrade2 := &kairosiov1alpha1.NodeOpUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeOpUpgrade2Name,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
					Image:           "quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1",
					UpgradeActive:   false,
					UpgradeRecovery: true,
					Force:           false,
				},
			}
			Expect(k8sClient.Create(ctx, nodeOpUpgrade2)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgrade2Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying RebootOnSuccess is false when UpgradeActive is false")
			nodeOp2 := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgrade2Name,
				Namespace: "default",
			}, nodeOp2)).To(Succeed())
			Expect(nodeOp2.Spec.RebootOnSuccess).To(BeFalse())

			By("Cleaning up second test resources")
			Expect(k8sClient.Delete(ctx, nodeOpUpgrade2)).To(Succeed())
			Expect(k8sClient.Delete(ctx, nodeOp2)).To(Succeed())
		})

		It("should set RebootOnSuccess to true when UpgradeActive is true", func() {
			By("Creating NodeOpUpgrade with UpgradeActive=true")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = false
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying RebootOnSuccess is true")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())
			Expect(nodeOp.Spec.RebootOnSuccess).To(BeTrue())
		})

		It("should set RebootOnSuccess to false when UpgradeActive is false", func() {
			By("Creating NodeOpUpgrade with UpgradeActive=false")
			nodeOpUpgrade.Spec.UpgradeActive = false
			nodeOpUpgrade.Spec.UpgradeRecovery = true
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying RebootOnSuccess is false")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())
			Expect(nodeOp.Spec.RebootOnSuccess).To(BeFalse())
		})

		It("should set RebootOnSuccess to true when upgrading both partitions", func() {
			By("Creating NodeOpUpgrade with both UpgradeActive and UpgradeRecovery true")
			nodeOpUpgrade.Spec.UpgradeActive = true
			nodeOpUpgrade.Spec.UpgradeRecovery = true
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying RebootOnSuccess is true when upgrading both partitions")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())
			Expect(nodeOp.Spec.RebootOnSuccess).To(BeTrue())
		})
	})
})
