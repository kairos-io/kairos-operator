package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

var _ = Describe("getNodeOpImage", func() {
	var nodeOp = &kairosiov1alpha1.NodeOp{}

	It("should return Spec.Image when set", func() {
		nodeOp.Spec.Image = "spec-image"
		defer func() { nodeOp = &kairosiov1alpha1.NodeOp{} }()
		Expect(getNodeOpImage(nodeOp)).To(Equal("spec-image"))
	})

	It("should return the value of NODEOP_DEFAULT_IMAGE when it is set and Spec.Image is empty", func() {
		Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "nodeop-env-image")).To(Succeed())
		defer func() { Expect(os.Unsetenv("NODEOP_DEFAULT_IMAGE")).To(Succeed()) }()
		Expect(getNodeOpImage(nodeOp)).To(Equal("nodeop-env-image"))
	})

	It("should return busybox:latest when Spec.Image and NODEOP_DEFAULT_IMAGE are empty", func() {
		Expect(getNodeOpImage(nodeOp)).To(Equal("busybox:latest"))
	})
})

var _ = Describe("getSentinelImage", func() {
	var nodeOp = &kairosiov1alpha1.NodeOp{}

	It("should return the value of SENTINEL_IMAGE when it is set", func() {
		Expect(os.Setenv("SENTINEL_IMAGE", "sentinel-env-image")).To(Succeed())
		defer func() { Expect(os.Unsetenv("SENTINEL_IMAGE")).To(Succeed()) }()
		Expect(getSentinelImage(nodeOp)).To(Equal("sentinel-env-image"))
	})

	It("should return Spec.Image when SENTINEL_IMAGE is empty", func() {
		nodeOp.Spec.Image = "spec-image"
		defer func() { nodeOp = &kairosiov1alpha1.NodeOp{} }()
		Expect(getSentinelImage(nodeOp)).To(Equal("spec-image"))
	})

	It("should return busybox:latest when Spec.Image and SENTINEL_IMAGE are empty", func() {
		Expect(getSentinelImage(nodeOp)).To(Equal("busybox:latest"))
	})
})

var _ = Describe("findNodeOpsForPreflightPod", func() {
	r := &NodeOpReconciler{}

	It("returns the owning NodeOp when the Pod carries the preflight + nodeop labels", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "some-preflight-xyz",
				Namespace: "default",
				Labels: map[string]string{
					"kairos.io/preflight": "true",
					"kairos.io/nodeop":    "my-upgrade",
				},
			},
		}
		reqs := r.findNodeOpsForPreflightPod(context.Background(), pod)
		Expect(reqs).To(HaveLen(1))
		Expect(reqs[0].NamespacedName.Name).To(Equal("my-upgrade"))
		Expect(reqs[0].NamespacedName.Namespace).To(Equal("default"))
	})

	It("returns nothing when the Pod is missing the nodeop label", func() {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "stray",
				Namespace: "default",
				Labels:    map[string]string{"kairos.io/preflight": "true"},
			},
		}
		Expect(r.findNodeOpsForPreflightPod(context.Background(), pod)).To(BeEmpty())
	})
})

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
			Expect(createdNodeOp.Spec.HostMountPath).Should(Equal("/host")) // Default value
			Expect(createdNodeOp.Spec.Image).Should(BeEmpty())              // Default applied at controller level via getNodeOpImage()
		})
	})

	Context("When resolving NodeOp and sentinel container images", func() {
		var (
			node *corev1.Node
			ctx  context.Context
		)

		BeforeEach(func() {
			NodeOpName = fmt.Sprintf("test-nodeop-%d", time.Now().UnixNano())
			ctx = context.Background()

			node = &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-node-%d", time.Now().UnixNano()),
				},
			}
			Expect(k8sClient.Create(ctx, node)).Should(Succeed())
		})

		AfterEach(func() {
			// Clean up NodeOps
			nodeOpList := &kairosiov1alpha1.NodeOpList{}
			_ = k8sClient.List(ctx, nodeOpList, client.InNamespace(NodeOpNamespace))
			for _, nodeOp := range nodeOpList.Items {
				_ = k8sClient.Delete(ctx, &nodeOp)
			}

			// Clean up Jobs
			jobList := &batchv1.JobList{}
			_ = k8sClient.List(ctx, jobList, client.InNamespace(NodeOpNamespace))
			for _, job := range jobList.Items {
				propagationPolicy := metav1.DeletePropagationBackground
				_ = k8sClient.Delete(ctx, &job, &client.DeleteOptions{
					PropagationPolicy: &propagationPolicy,
				})
			}

			_ = k8sClient.Delete(ctx, node)
		})

		// reconcileAndGetMainContainerImage creates a NodeOp with the given spec,
		// reconciles it, and returns the first job's main container image.
		reconcileAndGetMainContainerImage := func(spec kairosiov1alpha1.NodeOpSpec) string {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      NodeOpName,
					Namespace: NodeOpNamespace,
				},
				Spec: spec,
			}
			Expect(k8sClient.Create(ctx, nodeOp)).Should(Succeed())

			_, err := (&NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}).Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      NodeOpName,
					Namespace: NodeOpNamespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(NodeOpNamespace),
				client.MatchingLabels{"kairos.io/nodeop": NodeOpName})).Should(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			return jobList.Items[0].Spec.Template.Spec.Containers[0].Image
		}

		It("should use Spec.Image for NodeOp container, taking precedence over NODEOP_DEFAULT_IMAGE", func() {
			Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "env-image")).To(Succeed())
			defer func() { Expect(os.Unsetenv("NODEOP_DEFAULT_IMAGE")).To(Succeed()) }()

			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command: []string{"echo", "test"},
				Image:   "spec-image",
			})
			Expect(image).To(Equal("spec-image"))
		})

		It("should use NODEOP_DEFAULT_IMAGE for NodeOp container when Spec.Image is empty", func() {
			Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "env-image")).To(Succeed())
			defer func() { Expect(os.Unsetenv("NODEOP_DEFAULT_IMAGE")).To(Succeed()) }()

			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command: []string{"echo", "test"},
			})
			Expect(image).To(Equal("env-image"))
		})

		It("should use busybox:latest for NodeOp container when neither NODEOP_DEFAULT_IMAGE nor Spec.Image are set", func() {
			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command: []string{"echo", "test"},
			})
			Expect(image).To(Equal("busybox:latest"))
		})

		It("should use value of SENTINEL_IMAGE for sentinel container when it is set", func() {
			Expect(os.Setenv("SENTINEL_IMAGE", "sentinel-env-image")).To(Succeed())
			defer func() { Expect(os.Unsetenv("SENTINEL_IMAGE")).To(Succeed()) }()

			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				Image:           "nodeop-spec-image",
				RebootOnSuccess: asBool(true),
			})
			Expect(image).To(Equal("sentinel-env-image"))
		})

		It("should use Spec.Image for sentinel container when SENTINEL_IMAGE is not set", func() {
			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				Image:           "nodeop-spec-image",
				RebootOnSuccess: asBool(true),
			})
			Expect(image).To(Equal("nodeop-spec-image"))
		})

		It("should use busybox:latest for sentinel container when neither SENTINEL_IMAGE nor Spec.Image are set", func() {
			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				RebootOnSuccess: asBool(true),
			})
			Expect(image).To(Equal("busybox:latest"))
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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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

		It("should respect ImagePullSecrets setting in created Jobs", func() {
			By("Creating a NodeOp with ImagePullSecrets")
			imagePullSecretsNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-imagepull", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: "test-registry-secret"},
						{Name: "another-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, imagePullSecretsNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, imagePullSecretsNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      imagePullSecretsNodeOp.Name,
					Namespace: imagePullSecretsNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with correct ImagePullSecrets")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": imagePullSecretsNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "test-registry-secret"}))
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "another-secret"}))
		})

		It("should respect ImagePullSecrets setting in Jobs with RebootOnSuccess", func() {
			By("Creating a NodeOp with ImagePullSecrets and RebootOnSuccess=true")
			imagePullSecretsRebootNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-imagepull-reboot", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: "test-registry-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, imagePullSecretsRebootNodeOp)).To(Succeed())

			// Cleanup this test's NodeOp
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, imagePullSecretsRebootNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the NodeOp")
			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      imagePullSecretsRebootNodeOp.Name,
					Namespace: imagePullSecretsRebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Job was created with correct ImagePullSecrets even with reboot enabled")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": imagePullSecretsRebootNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "test-registry-secret"}))

			By("Verifying Job structure is correct for reboot case")
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1), "Job should have InitContainer for user command")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have main container for sentinel")
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
			Expect(markJobAsFailed(ctx, k8sClient, &job)).To(Succeed())

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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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

		It("should not uncordon a node that was already cordoned before the NodeOp ran", func() {
			By("Manually cordoning the node before any NodeOp activity")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			By("Creating a NodeOp with Cordon enabled and RebootOnSuccess disabled")
			preCordonedNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-precordoned", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Cordon:          asBool(true),
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, preCordonedNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, preCordonedNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to create the Job")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      preCordonedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Completing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": preCordonedNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsCompleted(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling again to process job completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      preCordonedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node remains cordoned because the operator did not cordon it")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(),
				"Node should remain cordoned because the operator did not cordon it itself")
		})

		It("should not re-uncordon a node that was manually cordoned after the NodeOp completed", func() {
			By("Creating a NodeOp with Cordon enabled and RebootOnSuccess disabled")
			repeatedUncordonNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-rerun", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Cordon:          asBool(true),
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, repeatedUncordonNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, repeatedUncordonNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to create the Job (which cordons the node)")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      repeatedUncordonNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should be cordoned by the operator")

			By("Completing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": repeatedUncordonNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsCompleted(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling to let the operator uncordon the node")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      repeatedUncordonNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should be uncordoned by the operator")

			By("User manually re-cordons the node (e.g. for maintenance)")
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			By("Reconciling again (simulating the periodic 5-minute requeue)")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      repeatedUncordonNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node remains cordoned because the operator already uncordoned once")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(),
				"Node should remain cordoned; the operator must not uncordon a node it did not cordon itself")
		})

		It("should not uncordon a node whose cordoned-by annotation points at a stale NodeOp UID", func() {
			By("Simulating a stale ownership annotation from a deleted NodeOp instance (same namespace/name)")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			if node.Annotations == nil {
				node.Annotations = map[string]string{}
			}
			staleNodeOpName := fmt.Sprintf("%s-recreated", resourceName)
			// The stale value names the same namespace/name the fresh NodeOp will have.
			// Without UID-aware ownership (the pre-fix format), the fresh NodeOp's owner ref
			// would match this value and the operator would incorrectly uncordon the node.
			node.Annotations["operator.kairos.io/cordoned-by"] = "default/" + staleNodeOpName
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			By("Creating a fresh NodeOp with the same namespace/name (Kubernetes assigns a new UID)")
			recreatedNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      staleNodeOpName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Cordon:          asBool(true),
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, recreatedNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, recreatedNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to create the Job (node is already cordoned, so cordonNode no-ops)")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      recreatedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Completing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": recreatedNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsCompleted(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling again to process job completion")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      recreatedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node remains cordoned because the annotation's UID does not match this NodeOp")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(),
				"Node should remain cordoned; a same-name/different-UID NodeOp must not claim a stale annotation")
		})

		It("should uncordon a node whose job failed when UncordonOnFailure is true", func() {
			By("Creating a NodeOp with Cordon and UncordonOnFailure enabled")
			uncordonNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-uncordon-fail", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:           []string{"echo", "test"},
					Cordon:            asBool(true),
					UncordonOnFailure: asBool(true),
					RebootOnSuccess:   asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, uncordonNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, uncordonNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to cordon the node and create the Job")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      uncordonNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the operator cordoned the node")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should be cordoned by the operator")

			By("Failing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": uncordonNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsFailed(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling again to process the job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      uncordonNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node is uncordoned and the ownership annotation is cleared")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(), "Node should be uncordoned after job failure")
			Expect(node.Annotations).NotTo(HaveKey("operator.kairos.io/cordoned-by"))
		})

		It("should leave a failed node cordoned when UncordonOnFailure is not set", func() {
			By("Creating a NodeOp with Cordon enabled but UncordonOnFailure unset")
			stayCordonedNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-stay-cordoned", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Cordon:          asBool(true),
					RebootOnSuccess: asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, stayCordonedNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, stayCordonedNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to cordon the node and create the Job")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      stayCordonedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Failing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": stayCordonedNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsFailed(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling again to process the job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      stayCordonedNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node remains cordoned")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should remain cordoned when UncordonOnFailure is not set")
		})

		It("should not uncordon a failed node cordoned out-of-band even when UncordonOnFailure is true", func() {
			By("Manually cordoning the node before any NodeOp activity (no ownership annotation)")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			node.Spec.Unschedulable = true
			Expect(k8sClient.Update(ctx, node)).To(Succeed())

			By("Creating a NodeOp with Cordon and UncordonOnFailure enabled")
			outOfBandNodeOp := &kairosiov1alpha1.NodeOp{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "kairos.io/v1alpha1",
					Kind:       "NodeOp",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-outofband", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:           []string{"echo", "test"},
					Cordon:            asBool(true),
					UncordonOnFailure: asBool(true),
					RebootOnSuccess:   asBool(false),
				},
			}
			Expect(k8sClient.Create(ctx, outOfBandNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, outOfBandNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to create the Job (node already cordoned, so cordonNode does not claim ownership)")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      outOfBandNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Failing the Job")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{"kairos.io/nodeop": outOfBandNodeOp.Name}),
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(markJobAsFailed(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

			By("Reconciling again to process the job failure")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      outOfBandNodeOp.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the node remains cordoned because the operator did not cordon it")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(),
				"Node cordoned out-of-band must not be uncordoned by UncordonOnFailure")
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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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

		It("creates the Job with a deterministic Name and embeds it in the reboot Pod's sentinel watch", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			uniqueIDNodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-unique-id", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, uniqueIDNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, uniqueIDNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling once")
			rec := &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := rec.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: uniqueIDNodeOp.Name, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Looking up the Job and the reboot Pod that were created for the same node")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels{"kairos.io/nodeop": uniqueIDNodeOp.Name},
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			job := jobList.Items[0]

			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels{"kairos.io/nodeop": uniqueIDNodeOp.Name, "kairos.io/reboot": "true"},
			)).To(Succeed())
			Expect(podList.Items).To(HaveLen(1))
			rebootPod := podList.Items[0]

			By("Verifying the Job was created with an explicit, unique Name (not GenerateName)")
			Expect(job.Name).NotTo(BeEmpty())
			Expect(job.GenerateName).To(BeEmpty(),
				"Job must be created with a deterministic Name so the reboot Pod can be told exactly which sentinel to watch")

			By("Verifying the reboot Pod's watch pattern is the Job's exact name, not the jobBaseName")
			Expect(rebootPod.Spec.Containers).To(HaveLen(1))
			rebootScript := strings.Join(rebootPod.Spec.Containers[0].Command, "\n")
			Expect(rebootScript).To(ContainSubstring(job.Name+"-*"),
				"reboot Pod must watch for the Job-specific sentinel pattern <jobName>-*")
		})

		It("does not include the racy leftover-sentinel cleanup in the reboot Pod's script", func() {
			By("Creating a NodeOp with RebootOnSuccess=true")
			noCleanupNodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-no-cleanup", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, noCleanupNodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, noCleanupNodeOp)
				}, timeout, interval).Should(Succeed())
			})

			rec := &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := rec.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: noCleanupNodeOp.Name, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())

			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels{"kairos.io/nodeop": noCleanupNodeOp.Name, "kairos.io/reboot": "true"},
			)).To(Succeed())
			Expect(podList.Items).To(HaveLen(1))

			rebootScript := strings.Join(podList.Items[0].Spec.Containers[0].Command, "\n")
			Expect(rebootScript).NotTo(ContainSubstring("Cleaning up any leftover sentinel files"),
				"the leftover-sentinel cleanup must be gone now that the watch pattern is Job-specific")
			Expect(rebootScript).NotTo(ContainSubstring("LEFTOVER_SENTINELS"),
				"the leftover-sentinel cleanup must be gone now that the watch pattern is Job-specific")
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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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
			Expect(markJobAsFailed(ctx, k8sClient, job)).To(Succeed())

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
			Expect(markJobAsFailed(ctx, k8sClient, job)).To(Succeed())

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
					Labels: map[string]string{
						"kubernetes.io/hostname": testNodeName,
					},
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

	// testConcurrencyLimit is a helper function to test concurrency limits
	testConcurrencyLimit := func(ctx context.Context, k8sClient client.Client,
		controllerReconciler *NodeOpReconciler, resourceName string,
		concurrency, expectedInitialJobs, expectedAfterCompletion int) {
		By(fmt.Sprintf("Creating a NodeOp with concurrency=%d", concurrency))
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
				Concurrency: int32(concurrency),
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

		By(fmt.Sprintf("Verifying %d job(s) were created initially", expectedInitialJobs))
		jobList := &batchv1.JobList{}
		err = k8sClient.List(ctx, jobList,
			client.InNamespace("default"),
			client.MatchingLabels(map[string]string{
				"kairos.io/nodeop": resourceName,
			}),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(jobList.Items).To(HaveLen(expectedInitialJobs),
			fmt.Sprintf("Should create %d job(s) initially", expectedInitialJobs))

		By("Simulating first job completion")
		job := &jobList.Items[0]
		Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

		By("Reconciling again to trigger next job creation")
		_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			},
		})
		Expect(err).NotTo(HaveOccurred())

		By(fmt.Sprintf("Verifying %d job(s) exist after completion", expectedAfterCompletion))
		err = k8sClient.List(ctx, jobList,
			client.InNamespace("default"),
			client.MatchingLabels(map[string]string{
				"kairos.io/nodeop": resourceName,
			}),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(jobList.Items).To(HaveLen(expectedAfterCompletion),
			fmt.Sprintf("Should have %d job(s) after completion", expectedAfterCompletion))
	}

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
			testConcurrencyLimit(ctx, k8sClient, controllerReconciler, resourceName, 1, 1, 2)
		})

		It("should respect concurrency limit of 2", func() {
			testConcurrencyLimit(ctx, k8sClient, controllerReconciler, resourceName, 2, 2, 3)
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
			Expect(markJobAsFailed(ctx, k8sClient, job)).To(Succeed())

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
			Expect(markJobAsFailed(ctx, k8sClient, job)).To(Succeed())

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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

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
					Expect(markJobAsCompleted(ctx, k8sClient, &j)).To(Succeed())
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
			Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

			// Get the node name that this job was assigned to
			jobNodeName, exists := job.Labels["kairos.io/node"]
			Expect(exists).To(BeTrue(), "Job should have node label")

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

			Expect(nodeOp.Status.NodeStatuses).To(HaveKey(jobNodeName), "Should have status for the job's node")
			nodeStatus := nodeOp.Status.NodeStatuses[jobNodeName]
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

			Expect(nodeOp.Status.NodeStatuses).To(HaveKey(jobNodeName), "Should have status for the job's node")
			nodeStatus = nodeOp.Status.NodeStatuses[jobNodeName]
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

			By("Verifying new job can now be created for other nodes")
			err = k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			// Since we have 3 nodes and concurrency=1, we should now have 2 jobs
			// (first one completed with reboot completed, second one just started for another node)
			Expect(jobList.Items).To(HaveLen(2), "Should create second job after reboot is completed")
		})
	})
})

var _ = Describe("getTargetNodes ordering", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	var (
		testCtx     context.Context
		workerName1 string
		workerName2 string
		cpName      string
		myNodes     []*corev1.Node
		reconciler  *NodeOpReconciler
	)

	BeforeEach(func() {
		testCtx = context.Background()
		suffix := time.Now().UnixNano()
		// Name the control-plane node so it sorts LAST alphabetically. That
		// way, if getTargetNodes returns nodes in whatever order the API
		// server happened to give them, the CP would come last — only the
		// explicit master-first sort can push it to position 0.
		workerName1 = fmt.Sprintf("sort-aaa-worker-1-%d", suffix)
		workerName2 = fmt.Sprintf("sort-bbb-worker-2-%d", suffix)
		cpName = fmt.Sprintf("sort-zzz-cp-%d", suffix)

		// Intentionally create the control-plane node in the middle so that
		// the only way it ends up first is via the sorting logic.
		worker1 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: workerName1}}
		cp := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   cpName,
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
		}}
		worker2 := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: workerName2}}

		Expect(k8sClient.Create(testCtx, worker1)).To(Succeed())
		Expect(k8sClient.Create(testCtx, cp)).To(Succeed())
		Expect(k8sClient.Create(testCtx, worker2)).To(Succeed())

		myNodes = []*corev1.Node{worker1, cp, worker2}

		reconciler = &NodeOpReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	AfterEach(func() {
		for _, n := range myNodes {
			Eventually(func() error {
				return k8sClient.Delete(testCtx, n)
			}, timeout, interval).Should(Succeed())
		}
	})

	It("should sort control-plane nodes first when no NodeSelector is set", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: "no-selector"},
			Spec:       kairosiov1alpha1.NodeOpSpec{},
		}

		targets, err := reconciler.getTargetNodes(testCtx, nodeOp)
		Expect(err).NotTo(HaveOccurred())

		// Only consider the nodes this test created; other tests' leftover
		// nodes (if any) are irrelevant to the ordering we want to verify.
		mine := map[string]bool{workerName1: true, cpName: true, workerName2: true}
		var ours []string
		for _, n := range targets {
			if mine[n.Name] {
				ours = append(ours, n.Name)
			}
		}
		Expect(ours).To(HaveLen(3))
		Expect(ours[0]).To(Equal(cpName), "control-plane node should be sorted first even without a NodeSelector")
	})
})

var _ = Describe("NodeOp Controller - Preflight", func() {
	const (
		preflightCtxImage = "quay.io/kairos/test:preflight"
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
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())
		uniq := fmt.Sprintf("-%d", time.Now().UnixNano())
		resourceName = "preflight-test" + uniq
		nodeNames = []string{"pf-a" + uniq, "pf-b" + uniq, "pf-c" + uniq}
		nodes = make([]*corev1.Node, 0, len(nodeNames))
		for _, name := range nodeNames {
			n := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:   name,
					Labels: map[string]string{"kubernetes.io/hostname": name},
				},
			}
			Expect(k8sClient.Create(ctx, n)).To(Succeed())
			nodes = append(nodes, n)
		}
		controllerReconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	})

	AfterEach(func() {
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())

		// Delete NodeOp.
		Eventually(func() error {
			r := &kairosiov1alpha1.NodeOp{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: "default"}, r)
			if err != nil {
				return client.IgnoreNotFound(err)
			}
			return k8sClient.Delete(ctx, r)
		}, timeout, interval).Should(Succeed())

		// Delete owned Jobs.
		Eventually(func() error {
			jl := &batchv1.JobList{}
			if err := k8sClient.List(ctx, jl, client.InNamespace("default")); err != nil {
				return err
			}
			for i := range jl.Items {
				j := jl.Items[i]
				for _, o := range j.OwnerReferences {
					if o.Kind == kindNodeOp && o.Name == resourceName {
						prop := metav1.DeletePropagationBackground
						if err := k8sClient.Delete(ctx, &j, &client.DeleteOptions{PropagationPolicy: &prop}); err != nil {
							return err
						}
						break
					}
				}
			}
			return nil
		}, timeout, interval).Should(Succeed())

		// Delete preflight Pods we may have created for this NodeOp.
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{"kairos.io/preflight": "true", "kairos.io/nodeop": resourceName},
		)).To(Succeed())
		for i := range podList.Items {
			pod := podList.Items[i]
			grace := int64(0)
			_ = k8sClient.Delete(ctx, &pod, &client.DeleteOptions{GracePeriodSeconds: &grace})
		}

		// Delete the test nodes.
		for _, n := range nodes {
			node := n
			Eventually(func() error {
				return k8sClient.Delete(ctx, node)
			}, timeout, interval).Should(Succeed())
		}
	})

	// --- Helpers ----------------------------------------------------------

	listPreflightPods := func() []corev1.Pod {
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{"kairos.io/preflight": "true", "kairos.io/nodeop": resourceName},
		)).To(Succeed())
		return podList.Items
	}

	reconcileOnce := func() {
		_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	getNodeOp := func() *kairosiov1alpha1.NodeOp {
		out := &kairosiov1alpha1.NodeOp{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: "default"}, out)).To(Succeed())
		return out
	}

	listOwnedJobs := func() []batchv1.Job {
		jl := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jl,
			client.InNamespace("default"),
			client.MatchingLabels{"kairos.io/nodeop": resourceName},
		)).To(Succeed())
		var owned []batchv1.Job
		for _, j := range jl.Items {
			for _, o := range j.OwnerReferences {
				if o.Kind == kindNodeOp && o.Name == resourceName {
					owned = append(owned, j)
					break
				}
			}
		}
		return owned
	}

	completePreflight := func(pod *corev1.Pod, terminationMessage string) {
		latest := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, latest)).To(Succeed())
		latest.Status.Phase = corev1.PodSucceeded
		latest.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "preflight",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 0,
						Reason:   "Completed",
						Message:  terminationMessage,
					},
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, latest)).To(Succeed())
	}

	failPreflight := func(pod *corev1.Pod) {
		latest := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, latest)).To(Succeed())
		latest.Status.Phase = corev1.PodFailed
		latest.Status.ContainerStatuses = []corev1.ContainerStatus{
			{
				Name: "preflight",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 137,
						Reason:   "DeadlineExceeded",
					},
				},
			},
		}
		Expect(k8sClient.Status().Update(ctx, latest)).To(Succeed())
	}

	// --- Tests ------------------------------------------------------------

	It("preserves existing behavior when Spec.Preflight is nil", func() {
		By("Creating a NodeOp without Spec.Preflight")
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()

		By("Verifying no preflight Pods were created")
		Expect(listPreflightPods()).To(BeEmpty())

		By("Verifying Jobs were created directly (one per node)")
		Eventually(func() int { return len(listOwnedJobs()) }, timeout, interval).Should(Equal(len(nodeNames)))
	})

	It("creates one preflight Pod per node and no Jobs while preflight is in progress", func() {
		By("Creating a NodeOp with Spec.Preflight set")
		deadline := int32(45)
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:         preflightCtxImage,
				Command:       []string{"echo", "test"},
				HostMountPath: "/host",
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command:               []string{"/bin/sh", "-c", "echo nope > /dev/termination-log"},
					ActiveDeadlineSeconds: &deadline,
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()

		By("Verifying one preflight Pod per node was created")
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		got := []string{pods[0].Spec.NodeName, pods[1].Spec.NodeName, pods[2].Spec.NodeName}
		Expect(got).To(ConsistOf(nodeNames))

		By("Verifying each preflight Pod has the expected spec")
		for _, pod := range pods {
			Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(pod.Spec.ActiveDeadlineSeconds).NotTo(BeNil())
			Expect(*pod.Spec.ActiveDeadlineSeconds).To(Equal(int64(deadline)))

			Expect(pod.Spec.Containers).To(HaveLen(1))
			c := pod.Spec.Containers[0]
			Expect(c.Image).To(Equal(preflightCtxImage), "preflight image defaults to Spec.Image when Spec.Preflight.Image is empty")
			Expect(c.Command).To(Equal([]string{"/bin/sh", "-c", "echo nope > /dev/termination-log"}))

			// terminationMessagePolicy defaults to File (zero value). Verify it's not FallbackToLogsOnError.
			Expect(c.TerminationMessagePolicy == "" || c.TerminationMessagePolicy == corev1.TerminationMessageReadFile).To(BeTrue())

			By("Verifying the host root is mounted read-only at Spec.HostMountPath")
			var hostVol *corev1.Volume
			for i := range pod.Spec.Volumes {
				if pod.Spec.Volumes[i].HostPath != nil && pod.Spec.Volumes[i].HostPath.Path == "/" {
					v := pod.Spec.Volumes[i]
					hostVol = &v
					break
				}
			}
			Expect(hostVol).NotTo(BeNil(), "preflight Pod must mount the host root via hostPath")

			var hostMount *corev1.VolumeMount
			for i := range c.VolumeMounts {
				if c.VolumeMounts[i].Name == hostVol.Name {
					m := c.VolumeMounts[i]
					hostMount = &m
					break
				}
			}
			Expect(hostMount).NotTo(BeNil())
			Expect(hostMount.MountPath).To(Equal("/host"))
			Expect(hostMount.ReadOnly).To(BeTrue())

			By("Verifying labels and owner ref")
			Expect(pod.Labels).To(HaveKeyWithValue("kairos.io/preflight", "true"))
			Expect(pod.Labels).To(HaveKeyWithValue("kairos.io/nodeop", resourceName))
			Expect(pod.Labels).To(HaveKeyWithValue("kairos.io/node", pod.Spec.NodeName))
			Expect(pod.OwnerReferences).To(HaveLen(1))
			Expect(pod.OwnerReferences[0].Kind).To(Equal(kindNodeOp))
			Expect(pod.OwnerReferences[0].Name).To(Equal(resourceName))
		}

		By("Verifying NodeStatus.Phase = Preflight for every targeted node")
		updated := getNodeOp()
		for _, n := range nodeNames {
			Expect(updated.Status.NodeStatuses).To(HaveKey(n))
			Expect(updated.Status.NodeStatuses[n].Phase).To(Equal("Preflight"))
		}

		By("Verifying no Jobs were created while preflight is in progress")
		Expect(listOwnedJobs()).To(BeEmpty())

		By("Verifying no nodes were cordoned")
		for _, name := range nodeNames {
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeFalse(),
				fmt.Sprintf("node %s must NOT be cordoned during preflight", name))
		}
	})

	It("is idempotent: a second reconcile does not create more preflight Pods", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		first := listPreflightPods()
		Expect(first).To(HaveLen(len(nodeNames)))

		reconcileOnce()
		second := listPreflightPods()
		Expect(second).To(HaveLen(len(first)))
	})

	It("reuses an existing preflight Pod and records NodeStatus when one is found without a status entry", func() {
		By("Manually creating a preflight Pod for a node BEFORE the NodeOp's status is populated")
		// This simulates the partial-failure scenario where a prior reconcile
		// created the Pod but failed to write Status.Update afterwards — the
		// next reconcile must reuse the existing Pod, not create a duplicate.
		uniq := nodeNames[0]
		preExisting := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: resourceName + "-preflight-",
				Namespace:    "default",
				Labels: map[string]string{
					"kairos.io/nodeop":    resourceName,
					"kairos.io/preflight": "true",
					"kairos.io/node":      uniq,
				},
			},
			Spec: corev1.PodSpec{
				NodeName:      uniq,
				RestartPolicy: corev1.RestartPolicyOnFailure,
				Containers: []corev1.Container{{
					Name:    "preflight",
					Image:   preflightCtxImage,
					Command: []string{"/bin/sh", "-c", "true"},
				}},
			},
		}
		Expect(k8sClient.Create(ctx, preExisting)).To(Succeed())

		By("Creating the NodeOp with no status yet")
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()

		By("Verifying only one preflight Pod exists for this node (existing one was reused, not duplicated)")
		podsForNode := []corev1.Pod{}
		for _, p := range listPreflightPods() {
			if p.Spec.NodeName == uniq {
				podsForNode = append(podsForNode, p)
			}
		}
		Expect(podsForNode).To(HaveLen(1))
		Expect(podsForNode[0].Name).To(Equal(preExisting.Name),
			"the existing preflight Pod must be reused; the controller must not create a duplicate")

		By("Verifying NodeStatus.Phase is recorded as Preflight even though the Pod was pre-existing")
		updated := getNodeOp()
		Expect(updated.Status.NodeStatuses).To(HaveKey(uniq))
		Expect(updated.Status.NodeStatuses[uniq].Phase).To(Equal("Preflight"))
	})

	It("skips a node when its preflight Pod terminates with a non-empty message", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		By("Marking the first preflight Pod as Succeeded with a skip reason")
		completePreflight(&pods[0], "node is already at v4.0.3")

		reconcileOnce()

		skippedNode := pods[0].Spec.NodeName
		updated := getNodeOp()
		status, ok := updated.Status.NodeStatuses[skippedNode]
		Expect(ok).To(BeTrue())
		Expect(status.Phase).To(Equal("Completed"))
		Expect(status.JobName).To(BeEmpty())
		Expect(status.Message).To(Equal("Skipped by preflight: node is already at v4.0.3"))

		By("Verifying no Job was created for the skipped node")
		for _, j := range listOwnedJobs() {
			Expect(j.Labels["kairos.io/node"]).NotTo(Equal(skippedNode))
		}

		By("Verifying the skipped node was NOT cordoned")
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: skippedNode}, node)).To(Succeed())
		Expect(node.Spec.Unschedulable).To(BeFalse())

		By("Verifying the preflight Pod was cleaned up after the verdict was recorded")
		Eventually(func() bool {
			p := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: pods[0].Name, Namespace: "default"}, p)
			return apierrors.IsNotFound(err) || p.DeletionTimestamp != nil
		}, timeout, interval).Should(BeTrue(),
			"the preflight Pod for the skipped node must be deleted once the verdict is recorded")
	})

	It("proceeds with cordon/drain/Job when preflight terminates with an empty message", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Cordon:  asBool(true),
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		By("Marking the first preflight Pod as Succeeded with an EMPTY message (proceed)")
		completePreflight(&pods[0], "")

		reconcileOnce()

		proceedNode := pods[0].Spec.NodeName
		updated := getNodeOp()
		status, ok := updated.Status.NodeStatuses[proceedNode]
		Expect(ok).To(BeTrue())
		Expect(status.Phase).NotTo(Equal("Preflight"), "node should have moved past Preflight after empty-message verdict")
		Expect(status.Phase).NotTo(Equal("Completed"), "an empty-message verdict means proceed, not skip")
		Expect(status.JobName).NotTo(BeEmpty(), "a Job should have been created for the proceeding node")

		By("Verifying the proceeding node was cordoned")
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: proceedNode}, node)).To(Succeed())
		Expect(node.Spec.Unschedulable).To(BeTrue())

		By("Verifying the preflight Pod was cleaned up after the verdict was recorded")
		Eventually(func() bool {
			p := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: pods[0].Name, Namespace: "default"}, p)
			return apierrors.IsNotFound(err) || p.DeletionTimestamp != nil
		}, timeout, interval).Should(BeTrue(),
			"the preflight Pod must be deleted once the controller decides to proceed with the main Job")
	})

	It("marks a node Failed when its preflight Pod ends up in PodFailed", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "exit 5"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		By("Marking the first preflight Pod as PodFailed")
		failPreflight(&pods[0])

		reconcileOnce()

		failedNode := pods[0].Spec.NodeName
		updated := getNodeOp()
		status, ok := updated.Status.NodeStatuses[failedNode]
		Expect(ok).To(BeTrue())
		Expect(status.Phase).To(Equal("Failed"))
		Expect(status.JobName).To(BeEmpty(), "no Job should have been created for a node that failed preflight")
		Expect(status.Message).To(ContainSubstring("Preflight"))

		By("Verifying the failed-preflight node was NOT cordoned")
		node := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: failedNode}, node)).To(Succeed())
		Expect(node.Spec.Unschedulable).To(BeFalse())

		By("Verifying the preflight Pod was cleaned up after the failure was recorded")
		Eventually(func() bool {
			p := &corev1.Pod{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: pods[0].Name, Namespace: "default"}, p)
			return apierrors.IsNotFound(err) || p.DeletionTimestamp != nil
		}, timeout, interval).Should(BeTrue(),
			"the preflight Pod must be deleted once the controller records the Failed verdict")
	})

	It("leaves NodeStatus.Phase=Preflight while the Pod has not terminated", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "sleep 60"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		By("Reconciling again without touching Pod status")
		reconcileOnce()

		updated := getNodeOp()
		for _, n := range nodeNames {
			Expect(updated.Status.NodeStatuses[n].Phase).To(Equal("Preflight"))
		}
		Expect(listOwnedJobs()).To(BeEmpty())
	})

	It("counts Preflight against Concurrency: with Concurrency=1, only one preflight Pod runs at a time", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:       preflightCtxImage,
				Command:     []string{"echo", "test"},
				Concurrency: 1,
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()

		pods := listPreflightPods()
		Expect(pods).To(HaveLen(1), "with Concurrency=1 only the first node should be in Preflight")
		firstNode := pods[0].Spec.NodeName

		By("Skipping the first preflight Pod so its slot becomes free")
		completePreflight(&pods[0], "skipped")

		reconcileOnce()

		By("Verifying the freed slot was reused for a different node (the first Pod was cleaned up after its verdict)")
		Eventually(func() bool {
			current := listPreflightPods()
			if len(current) != 1 {
				return false
			}
			return current[0].Spec.NodeName != firstNode
		}, timeout, interval).Should(BeTrue(),
			"after the first verdict freed its slot, a preflight Pod should now exist for a different node")
	})

	It("uses Spec.Preflight.Image when set, leaving the main Job's image alone", func() {
		const preflightOverride = "quay.io/kairos/test:preflight-only"
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
					Image:   preflightOverride,
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))
		for _, pod := range pods {
			Expect(pod.Spec.Containers[0].Image).To(Equal(preflightOverride))
		}

		By("Completing all preflights with empty messages so the main Job runs")
		for i := range pods {
			completePreflight(&pods[i], "")
		}

		reconcileOnce()

		jobs := listOwnedJobs()
		Expect(jobs).To(HaveLen(len(nodeNames)))
		for _, job := range jobs {
			containers := job.Spec.Template.Spec.Containers
			if len(containers) == 0 {
				containers = job.Spec.Template.Spec.InitContainers
			}
			Expect(containers).NotTo(BeEmpty())
			Expect(containers[0].Image).To(Equal(preflightCtxImage),
				"the main Job container must keep Spec.Image, not the preflight override")
		}
	})

	It("defaults Spec.Preflight.ActiveDeadlineSeconds to a sane value when not set", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:   preflightCtxImage,
				Command: []string{"echo", "test"},
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))
		for _, pod := range pods {
			Expect(pod.Spec.ActiveDeadlineSeconds).NotTo(BeNil(),
				"preflight Pod must always have an ActiveDeadlineSeconds to guarantee forward progress")
			Expect(*pod.Spec.ActiveDeadlineSeconds).To(BeNumerically(">", 0))
		}
	})
})
