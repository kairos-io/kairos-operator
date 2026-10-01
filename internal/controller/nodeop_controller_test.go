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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	"github.com/kairos-io/kairos-operator/internal/bootid"
	"github.com/kairos-io/kairos-operator/internal/rebootwatcher"
)

var _ = Describe("getNodeOpImage", func() {
	var nodeOp *kairosiov1alpha1.NodeOp

	BeforeEach(func() {
		nodeOp = &kairosiov1alpha1.NodeOp{}
		Expect(os.Unsetenv("NODEOP_DEFAULT_IMAGE")).To(Succeed())
	})

	It("should return Spec.Image when set", func() {
		nodeOp.Spec.Image = "spec-image"
		Expect(getNodeOpImage(nodeOp)).To(Equal("spec-image"))
	})

	It("should return the value of NODEOP_DEFAULT_IMAGE when it is set and Spec.Image is empty", func() {
		Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "nodeop-env-image")).To(Succeed())
		Expect(getNodeOpImage(nodeOp)).To(Equal("nodeop-env-image"))
	})

	It("should return busybox:latest when Spec.Image and NODEOP_DEFAULT_IMAGE are empty", func() {
		Expect(getNodeOpImage(nodeOp)).To(Equal("busybox:latest"))
	})
})

var _ = Describe("bootIDReporterImage", func() {
	var nodeOp *kairosiov1alpha1.NodeOp

	BeforeEach(func() {
		nodeOp = &kairosiov1alpha1.NodeOp{}
		Expect(os.Unsetenv("SENTINEL_IMAGE")).To(Succeed())
	})

	It("should return the value of SENTINEL_IMAGE when it is set", func() {
		Expect(os.Setenv("SENTINEL_IMAGE", "sentinel-env-image")).To(Succeed())
		Expect(bootIDReporterImage(nodeOp)).To(Equal("sentinel-env-image"))
	})

	It("should return Spec.Image when SENTINEL_IMAGE is empty", func() {
		nodeOp.Spec.Image = "spec-image"
		Expect(bootIDReporterImage(nodeOp)).To(Equal("spec-image"))
	})

	It("should return busybox:latest when Spec.Image and SENTINEL_IMAGE are empty", func() {
		Expect(bootIDReporterImage(nodeOp)).To(Equal("busybox:latest"))
	})
})

var _ = Describe("getHostMountPath", func() {
	var nodeOp *kairosiov1alpha1.NodeOp

	BeforeEach(func() {
		nodeOp = &kairosiov1alpha1.NodeOp{}
	})

	It("should return Spec.HostMountPath when set", func() {
		nodeOp.Spec.HostMountPath = "/mnt/kairos-host"
		Expect(getHostMountPath(nodeOp)).To(Equal("/mnt/kairos-host"))
	})

	It("should return /host when Spec.HostMountPath is empty", func() {
		Expect(getHostMountPath(nodeOp)).To(Equal("/host"))
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
	var NodeOpName string

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

	Context("When resolving NodeOp and boot-id-reporter container images", func() {
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
			Expect(os.Unsetenv("NODEOP_DEFAULT_IMAGE")).To(Succeed())
			Expect(os.Unsetenv("SENTINEL_IMAGE")).To(Succeed())
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
			if getBool(spec.RebootOnSuccess, RebootOnSuccessDefault) {
				runRebootPodsAndReconcile(ctx, &NodeOpReconciler{
					Client: k8sClient,
					Scheme: k8sClient.Scheme(),
				}, nodeOp)
			}

			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(NodeOpNamespace),
				client.MatchingLabels{"kairos.io/nodeop": NodeOpName})).Should(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			return jobList.Items[0].Spec.Template.Spec.Containers[0].Image
		}

		It("should use Spec.Image for NodeOp container, taking precedence over NODEOP_DEFAULT_IMAGE", func() {
			Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "env-image")).To(Succeed())

			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command: []string{"echo", "test"},
				Image:   "spec-image",
			})
			Expect(image).To(Equal("spec-image"))
		})

		It("should use NODEOP_DEFAULT_IMAGE for NodeOp container when Spec.Image is empty", func() {
			Expect(os.Setenv("NODEOP_DEFAULT_IMAGE", "env-image")).To(Succeed())

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

		It("should use value of SENTINEL_IMAGE for boot-id-reporter container when it is set", func() {
			Expect(os.Setenv("SENTINEL_IMAGE", "sentinel-env-image")).To(Succeed())

			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				Image:           "nodeop-spec-image",
				RebootOnSuccess: asBool(true),
			})
			Expect(image).To(Equal("sentinel-env-image"))
		})

		It("should use Spec.Image for boot-id-reporter container when SENTINEL_IMAGE is not set", func() {
			image := reconcileAndGetMainContainerImage(kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				Image:           "nodeop-spec-image",
				RebootOnSuccess: asBool(true),
			})
			Expect(image).To(Equal("nodeop-spec-image"))
		})

		It("should use busybox:latest for boot-id-reporter container when neither SENTINEL_IMAGE nor Spec.Image are set", func() {
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, imagePullSecretsRebootNodeOp)

			By("Verifying Job was created with correct ImagePullSecrets even with reboot enabled")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
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
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have the boot-id-reporter main container")
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, cordonDrainNodeOp)

			By("Verifying node is cordoned")
			node := &corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should be cordoned")

			By("Verifying job was created")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
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

			By("Simulating the node rebooting after the upgrade")
			jobRebootsItsNode(ctx, job)

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
			Expect(k8sClient.List(
				ctx, jobList,
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
			Expect(k8sClient.List(
				ctx, jobList,
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
			Expect(k8sClient.List(
				ctx, jobList,
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
			Expect(k8sClient.List(
				ctx, jobList,
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
			Expect(k8sClient.List(
				ctx, jobList,
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
			Expect(k8sClient.List(
				ctx, jobList,
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

			// The first reconcile creates the reboot Pod. The upgrade Job is
			// created once the reboot Pod is ready.
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      rebootNodeOp.Name,
					Namespace: rebootNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			runRebootPodsAndReconcile(ctx, controllerReconciler, rebootNodeOp)

			// Verify Job was created
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, podList,
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
			Expect(rebootPod.Spec.Containers[0].Command).To(Equal(rebootwatcher.Command()))
			Expect(rebootPod.Spec.Containers[0].Env).To(HaveLen(2),
				"the reboot watcher needs the Job name and namespace to find its Job")
			Expect(rebootPod.Spec.Containers[0].Env[0].Name).To(Equal(rebootwatcher.JobNameEnv))
			Expect(rebootPod.Spec.Containers[0].Env[0].Value).To(Equal(job.Name))
			Expect(rebootPod.Spec.Containers[0].Env[1].Name).To(Equal(rebootwatcher.NamespaceEnv))
			Expect(rebootPod.Spec.Containers[0].SecurityContext.Privileged).To(PointTo(BeTrue()))
			Expect(rebootPod.Spec.Containers[0].Env[1].ValueFrom).To(Equal(&corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"},
			}), "the reboot watcher reads its namespace from the Downward API")
			Expect(rebootPod.Spec.Volumes).To(BeEmpty(),
				"the reboot watcher reads its Job through the API and needs no host volume")
			Expect(rebootPod.Spec.Containers[0].VolumeMounts).To(BeEmpty())
			Expect(rebootPod.Spec.HostPID).To(BeTrue(), "the reboot watcher enters the host PID namespace to reboot")
			Expect(rebootPod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
			Expect(rebootPod.Spec.ServiceAccountName).To(Equal(rebootNodeOp.Name+"-reboot"),
				"the reboot Pod runs under the per-NodeOp ServiceAccount that may read its Job")
			Expect(rebootPod.Spec.AutomountServiceAccountToken).To(BeNil(),
				"the reboot watcher needs the ServiceAccount token to read its Job")
			Expect(rebootPod.Spec.Tolerations).To(ContainElements(
				corev1.Toleration{
					Key:      corev1.TaintNodeNotReady,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
				corev1.Toleration{
					Key:      corev1.TaintNodeUnreachable,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
			), "reboot pod must tolerate NotReady/Unreachable without tolerationSeconds so taint-eviction cannot delete it during the reboot it triggers")

			// Verify node is still cordoned
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
			Expect(node.Spec.Unschedulable).To(BeTrue(), "Node should remain cordoned until reboot is completed")

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

			// Simulate the node rebooting after the upgrade
			jobRebootsItsNode(ctx, job)

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

			By("Verifying the read-only reboot RBAC was created in the NodeOp's namespace")
			expectRebootRBAC(ctx, rebootNodeOp)

			err = k8sClient.Get(ctx, types.NamespacedName{
				Name: fmt.Sprintf("nodeop-reboot-%s", rebootNodeOp.Name),
			}, &rbacv1.ClusterRoleBinding{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"no per-NodeOp reboot ClusterRoleBinding should exist, got err=%v", err)

			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      rebootNodeOp.Name,
				Namespace: rebootNodeOp.Namespace,
			}, rebootNodeOp)).To(Succeed())
			Expect(rebootNodeOp.Finalizers).NotTo(ContainElement("nodeop-reboot.kairos.io/clusterrolebinding"),
				"a NodeOp without a reboot ClusterRoleBinding needs no cleanup finalizer")
		})

		// A released operator created a ServiceAccount with the same name,
		// controlled by the same NodeOp, for its reboot Pod.
		It("should reuse a reboot ServiceAccount the NodeOp already owns", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-existing-sa", resourceName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
			DeferCleanup(func() {
				Eventually(func() error {
					return client.IgnoreNotFound(k8sClient.Delete(ctx, nodeOp))
				}, timeout, interval).Should(Succeed())
			})

			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeOp.Name + "-reboot",
					Namespace: nodeOp.Namespace,
				},
			}
			Expect(controllerutil.SetControllerReference(nodeOp, sa, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Create(ctx, sa)).To(Succeed())

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: nodeOp.Name, Namespace: nodeOp.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())

			podList := &corev1.PodList{}
			Expect(k8sClient.List(
				ctx, podList,
				client.InNamespace(nodeOp.Namespace),
				client.MatchingLabels{"kairos.io/nodeop": nodeOp.Name, "kairos.io/reboot": "true"},
			)).To(Succeed())
			Expect(podList.Items).To(HaveLen(1))
			Expect(podList.Items[0].Spec.ServiceAccountName).To(Equal(sa.Name))

			expectRebootRBAC(ctx, nodeOp)
		})

		// NodeOps created by released operator versions carry the
		// clusterrolebinding finalizer and own a cluster-wide
		// ClusterRoleBinding named nodeop-reboot-<name>.
		It("should remove the reboot ClusterRoleBinding and the finalizer when deleting a NodeOp that carries them", func() {
			legacyNodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf("%s-legacy", resourceName),
					Namespace:  "default",
					Finalizers: []string{"nodeop-reboot.kairos.io/clusterrolebinding"},
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, legacyNodeOp)).To(Succeed())

			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("nodeop-reboot-%s", legacyNodeOp.Name),
				},
				Subjects: []rbacv1.Subject{{
					Kind:      "ServiceAccount",
					Name:      fmt.Sprintf("%s-reboot", legacyNodeOp.Name),
					Namespace: "default",
				}},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "nodeop-reboot",
				},
			}
			Expect(k8sClient.Create(ctx, crb)).To(Succeed())

			Expect(k8sClient.Delete(ctx, legacyNodeOp)).To(Succeed())

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      legacyNodeOp.Name,
					Namespace: legacyNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{Name: crb.Name}, &rbacv1.ClusterRoleBinding{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"the reboot ClusterRoleBinding should be deleted, got err=%v", err)

			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      legacyNodeOp.Name,
				Namespace: legacyNodeOp.Namespace,
			}, &kairosiov1alpha1.NodeOp{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"removing the finalizer should let the NodeOp deletion complete, got err=%v", err)
		})

		It("should remove the finalizer when deleting a NodeOp whose reboot ClusterRoleBinding is already gone", func() {
			legacyNodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf("%s-legacy-nocrb", resourceName),
					Namespace:  "default",
					Finalizers: []string{"nodeop-reboot.kairos.io/clusterrolebinding"},
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{"echo", "test"},
				},
			}
			Expect(k8sClient.Create(ctx, legacyNodeOp)).To(Succeed())
			Expect(k8sClient.Delete(ctx, legacyNodeOp)).To(Succeed())

			controllerReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      legacyNodeOp.Name,
					Namespace: legacyNodeOp.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      legacyNodeOp.Name,
				Namespace: legacyNodeOp.Namespace,
			}, &kairosiov1alpha1.NodeOp{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"removing the finalizer should let the NodeOp deletion complete, got err=%v", err)
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
			err = k8sClient.List(
				ctx, podList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootFirstNodeOp.Name,
					"kairos.io/reboot": "true",
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(podList.Items).To(HaveLen(1), "Reboot pod should be created before Jobs when RebootOnSuccess is true")

			By("Verifying the upgrade Job is created once the reboot Pod is ready")
			runRebootPodsAndReconcile(ctx, controllerReconciler, rebootFirstNodeOp)
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": rebootFirstNodeOp.Name,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Job should also be created")

			By("Verifying the upgrade Job has the upgrade init container and the boot-id-reporter container")
			job := &jobList.Items[0]
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1), "Job should have exactly one InitContainer for user command")
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have exactly one main container, the boot-id-reporter")

			// Verify InitContainer (user's workload)
			initContainer := job.Spec.Template.Spec.InitContainers[0]
			Expect(initContainer.Name).To(Equal("nodeop"))
			Expect(initContainer.Command).To(Equal([]string{"echo", "test"}))
			Expect(initContainer.Image).To(Equal("busybox:latest"))
			Expect(initContainer.SecurityContext.Privileged).To(PointTo(BeTrue()))
			Expect(initContainer.VolumeMounts).To(HaveLen(1))
			Expect(initContainer.VolumeMounts[0].Name).To(Equal("host-root"))
			Expect(initContainer.VolumeMounts[0].MountPath).To(Equal("/host"))

			// Verify the upgrade Job's boot-id-reporter container
			mainContainer := job.Spec.Template.Spec.Containers[0]
			Expect(mainContainer.Name).To(Equal(bootid.ReporterContainerName))
			Expect(mainContainer.Image).To(Equal("busybox:latest"))
			Expect(mainContainer.Resources.Requests.Cpu().String()).To(Equal("10m"))
			Expect(mainContainer.Resources.Requests.Memory().String()).To(Equal("32Mi"))
			Expect(mainContainer.Resources.Limits.Cpu().String()).To(Equal("10m"))
			Expect(mainContainer.Resources.Limits.Memory().String()).To(Equal("32Mi"))
			// The script only writes the node's boot ID to the termination
			// message, which the operator stores in the NodeOp status.
			Expect(mainContainer.Command).To(Equal([]string{
				"/bin/sh",
				"-c",
				"read -r boot_id < /proc/sys/kernel/random/boot_id" +
					" && printf '%s' \"$boot_id\" > /dev/termination-log",
			}))
			Expect(mainContainer.TerminationMessagePolicy).To(Equal(corev1.TerminationMessageReadFile))
			Expect(mainContainer.VolumeMounts).To(BeEmpty())
			Expect(mainContainer.Env).To(BeEmpty())

			// Verify volumes
			Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(1))
			hostRootVolume := job.Spec.Template.Spec.Volumes[0]
			Expect(hostRootVolume.Name).To(Equal("host-root"))
			Expect(hostRootVolume.VolumeSource.HostPath.Path).To(Equal("/"))
		})

		It("names the upgrade Job up front and gives that name to the reboot Pod", func() {
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
			runRebootPodsAndReconcile(ctx, rec, uniqueIDNodeOp)

			By("Looking up the Job and the reboot Pod that were created for the same node")
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(
				ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels{"kairos.io/nodeop": uniqueIDNodeOp.Name},
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			job := jobList.Items[0]

			podList := &corev1.PodList{}
			Expect(k8sClient.List(
				ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels{"kairos.io/nodeop": uniqueIDNodeOp.Name, "kairos.io/reboot": "true"},
			)).To(Succeed())
			Expect(podList.Items).To(HaveLen(1))
			rebootPod := podList.Items[0]

			By("Verifying the Job was created with an explicit, unique Name (not GenerateName)")
			Expect(job.Name).NotTo(BeEmpty())
			Expect(job.GenerateName).To(BeEmpty(),
				"Job must be created with a deterministic Name so the reboot Pod can be told exactly which Job to read")

			By("Verifying the reboot Pod's JOB_NAME is the upgrade Job's full name")
			Expect(rebootPod.Spec.Containers).To(HaveLen(1))
			var watchedJob string
			for _, e := range rebootPod.Spec.Containers[0].Env {
				if e.Name == rebootwatcher.JobNameEnv {
					watchedJob = e.Value
				}
			}
			Expect(watchedJob).To(Equal(job.Name),
				"reboot Pod must be told the exact name of its upgrade Job")
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, statusNodeOp)

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
			err = k8sClient.List(
				ctx, jobList,
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

			// Overall NodeOp should still be Running, not Completed, because the node has not rebooted yet
			Expect(updatedNodeOp.Status.Phase).To(Equal("Running"), "Overall NodeOp status should remain 'Running' when job completes but the node has not rebooted yet")
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("pending"), "RebootStatus should remain 'pending' after job completion but before reboot completion")
				Expect(nodeStatus.Phase).To(Equal("Completed"))
			}

			By("Simulating the node rebooting after the upgrade - rebootStatus should become 'completed'")
			jobRebootsItsNode(ctx, job)

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

			// Now the overall NodeOp should be Completed since both the job finished and the node rebooted
			Expect(updatedNodeOp.Status.Phase).To(Equal("Completed"), "Overall NodeOp status should be 'Completed' only when both the job completed and the node rebooted")
			for _, nodeStatus := range updatedNodeOp.Status.NodeStatuses {
				Expect(nodeStatus.RebootStatus).To(Equal("completed"), "RebootStatus should be 'completed' after the node rebooted")
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, failedJobNodeOp)

			By("Verifying reboot pod was created")
			podList := &corev1.PodList{}
			err = k8sClient.List(
				ctx, podList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, podList,
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
			err = k8sClient.List(
				ctx, podList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, rebootBackoffNodeOp)

			By("Verifying Job was created with custom BackoffLimit even with reboot enabled")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
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
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1), "Job should have the boot-id-reporter main container")
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
		concurrency, expectedInitialJobs, expectedAfterCompletion int,
	) {
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
		err = k8sClient.List(
			ctx, jobList,
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
		err = k8sClient.List(
			ctx, jobList,
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
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			APIReader: k8sClient,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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

		It("releases a node that waits for its reboot Pod when StopOnFailure trips", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Concurrency:     2,
					StopOnFailure:   asBool(true),
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
			reconcileRequest := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(nodeOp)}

			_, err := controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName, labelKeyReboot: "true"})).To(Succeed())
			Expect(podList.Items).To(HaveLen(2))
			started, waiting := podList.Items[0], podList.Items[1]

			By("Starting the upgrade Job of one node only")
			markRebootPodRunning(ctx, &started)
			_, err = controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName})).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))

			By("Failing that upgrade Job")
			Expect(markJobAsFailed(ctx, k8sClient, &jobList.Items[0])).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[started.Spec.NodeName].Phase).To(Equal(phaseFailed))
			Expect(current.Status.NodeStatuses).NotTo(HaveKey(waiting.Spec.NodeName),
				"a node that never got its Job has not started and must not stay Pending")
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&waiting), pod)).To(Succeed())
			Expect(pod.DeletionTimestamp.IsZero()).To(BeFalse(), "the waiting node's reboot Pod must be deleted")
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName})).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
		})

		It("keeps a waiting node whose upgrade Job already exists when StopOnFailure trips, and saves that upgrade Job in the NodeOp status", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Concurrency:     2,
					StopOnFailure:   asBool(true),
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
			reconcileRequest := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(nodeOp)}

			_, err := controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName, labelKeyReboot: "true"})).To(Succeed())
			Expect(podList.Items).To(HaveLen(2))
			started, waiting := podList.Items[0], podList.Items[1]

			By("Starting the upgrade Job of one node only")
			markRebootPodRunning(ctx, &started)
			_, err = controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName})).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			failedJob := jobList.Items[0]

			By("Creating the waiting node's upgrade Job without saving it in the NodeOp status")
			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(awaitingRebootPod(current.Status.NodeStatuses[waiting.Spec.NodeName])).To(BeTrue())
			node := corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: waiting.Spec.NodeName}, &node)).To(Succeed())
			jobName := rebootPodJobName(&waiting)
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: "default",
					Labels:    map[string]string{labelKeyNodeOp: resourceName, labelKeyNode: node.Name},
				},
				Spec: controllerReconciler.createRebootJobSpec(current, node, 6),
			}
			Expect(controllerutil.SetControllerReference(current, job, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			markRebootPodRunning(ctx, &waiting)

			By("Failing the other node's upgrade Job")
			Expect(markJobAsFailed(ctx, k8sClient, &failedJob)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcileRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[started.Spec.NodeName].Phase).To(Equal(phaseFailed))
			Expect(current.Status.NodeStatuses).To(HaveKey(waiting.Spec.NodeName),
				"a node whose Job runs has started and must stay tracked")
			Expect(current.Status.NodeStatuses[waiting.Spec.NodeName].JobName).To(Equal(jobName))
			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&waiting), pod)).To(Succeed())
			Expect(pod.DeletionTimestamp.IsZero()).To(BeTrue(), "the reboot Pod of a node whose Job runs must stay")
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(2), "Should not create job for third node not in target list")
		})
	})

	Context("When the reboot Pod has not started yet", func() {
		var nodeOp *kairosiov1alpha1.NodeOp

		reconcileNodeOp := func() {
			GinkgoHelper()
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"},
			})
			Expect(err).NotTo(HaveOccurred())
		}

		listJobs := func() []batchv1.Job {
			GinkgoHelper()
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName},
			)).To(Succeed())
			return jobList.Items
		}

		listRebootPods := func() []corev1.Pod {
			GinkgoHelper()
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList,
				client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName, labelKeyReboot: "true"},
			)).To(Succeed())
			return podList.Items
		}

		BeforeEach(func() {
			nodeOp = &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command:         []string{"echo", "test"},
					Concurrency:     1,
					RebootOnSuccess: asBool(true),
				},
			}
			Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
		})

		It("creates no upgrade Job while the reboot Pod is Pending", func() {
			reconcileNodeOp()

			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			Expect(listJobs()).To(BeEmpty(),
				"the upgrade must not run before the Pod that reboots the node is ready")

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses).To(HaveKey(pods[0].Spec.NodeName))
			status := current.Status.NodeStatuses[pods[0].Spec.NodeName]
			Expect(status.Phase).To(Equal(phasePending))
			Expect(status.JobName).To(BeEmpty())
			Expect(status.Message).To(Equal("Waiting for the reboot Pod to be ready"))
			Expect(status.RebootStatus).To(Equal(rebootStatusPending))
		})

		It("creates neither a second reboot Pod nor an upgrade Job on later reconciles while the reboot Pod is Pending", func() {
			reconcileNodeOp()
			reconcileNodeOp()
			reconcileNodeOp()

			Expect(listRebootPods()).To(HaveLen(1))
			Expect(listJobs()).To(BeEmpty())
		})

		It("counts the waiting node against the concurrency budget", func() {
			reconcileNodeOp()
			reconcileNodeOp()

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses).To(HaveLen(1),
				"with Concurrency=1 no other node may start while the first waits for its reboot Pod")
			Expect(listRebootPods()).To(HaveLen(1))
		})

		It("creates the upgrade Job named in the reboot Pod once the reboot Pod is ready", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			jobName := pods[0].Spec.Containers[0].Env[0].Value
			Expect(pods[0].Spec.Containers[0].Env[0].Name).To(Equal(rebootwatcher.JobNameEnv))

			markRebootPodsRunning(ctx, nodeOp)
			reconcileNodeOp()

			jobs := listJobs()
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(jobName))
			Expect(jobs[0].Labels[labelKeyNode]).To(Equal(pods[0].Spec.NodeName))
			Expect(listRebootPods()).To(HaveLen(1))

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[pods[0].Spec.NodeName].JobName).To(Equal(jobName))
		})

		It("saves an upgrade Job that exists but is missing from the NodeOp status", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			rebootPod := pods[0]
			jobName := rebootPodJobName(&rebootPod)

			By("Creating the upgrade Job without saving it in the NodeOp status")
			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			node := corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rebootPod.Spec.NodeName}, &node)).To(Succeed())
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: "default",
					Labels:    map[string]string{labelKeyNodeOp: resourceName, labelKeyNode: node.Name},
				},
				Spec: controllerReconciler.createRebootJobSpec(current, node, 6),
			}
			Expect(controllerutil.SetControllerReference(current, job, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			Expect(awaitingRebootPod(current.Status.NodeStatuses[node.Name])).To(BeTrue())

			markRebootPodsRunning(ctx, nodeOp)
			reconcileNodeOp()

			Expect(listJobs()).To(HaveLen(1))
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[node.Name].JobName).To(Equal(jobName))
		})

		It("saves an upgrade Job missing from the NodeOp status without draining the upgrade Job's Pod off the node", func() {
			By("Asking for cordon and drain")
			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			current.Spec.Cordon = asBool(true)
			current.Spec.DrainOptions = &kairosiov1alpha1.DrainOptions{Enabled: asBool(true)}
			Expect(k8sClient.Update(ctx, current)).To(Succeed())

			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			rebootPod := pods[0]
			jobName := rebootPodJobName(&rebootPod)

			By("Creating the upgrade Job and its running Pod without saving the upgrade Job in the NodeOp status")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			node := corev1.Node{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: rebootPod.Spec.NodeName}, &node)).To(Succeed())
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: "default",
					Labels:    map[string]string{labelKeyNodeOp: resourceName, labelKeyNode: node.Name},
				},
				Spec: controllerReconciler.createRebootJobSpec(current, node, 6),
			}
			Expect(controllerutil.SetControllerReference(current, job, k8sClient.Scheme())).To(Succeed())
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			jobPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName + "-pod",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "batch/v1",
						Kind:       "Job",
						Name:       job.Name,
						UID:        job.UID,
						Controller: asBool(true),
					}},
				},
				Spec: corev1.PodSpec{
					NodeName:      node.Name,
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "upgrade", Image: "busybox"}},
				},
			}
			Expect(k8sClient.Create(ctx, jobPod)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, jobPod, client.GracePeriodSeconds(0)))).To(Succeed())
			})
			jobPod.Status.Phase = corev1.PodRunning
			Expect(k8sClient.Status().Update(ctx, jobPod)).To(Succeed())

			markRebootPodsRunning(ctx, nodeOp)
			reconcileNodeOp()

			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[node.Name].JobName).To(Equal(jobName))
			still := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobPod), still)).To(Succeed(),
				"recording a Job that already runs must not drain that Job's Pod")
			Expect(still.DeletionTimestamp.IsZero()).To(BeTrue())
		})

		It("refuses to use an upgrade Job of the same name that does not belong to this NodeOp", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			jobName := rebootPodJobName(&pods[0])

			foreign := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: "default"},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers:    []corev1.Container{{Name: "c", Image: "busybox"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, foreign))).To(Succeed())
			})

			markRebootPodsRunning(ctx, nodeOp)
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"},
			})
			Expect(err).To(HaveOccurred())

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[pods[0].Spec.NodeName].JobName).To(BeEmpty())
		})

		It("replaces a reboot Pod that has stopped (Succeeded or Failed)", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			failed := &pods[0]
			failedJobName := rebootPodJobName(failed)

			By("Making the reboot Pod fail, as a node-pressure eviction would")
			failed.Status.Phase = corev1.PodFailed
			failed.Status.Reason = "Evicted"
			Expect(k8sClient.Status().Update(ctx, failed)).To(Succeed())

			reconcileNodeOp()

			var replacements []corev1.Pod
			for _, pod := range listRebootPods() {
				if pod.Name != failed.Name {
					replacements = append(replacements, pod)
				}
			}
			Expect(replacements).To(HaveLen(1), "a fresh reboot Pod must replace the Failed one")
			Expect(rebootPodJobName(&replacements[0])).NotTo(Equal(failedJobName))
			Expect(listJobs()).To(BeEmpty())

			By("Creating the upgrade Job named by the new reboot Pod once it is ready")
			markRebootPodRunning(ctx, &replacements[0])
			reconcileNodeOp()
			jobs := listJobs()
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(rebootPodJobName(&replacements[0])))
		})

		It("deletes a stopped reboot Pod while the node waits", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			finished := &pods[0]
			finished.Status.Phase = corev1.PodSucceeded
			Expect(k8sClient.Status().Update(ctx, finished)).To(Succeed())

			reconcileNodeOp()

			// The API server removes a stopped reboot Pod right away, without
			// a grace period.
			current := &corev1.Pod{}
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(finished), current)
			if err == nil {
				Expect(current.DeletionTimestamp.IsZero()).To(BeFalse(),
					"a finished reboot Pod that the replacement supersedes must be deleted")
			} else {
				Expect(apierrors.IsNotFound(err)).To(BeTrue(), "unexpected error: %v", err)
			}
		})

		It("uses the oldest of several running reboot Pods and deletes the others", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			oldest := pods[0]

			By("Creating a newer second reboot Pod whose name sorts first")
			Eventually(func() bool {
				return time.Now().After(oldest.CreationTimestamp.Add(time.Second))
			}, 3*time.Second, 100*time.Millisecond).Should(BeTrue())
			duplicate := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            oldest.GenerateName + "00000",
					Namespace:       oldest.Namespace,
					Labels:          oldest.Labels,
					OwnerReferences: oldest.OwnerReferences,
				},
				Spec: *oldest.Spec.DeepCopy(),
			}
			oldestJobName := rebootPodJobName(&oldest)
			duplicate.Spec.Containers[0].Env[0].Value = oldestJobName[:len(oldestJobName)-1] + "0"
			Expect(k8sClient.Create(ctx, duplicate)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, duplicate, client.GracePeriodSeconds(0)))).To(Succeed())
			})

			markRebootPodsRunning(ctx, nodeOp)
			reconcileNodeOp()

			jobs := listJobs()
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(rebootPodJobName(&oldest)))
			current := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(duplicate), current)).To(Succeed())
			Expect(current.DeletionTimestamp.IsZero()).To(BeFalse(), "the younger duplicate must be deleted")
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&oldest), current)).To(Succeed())
			Expect(current.DeletionTimestamp.IsZero()).To(BeTrue())
		})

		It("deletes every reboot Pod of the node when the node's upgrade Job fails", func() {
			reconcileNodeOp()
			markRebootPodsRunning(ctx, nodeOp)
			reconcileNodeOp()
			jobs := listJobs()
			Expect(jobs).To(HaveLen(1))
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))

			By("Adding a second reboot Pod for the same node")
			extra := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            pods[0].GenerateName + "extra",
					Namespace:       pods[0].Namespace,
					Labels:          pods[0].Labels,
					OwnerReferences: pods[0].OwnerReferences,
				},
				Spec: *pods[0].Spec.DeepCopy(),
			}
			Expect(k8sClient.Create(ctx, extra)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, extra, client.GracePeriodSeconds(0)))).To(Succeed())
			})

			Expect(markJobAsFailed(ctx, k8sClient, &jobs[0])).To(Succeed())
			reconcileNodeOp()

			var remaining []corev1.Pod
			for _, pod := range listRebootPods() {
				if pod.Spec.NodeName == pods[0].Spec.NodeName {
					remaining = append(remaining, pod)
				}
			}
			Expect(remaining).To(HaveLen(2))
			for _, pod := range remaining {
				Expect(pod.DeletionTimestamp.IsZero()).To(BeFalse(), "reboot Pod %s must be deleted", pod.Name)
			}
		})

		It("creates no upgrade Job while the reboot Pod is Running but not ready", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			pods[0].Status.Phase = corev1.PodRunning
			pods[0].Status.Conditions = []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			}}
			Expect(k8sClient.Status().Update(ctx, &pods[0])).To(Succeed())

			reconcileNodeOp()

			Expect(listJobs()).To(BeEmpty(), "a Running Pod that is not ready may not run its watcher")
			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[pods[0].Spec.NodeName].Message).To(
				Equal("Waiting for the reboot Pod to be ready (reboot Pod phase: Running)"))
		})

		It("shows why the reboot Pod's container is waiting while it keeps crashing", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			pods[0].Status.Phase = corev1.PodRunning
			pods[0].Status.Conditions = []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			}}
			pods[0].Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  pods[0].Spec.Containers[0].Name,
				Image: pods[0].Spec.Containers[0].Image,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}}
			Expect(k8sClient.Status().Update(ctx, &pods[0])).To(Succeed())

			reconcileNodeOp()

			Expect(listJobs()).To(BeEmpty())
			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[pods[0].Spec.NodeName].Message).To(
				Equal("Waiting for the reboot Pod to be ready (reboot Pod phase: Running, container: CrashLoopBackOff)"))
		})

		It("creates no upgrade Job while the reboot Pod is ready but its container is not running", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			markRebootPodRunning(ctx, &pods[0])
			pods[0].Status.ContainerStatuses[0].State = corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
			}
			Expect(k8sClient.Status().Update(ctx, &pods[0])).To(Succeed())

			reconcileNodeOp()

			Expect(listJobs()).To(BeEmpty())
		})

		It("names the reboot Pod's phase in the waiting message when it is not Pending", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			pods[0].Status.Phase = corev1.PodUnknown
			Expect(k8sClient.Status().Update(ctx, &pods[0])).To(Succeed())

			reconcileNodeOp()

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			Expect(current.Status.NodeStatuses[pods[0].Spec.NodeName].Message).To(
				Equal("Waiting for the reboot Pod to be ready (reboot Pod phase: Unknown)"))
		})

		It("does not reuse the upgrade Job name of a reboot Pod that is being deleted", func() {
			reconcileNodeOp()
			pods := listRebootPods()
			Expect(pods).To(HaveLen(1))
			stale := &pods[0]
			staleJobName := stale.Spec.Containers[0].Env[0].Value

			By("Deleting the reboot Pod and removing the node from the NodeOp status")
			// A finalizer keeps the reboot Pod around with a deletion
			// timestamp, as a Pod in its termination grace period would be.
			stale.Finalizers = append(stale.Finalizers, "kairos.io/test-hold")
			Expect(k8sClient.Update(ctx, stale)).To(Succeed())
			DeferCleanup(func() {
				held := &corev1.Pod{}
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(stale), held); err == nil {
					held.Finalizers = nil
					Expect(k8sClient.Update(ctx, held)).To(Succeed())
				}
			})
			Expect(k8sClient.Delete(ctx, stale)).To(Succeed())

			current := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nodeOp), current)).To(Succeed())
			current.Status.NodeStatuses = nil
			Expect(k8sClient.Status().Update(ctx, current)).To(Succeed())

			reconcileNodeOp()

			var live []corev1.Pod
			for _, pod := range listRebootPods() {
				if pod.DeletionTimestamp.IsZero() {
					live = append(live, pod)
				}
			}
			Expect(live).To(HaveLen(1))
			Expect(live[0].Spec.Containers[0].Env[0].Value).NotTo(Equal(staleJobName))
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
			runRebootPodsAndReconcile(ctx, controllerReconciler, nodeOp)

			By("Verifying one job was created initially")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(
				ctx, jobList,
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
			err = k8sClient.List(
				ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels(map[string]string{
					"kairos.io/nodeop": resourceName,
				}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1), "Should not create additional jobs while reboot is pending")

			By("Simulating the node rebooting after the upgrade")
			jobRebootsItsNode(ctx, job)

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
			runRebootPodsAndReconcile(ctx, controllerReconciler, nodeOp)

			By("Verifying new job can now be created for other nodes")
			err = k8sClient.List(
				ctx, jobList,
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
		Expect(k8sClient.List(
			ctx, podList,
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
		Expect(k8sClient.List(
			ctx, podList,
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
		Expect(k8sClient.List(
			ctx, jl,
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
				HostMountPath: defaultHostMountPath,
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
			Expect(hostMount.MountPath).To(Equal(defaultHostMountPath))
			Expect(hostMount.ReadOnly).To(BeTrue())

			// Verify HOST_DIR env var falls back to default "/host"
			var foundEnv *corev1.EnvVar
			for i := range c.Env {
				if c.Env[i].Name == "HOST_DIR" {
					foundEnv = &c.Env[i]
					break
				}
			}
			Expect(foundEnv).NotTo(BeNil(), "HOST_DIR env var must be set on the preflight container")
			Expect(foundEnv.Value).To(Equal(defaultHostMountPath), "HOST_DIR env var must fall back to the default '/host' when HostMountPath is empty")

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

	It("propagates a user-set HostMountPath to the preflight Pod's mount and HOST_DIR env var", func() {
		By("Creating a NodeOp with a non-default HostMountPath")
		userSetMount := "/mnt/kairos-host"
		deadline := int32(45)
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:         preflightCtxImage,
				Command:       []string{"echo", "test"},
				HostMountPath: userSetMount, // non-empty — triggers getHostMountPath if branch
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command:               []string{"/bin/sh", "-c", "echo preflight-custom"},
					ActiveDeadlineSeconds: &deadline,
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()

		By("Verifying preflight Pods use the custom HostMountPath for both mount and env var")
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		for _, pod := range pods {
			Expect(pod.Spec.Containers).To(HaveLen(1))
			c := pod.Spec.Containers[0]

			// Verify HOST_DIR env var matches the custom path
			var foundEnv *corev1.EnvVar
			for i := range c.Env {
				if c.Env[i].Name == "HOST_DIR" {
					foundEnv = &c.Env[i]
					break
				}
			}
			Expect(foundEnv).NotTo(BeNil(), "HOST_DIR env var must be set on the preflight container")
			Expect(foundEnv.Value).To(Equal(userSetMount), "HOST_DIR env var must match the user-set HostMountPath")

			// Verify the volume mount path matches the custom path
			var foundMount *corev1.VolumeMount
			for i := range c.VolumeMounts {
				if c.VolumeMounts[i].Name == "host-root" {
					foundMount = &c.VolumeMounts[i]
					break
				}
			}
			Expect(foundMount).NotTo(BeNil(), "host-root volume mount must exist on the preflight container")
			Expect(foundMount.MountPath).To(Equal(userSetMount), "container mount path must match the user-set HostMountPath")
			Expect(foundMount.ReadOnly).To(BeTrue())

			// Verify the volume itself still points at "/"
			var foundVol *corev1.Volume
			for i := range pod.Spec.Volumes {
				if pod.Spec.Volumes[i].Name == "host-root" {
					foundVol = &pod.Spec.Volumes[i]
					break
				}
			}
			Expect(foundVol).NotTo(BeNil())
			Expect(foundVol.HostPath.Path).To(Equal("/"))
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

	It("after the preflight Pod says proceed, creates the upgrade Job only once the reboot Pod is ready", func() {
		nodeOp := &kairosiov1alpha1.NodeOp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Image:           preflightCtxImage,
				Command:         []string{"echo", "test"},
				RebootOnSuccess: asBool(true),
				Preflight: &kairosiov1alpha1.PreflightSpec{
					Command: []string{"/bin/sh", "-c", "true"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		reconcileOnce()
		pods := listPreflightPods()
		Expect(pods).To(HaveLen(len(nodeNames)))

		By("Letting the first preflight Pod say proceed while the node's reboot Pod is Pending")
		completePreflight(&pods[0], "")
		reconcileOnce()

		proceedNode := pods[0].Spec.NodeName
		status := getNodeOp().Status.NodeStatuses[proceedNode]
		Expect(status.Phase).To(Equal(phasePending))
		Expect(status.JobName).To(BeEmpty())
		Expect(status.Message).To(Equal("Waiting for the reboot Pod to be ready"))
		Expect(listOwnedJobs()).To(BeEmpty())

		By("Marking the reboot Pod ready")
		markRebootPodsRunning(ctx, nodeOp)
		reconcileOnce()

		jobs := listOwnedJobs()
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Labels[labelKeyNode]).To(Equal(proceedNode))
		Expect(getNodeOp().Status.NodeStatuses[proceedNode].JobName).To(Equal(jobs[0].Name))
	})

	It("keeps the preflight failure reason on later reconciles", func() {
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
		Expect(pods).NotTo(BeEmpty())

		By("Marking the first preflight Pod as PodFailed")
		failPreflight(&pods[0])
		reconcileOnce()

		failedNode := pods[0].Spec.NodeName
		recorded := getNodeOp().Status.NodeStatuses[failedNode]
		Expect(recorded.Phase).To(Equal("Failed"))
		Expect(recorded.JobName).To(BeEmpty())
		Expect(recorded.Message).To(ContainSubstring("Preflight failed"))

		By("Reconciling again, as the 5 minute resync does")
		reconcileOnce()
		reconcileOnce()

		after := getNodeOp().Status.NodeStatuses[failedNode]
		Expect(after.Phase).To(Equal("Failed"))
		Expect(after.Message).To(Equal(recorded.Message),
			"a node that failed preflight has no Job, so nothing may replace its reason with a Job verdict")
		Expect(getNodeOp().Status.Phase).To(Equal("Failed"))
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

var _ = Describe("NodeOp Controller - Resources", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	var (
		ctx          context.Context
		resourceName string
		nodeName     string
		node         *corev1.Node
		r            *NodeOpReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())
		suffix := fmt.Sprintf("-%d", time.Now().UnixNano())
		resourceName = "res-test" + suffix
		nodeName = "res-node" + suffix
		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName,
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		r = &NodeOpReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	AfterEach(func() {
		Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())

		// Delete NodeOp.
		Eventually(func() error {
			nop := &kairosiov1alpha1.NodeOp{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      resourceName,
				Namespace: "default",
			}, nop)
			if err != nil {
				return client.IgnoreNotFound(err)
			}
			return k8sClient.Delete(ctx, nop)
		}, timeout, interval).Should(Succeed())

		// Delete Jobs owned by this test's NodeOp.
		jobList := &batchv1.JobList{}
		Expect(k8sClient.List(ctx, jobList, client.InNamespace("default"))).To(Succeed())
		for i := range jobList.Items {
			j := jobList.Items[i]
			for _, o := range j.OwnerReferences {
				if o.Kind == kindNodeOp && o.Name == resourceName {
					prop := metav1.DeletePropagationBackground
					Expect(k8sClient.Delete(ctx, &j, &client.DeleteOptions{PropagationPolicy: &prop})).To(Succeed())
					break
				}
			}
		}

		// Delete any reboot Pods created for this test's NodeOp.
		podList := &corev1.PodList{}
		Expect(k8sClient.List(
			ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{"kairos.io/nodeop": resourceName},
		)).To(Succeed())
		for i := range podList.Items {
			pod := podList.Items[i]
			grace := int64(0)
			Expect(k8sClient.Delete(ctx, &pod, &client.DeleteOptions{GracePeriodSeconds: &grace})).To(Succeed())
		}

		Expect(k8sClient.Delete(ctx, node)).To(Succeed())
	})

	reconcileOnce := func() {
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"},
		})
		Expect(err).NotTo(HaveOccurred())
	}

	newNodeOp := func(spec kairosiov1alpha1.NodeOpSpec) *kairosiov1alpha1.NodeOp {
		nodeOp := &kairosiov1alpha1.NodeOp{
			TypeMeta:   metav1.TypeMeta{APIVersion: "kairos.io/v1alpha1", Kind: kindNodeOp},
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())
		return nodeOp
	}

	// getRebootPod runs the reconciler until the reboot Pod exists
	getRebootPod := func(spec kairosiov1alpha1.NodeOpSpec) *corev1.Pod {
		nodeOp := newNodeOp(spec)
		reconcileOnce()
		runRebootPodsAndReconcile(ctx, r, nodeOp)

		jobList := &batchv1.JobList{}
		Expect(k8sClient.List(
			ctx, jobList,
			client.InNamespace("default"),
			client.MatchingLabels{labelKeyNodeOp: resourceName},
		)).To(Succeed())
		Expect(jobList.Items).To(HaveLen(1))
		Expect(markJobAsCompleted(ctx, k8sClient, &jobList.Items[0])).To(Succeed())

		reconcileOnce()

		podList := &corev1.PodList{}
		Expect(k8sClient.List(
			ctx, podList,
			client.InNamespace("default"),
			client.MatchingLabels{labelKeyNodeOp: resourceName, labelKeyReboot: "true"},
		)).To(Succeed())
		Expect(podList.Items).To(HaveLen(1))
		return &podList.Items[0]
	}

	resourceRequirements := func() *corev1.ResourceRequirements {
		return &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		}
	}

	When("spec.resources is unset", func() {
		It("leaves the main container unconstrained", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{
						"echo",
						"test",
					},
				},
			}

			stdJob := r.createStandardJobSpec(nodeOp, *node, 6)
			Expect(stdJob.Template.Spec.Containers).To(HaveLen(1))
			Expect(stdJob.Template.Spec.Containers[0].Name).To(Equal("nodeop"))
			Expect(stdJob.Template.Spec.Containers[0].Resources.Requests).To(BeEmpty())
			Expect(stdJob.Template.Spec.Containers[0].Resources.Limits).To(BeEmpty())
		})
	})

	When("spec.preflightResources", func() {
		It("applies the built-in default when unset", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{
						"echo",
						"test",
					},
					Preflight: &kairosiov1alpha1.PreflightSpec{
						Command: []string{"true"},
					},
				},
			}

			preflightPod := r.buildPreflightPod(nodeOp, *node)
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("200m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))
		})

		It("opts out when explicitly empty", func() {
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{
						"echo",
						"test",
					},
					Preflight: &kairosiov1alpha1.PreflightSpec{
						Command: []string{
							"true",
						},
					},
					PreflightResources: &corev1.ResourceRequirements{},
				},
			}

			preflightPod := r.buildPreflightPod(nodeOp, *node)
			Expect(preflightPod.Spec.Containers[0].Resources.Requests).To(BeEmpty())
			Expect(preflightPod.Spec.Containers[0].Resources.Limits).To(BeEmpty())
		})

		It("applies explicit requests and limits", func() {
			preflightReqs := &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("300m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			}
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{
						"echo",
						"test",
					},
					Preflight: &kairosiov1alpha1.PreflightSpec{
						Command: []string{
							"true",
						},
					},
					PreflightResources: preflightReqs,
				},
			}

			preflightPod := r.buildPreflightPod(nodeOp, *node)
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("300m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("256Mi"))
		})
	})

	When("spec.rebootResources", func() {
		It("applies the built-in default when unset", func() {
			rebootPod := getRebootPod(kairosiov1alpha1.NodeOpSpec{
				Command: []string{
					"echo",
					"test",
				},
				RebootOnSuccess: asBool(true),
				Cordon:          asBool(true),
			})
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("200m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))
		})

		It("opts out when explicitly empty", func() {
			rebootPod := getRebootPod(kairosiov1alpha1.NodeOpSpec{
				Command: []string{
					"echo",
					"test",
				},
				RebootOnSuccess: asBool(true),
				Cordon:          asBool(true),
				RebootResources: &corev1.ResourceRequirements{},
			})
			Expect(rebootPod.Spec.Containers[0].Resources.Requests).To(BeEmpty())
			Expect(rebootPod.Spec.Containers[0].Resources.Limits).To(BeEmpty())
		})

		It("applies explicit requests and limits", func() {
			rebootPod := getRebootPod(kairosiov1alpha1.NodeOpSpec{
				Command: []string{
					"echo",
					"test",
				},
				RebootOnSuccess: asBool(true),
				Cordon:          asBool(true),
				RebootResources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("300m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			})
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("300m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("256Mi"))
		})
	})

	When("spec.resources is set", func() {
		It("applies the requirements to the main container only, not to the boot-id-reporter container", func() {
			reqs := resourceRequirements()
			nodeOp := &kairosiov1alpha1.NodeOp{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpSpec{
					Command: []string{
						"echo",
						"test",
					},
					Preflight: &kairosiov1alpha1.PreflightSpec{
						Command: []string{
							"true",
						},
					},
					Resources: reqs,
				},
			}

			stdJob := r.createStandardJobSpec(nodeOp, *node, 6)
			Expect(stdJob.Template.Spec.Containers).To(HaveLen(1))
			Expect(stdJob.Template.Spec.Containers[0].Name).To(Equal("nodeop"))
			Expect(stdJob.Template.Spec.Containers[0].Resources.Requests).To(Equal(reqs.Requests))
			Expect(stdJob.Template.Spec.Containers[0].Resources.Limits).To(Equal(reqs.Limits))

			rebootJob := r.createRebootJobSpec(nodeOp, *node, 6)
			Expect(rebootJob.Template.Spec.InitContainers).To(HaveLen(1))
			Expect(rebootJob.Template.Spec.InitContainers[0].Name).To(Equal("nodeop"))
			Expect(rebootJob.Template.Spec.InitContainers[0].Resources.Requests).To(Equal(reqs.Requests))
			Expect(rebootJob.Template.Spec.InitContainers[0].Resources.Limits).To(Equal(reqs.Limits))
			Expect(rebootJob.Template.Spec.Containers).To(HaveLen(1))
			Expect(rebootJob.Template.Spec.Containers[0].Name).To(Equal(bootid.ReporterContainerName))
			Expect(rebootJob.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("10m"))
			Expect(rebootJob.Template.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("32Mi"))
			Expect(rebootJob.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("10m"))
			Expect(rebootJob.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("32Mi"))

			preflightPod := r.buildPreflightPod(nodeOp, *node)
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("200m"))
			Expect(preflightPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))
		})

		It("keeps the reboot Pod on the built-in default while the Job carries the requirements", func() {
			reqs := resourceRequirements()
			rebootPod := getRebootPod(kairosiov1alpha1.NodeOpSpec{
				Command: []string{
					"echo",
					"test",
				},
				RebootOnSuccess: asBool(true),
				Cordon:          asBool(true),
				Resources:       reqs,
			})

			Expect(rebootPod.Spec.Containers).To(HaveLen(1))
			Expect(rebootPod.Spec.Containers[0].Name).To(Equal("reboot"))
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("200m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("200m"))
			Expect(rebootPod.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))

			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(
				ctx, jobList,
				client.InNamespace("default"),
				client.MatchingLabels{labelKeyNodeOp: resourceName},
			)).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			Expect(jobList.Items[0].Spec.Template.Spec.InitContainers[0].Resources.Requests).To(Equal(reqs.Requests))
			Expect(jobList.Items[0].Spec.Template.Spec.InitContainers[0].Resources.Limits).To(Equal(reqs.Limits))
		})
	})
})

// Boot IDs a node reports before and after the reboot that follows its
// upgrade, in the canonical form the kernel uses.
const (
	bootBefore = "11111111-1111-4111-8111-111111111111"
	bootAfter  = "22222222-2222-4222-8222-222222222222"
)

var _ = Describe("Reboot completion from the boot ID the upgrade Job reports", func() {
	var (
		ctx          context.Context
		nodeName     string
		node         *corev1.Node
		nodeOp       *kairosiov1alpha1.NodeOp
		reconciler   *NodeOpReconciler
		reconcileNow func()
		rebootPodFor func() *corev1.Pod
		upgradeJob   func() *batchv1.Job
		nodeStatus   func() kairosiov1alpha1.NodeStatus
	)

	BeforeEach(func() {
		ctx = context.Background()
		Expect(os.Setenv("CONTROLLER_POD_NAMESPACE", "default")).To(Succeed())

		unique := fmt.Sprintf("bootid-%d", time.Now().UnixNano())
		nodeName = unique + "-node"

		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nodeName,
				Labels: map[string]string{"kubernetes.io/hostname": nodeName},
			},
		}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())
		nodeBootsInto(ctx, nodeName, bootBefore, corev1.ConditionTrue)

		nodeOp = &kairosiov1alpha1.NodeOp{
			TypeMeta: metav1.TypeMeta{APIVersion: "kairos.io/v1alpha1", Kind: kindNodeOp},
			ObjectMeta: metav1.ObjectMeta{
				Name:      unique,
				Namespace: "default",
			},
			Spec: kairosiov1alpha1.NodeOpSpec{
				Command:         []string{"echo", "test"},
				RebootOnSuccess: asBool(true),
				NodeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/hostname": nodeName},
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeOp)).To(Succeed())

		DeferCleanup(func() {
			Expect(os.Unsetenv("CONTROLLER_POD_NAMESPACE")).To(Succeed())
			Eventually(func() error {
				return client.IgnoreNotFound(k8sClient.Delete(ctx, nodeOp))
			}, timeout, interval).Should(Succeed())
			Eventually(func() error {
				return client.IgnoreNotFound(k8sClient.Delete(ctx, node))
			}, timeout, interval).Should(Succeed())
		})

		reconciler = &NodeOpReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

		reconcileNow = func() {
			GinkgoHelper()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: nodeOp.Name, Namespace: nodeOp.Namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		}

		rebootPodFor = func() *corev1.Pod {
			GinkgoHelper()
			podList := &corev1.PodList{}
			Expect(k8sClient.List(ctx, podList,
				client.InNamespace(nodeOp.Namespace),
				client.MatchingLabels(map[string]string{
					labelKeyNodeOp: nodeOp.Name,
					labelKeyReboot: "true",
					labelKeyNode:   nodeName,
				}))).To(Succeed())
			Expect(podList.Items).To(HaveLen(1))
			return &podList.Items[0]
		}

		upgradeJob = func() *batchv1.Job {
			GinkgoHelper()
			jobList := &batchv1.JobList{}
			Expect(k8sClient.List(ctx, jobList,
				client.InNamespace(nodeOp.Namespace),
				client.MatchingLabels(map[string]string{labelKeyNodeOp: nodeOp.Name}))).To(Succeed())
			Expect(jobList.Items).To(HaveLen(1))
			return &jobList.Items[0]
		}

		nodeStatus = func() kairosiov1alpha1.NodeStatus {
			GinkgoHelper()
			updated := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: nodeOp.Name, Namespace: nodeOp.Namespace,
			}, updated)).To(Succeed())
			Expect(updated.Status.NodeStatuses).To(HaveKey(nodeName))
			return updated.Status.NodeStatuses[nodeName]
		}
	})

	// Completes the upgrade Job, with its Pod reporting terminationMessage
	// from the boot-id-reporter container, and reconciles once, which leaves
	// the operation waiting for the reboot.
	upgradeJobDone := func(terminationMessage string) {
		GinkgoHelper()

		reconcileNow()
		runRebootPodsAndReconcile(ctx, reconciler, nodeOp)
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending))

		job := upgradeJob()
		createSucceededJobPod(ctx, job, terminationMessage)
		Expect(markJobAsCompleted(ctx, k8sClient, job)).To(Succeed())

		reconcileNow()
		Expect(nodeStatus().Phase).To(Equal(phaseCompleted))
	}

	It("gives the reboot Pod the upgrade Job's name", func() {
		reconcileNow()
		runRebootPodsAndReconcile(ctx, reconciler, nodeOp)

		container := rebootPodFor().Spec.Containers[0]
		Expect(container.Command).To(Equal(rebootwatcher.Command()),
			"the reboot Pod runs the manager's reboot-watcher subcommand")

		var watchedJob string
		for _, e := range container.Env {
			if e.Name == rebootwatcher.JobNameEnv {
				watchedJob = e.Value
			}
		}
		Expect(watchedJob).To(Equal(upgradeJob().Name),
			"the watcher needs JOB_NAME to know which Job belongs to this operation")
	})

	It("saves the boot ID the upgrade Job's Pod reports in the NodeOp status", func() {
		upgradeJobDone(bootBefore + "\n")

		Expect(nodeStatus().PreRebootBootID).To(Equal(bootBefore))
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending))
	})

	It("stays pending when the upgrade Job's Pod reports an invalid boot ID", func() {
		upgradeJobDone("not-a-boot-id")
		nodeBootsInto(ctx, nodeName, bootAfter, corev1.ConditionTrue)

		reconcileNow()
		Expect(nodeStatus().PreRebootBootID).To(BeEmpty())
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending))
	})

	It("stays pending when no Pod of the upgrade Job reports a boot ID", func() {
		reconcileNow()
		runRebootPodsAndReconcile(ctx, reconciler, nodeOp)
		Expect(markJobAsCompleted(ctx, k8sClient, upgradeJob())).To(Succeed())
		nodeBootsInto(ctx, nodeName, bootAfter, corev1.ConditionTrue)

		reconcileNow()
		Expect(nodeStatus().Phase).To(Equal(phaseCompleted))
		Expect(nodeStatus().PreRebootBootID).To(BeEmpty())
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending),
			"a boot ID change alone must not be read as this operation's reboot")
	})

	It("stays pending while the node still reports the boot ID the upgrade Job reported", func() {
		upgradeJobDone(bootBefore)

		reconcileNow()
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending))
	})

	It("completes once the node is Ready on a new boot, without the reboot Pod succeeding", func() {
		upgradeJobDone(bootBefore)
		nodeBootsInto(ctx, nodeName, bootAfter, corev1.ConditionTrue)

		reconcileNow()
		Expect(rebootPodFor().Status.Phase).NotTo(Equal(corev1.PodSucceeded),
			"the point of this test is that the reboot Pod never succeeded")
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusCompleted))
	})

	It("stays pending while the rebooted node is not Ready yet", func() {
		upgradeJobDone(bootBefore)
		nodeBootsInto(ctx, nodeName, bootAfter, corev1.ConditionFalse)

		reconcileNow()
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending))
	})

	It("stays pending without a reported boot ID even when the reboot Pod succeeded", func() {
		upgradeJobDone("")
		nodeBootsInto(ctx, nodeName, bootAfter, corev1.ConditionTrue)

		rebootPod := rebootPodFor()
		rebootPod.Status.Phase = corev1.PodSucceeded
		Expect(k8sClient.Status().Update(ctx, rebootPod)).To(Succeed())

		reconcileNow()
		Expect(nodeStatus().PreRebootBootID).To(BeEmpty())
		Expect(nodeStatus().RebootStatus).To(Equal(rebootStatusPending),
			"the reboot Pod's phase is not evidence of a reboot")
	})
})

// createSucceededJobPod creates the Pod Kubernetes would run for the upgrade
// Job job. The Pod carries the job-name label and reports a Succeeded phase,
// with terminationMessage as the boot-id-reporter container's termination
// message, as the kubelet would after the container wrote it to
// /dev/termination-log.
func createSucceededJobPod(ctx context.Context, job *batchv1.Job, terminationMessage string) {
	GinkgoHelper()
	image := job.Spec.Template.Spec.Containers[0].Image
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: job.Name + "-",
			Namespace:    job.Namespace,
			Labels:       map[string]string{batchv1.JobNameLabel: job.Name},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  bootid.ReporterContainerName,
				Image: image,
			}},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod))).To(Succeed())
	})

	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  bootid.ReporterContainerName,
		Image: image,
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0,
				Reason:   "Completed",
				Message:  terminationMessage,
			},
		},
	}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

// jobRebootsItsNode simulates a successful upgrade followed by the reboot: the
// upgrade Job's Pod reports bootBefore, and the upgrade Job's node comes back
// Ready with bootAfter.
func jobRebootsItsNode(ctx context.Context, job *batchv1.Job) {
	GinkgoHelper()
	createSucceededJobPod(ctx, job, bootBefore)
	nodeBootsInto(ctx, job.Labels[labelKeyNode], bootAfter, corev1.ConditionTrue)
}

// nodeBootsInto makes nodeName report bootID with the given Ready status, as
// the kubelet does on every boot.
func nodeBootsInto(ctx context.Context, nodeName, bootID string, ready corev1.ConditionStatus) {
	GinkgoHelper()
	node := &corev1.Node{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
	node.Status.NodeInfo.BootID = bootID
	setNodeReady(node, ready)
	Expect(k8sClient.Status().Update(ctx, node)).To(Succeed())
}

// setNodeReady sets the node's Ready condition, replacing any existing one.
func setNodeReady(node *corev1.Node, status corev1.ConditionStatus) {
	conditions := []corev1.NodeCondition{}
	for _, c := range node.Status.Conditions {
		if c.Type != corev1.NodeReady {
			conditions = append(conditions, c)
		}
	}
	node.Status.Conditions = append(conditions, corev1.NodeCondition{
		Type:               corev1.NodeReady,
		Status:             status,
		LastHeartbeatTime:  metav1.Now(),
		LastTransitionTime: metav1.Now(),
	})
}

// expectRebootRBAC asserts that the ServiceAccount, Role and RoleBinding that
// nodeOp's reboot Pods run under exist in the NodeOp's namespace, belong to
// the NodeOp, and only allow getting the upgrade Job and listing its Pods.
func expectRebootRBAC(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) {
	GinkgoHelper()

	current := &kairosiov1alpha1.NodeOp{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeOp.Name, Namespace: nodeOp.Namespace}, current)).To(Succeed())

	name := nodeOp.Name + "-reboot"
	key := types.NamespacedName{Name: name, Namespace: nodeOp.Namespace}
	ownerRef := metav1.OwnerReference{
		APIVersion:         kairosiov1alpha1.GroupVersion.String(),
		Kind:               kindNodeOp,
		Name:               current.Name,
		UID:                current.UID,
		Controller:         ptr(true),
		BlockOwnerDeletion: ptr(true),
	}

	sa := &corev1.ServiceAccount{}
	Expect(k8sClient.Get(ctx, key, sa)).To(Succeed())
	Expect(sa.OwnerReferences).To(ConsistOf(ownerRef))

	role := &rbacv1.Role{}
	Expect(k8sClient.Get(ctx, key, role)).To(Succeed())
	Expect(role.OwnerReferences).To(ConsistOf(ownerRef))
	Expect(role.Rules).To(ConsistOf(
		rbacv1.PolicyRule{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get"}},
		rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list"}},
	))

	binding := &rbacv1.RoleBinding{}
	Expect(k8sClient.Get(ctx, key, binding)).To(Succeed())
	Expect(binding.OwnerReferences).To(ConsistOf(ownerRef))
	Expect(binding.RoleRef).To(Equal(rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName,
		Kind:     "Role",
		Name:     name,
	}))
	Expect(binding.Subjects).To(ConsistOf(rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      name,
		Namespace: nodeOp.Namespace,
	}))
}

// runRebootPodsAndReconcile marks nodeOp's reboot Pods ready and reconciles
// nodeOp, which creates the upgrade Jobs those reboot Pods wait for.
func runRebootPodsAndReconcile(ctx context.Context, r *NodeOpReconciler, nodeOp *kairosiov1alpha1.NodeOp) {
	GinkgoHelper()
	markRebootPodsRunning(ctx, nodeOp)
	_, err := r.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: nodeOp.Name, Namespace: nodeOp.Namespace},
	})
	Expect(err).NotTo(HaveOccurred())
}

// markRebootPodsRunning reports every one of nodeOp's reboot Pods as Running
// and ready, as the kubelet would once their container starts. envtest runs no
// kubelet, so the operator would otherwise wait for these reboot Pods forever.
func markRebootPodsRunning(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) {
	GinkgoHelper()
	podList := &corev1.PodList{}
	Expect(k8sClient.List(ctx, podList,
		client.InNamespace(nodeOp.Namespace),
		client.MatchingLabels{labelKeyNodeOp: nodeOp.Name, labelKeyReboot: "true"},
	)).To(Succeed())
	for i := range podList.Items {
		markRebootPodRunning(ctx, &podList.Items[i])
	}
}

// markRebootPodRunning reports the reboot Pod pod as Running and ready, with
// its container running, as the kubelet would once that container starts.
func markRebootPodRunning(ctx context.Context, pod *corev1.Pod) {
	GinkgoHelper()
	now := metav1.Now()
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:               corev1.PodReady,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: now,
	}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  pod.Spec.Containers[0].Name,
		Image: pod.Spec.Containers[0].Image,
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}},
	}}
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}
