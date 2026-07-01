package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	timeout   = time.Second * 10
	interval  = time.Millisecond * 250
	labelTrue = "true"
)

var _ = Describe("upgradePreflightScript", func() {
	// These tests drive the actual shell script with synthetic
	// /etc/kairos-release files in a temp directory (via env-var overrides),
	// so we're testing real behaviour rather than literal script content.

	type files struct {
		targetKairos, targetOS, hostKairos, hostOS string
	}

	// runScript runs the preflight script with the given files mapped in via
	// env vars (empty path => omit the file). Returns the contents of the
	// termination-log file (or "" if not written).
	runScript := func(f files) string {
		tmpDir := GinkgoT().TempDir()
		writeMaybe := func(name, content string) string {
			if content == "" {
				return filepath.Join(tmpDir, name+"-DOES-NOT-EXIST")
			}
			p := filepath.Join(tmpDir, name)
			Expect(os.WriteFile(p, []byte(content), 0644)).To(Succeed())
			return p
		}

		termLog := filepath.Join(tmpDir, "termination-log")

		cmd := exec.Command("/bin/sh", "-c", upgradePreflightScript())
		cmd.Env = append(os.Environ(),
			"TARGET_KAIROS_RELEASE="+writeMaybe("target-kairos", f.targetKairos),
			"TARGET_OS_RELEASE="+writeMaybe("target-os", f.targetOS),
			"HOST_KAIROS_RELEASE="+writeMaybe("host-kairos", f.hostKairos),
			"HOST_OS_RELEASE="+writeMaybe("host-os", f.hostOS),
			"TERMINATION_LOG="+termLog,
		)
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "script must always exit 0: %s", string(out))

		msg, readErr := os.ReadFile(termLog)
		if os.IsNotExist(readErr) {
			return ""
		}
		Expect(readErr).NotTo(HaveOccurred())
		return string(msg)
	}

	const matchingRelease = `KAIROS_VERSION="v4.1.0"
KAIROS_SOFTWARE_VERSION="v1.34.7+k3s1"
KAIROS_SOFTWARE_VERSION_PREFIX="k3s"
`
	const differentRelease = `KAIROS_VERSION="v4.2.0"
KAIROS_SOFTWARE_VERSION="v1.34.7+k3s1"
KAIROS_SOFTWARE_VERSION_PREFIX="k3s"
`
	const osReleaseWithoutKairos = `NAME="Generic Linux"
VERSION="1.0"
ID=generic
`

	It("skips when both kairos-release files have matching KAIROS_* values", func() {
		msg := runScript(files{
			targetKairos: matchingRelease,
			hostKairos:   matchingRelease,
		})
		Expect(msg).To(ContainSubstring("node is already at"))
		Expect(msg).To(ContainSubstring("v4.1.0"))
	})

	It("proceeds when KAIROS_VERSION differs between target and host", func() {
		msg := runScript(files{
			targetKairos: differentRelease,
			hostKairos:   matchingRelease,
		})
		Expect(msg).To(BeEmpty(), "different versions must NOT trigger a skip")
	})

	It("proceeds when the target side has no readable KAIROS_VERSION (target falls back to os-release without KAIROS_*)", func() {
		// This is the bug Copilot flagged: if both sides fall back to
		// os-release files that lack KAIROS_*, get_version returns "" for
		// both, and a naive equality check would skip the upgrade.
		msg := runScript(files{
			targetOS: osReleaseWithoutKairos,
			hostOS:   osReleaseWithoutKairos,
		})
		Expect(msg).To(BeEmpty(),
			"two unknown versions must NOT compare equal and trigger a wrongful skip")
	})

	It("proceeds when only the target side lacks KAIROS_VERSION", func() {
		msg := runScript(files{
			targetOS:   osReleaseWithoutKairos,
			hostKairos: matchingRelease,
		})
		Expect(msg).To(BeEmpty())
	})

	It("proceeds when only the host side lacks KAIROS_VERSION", func() {
		msg := runScript(files{
			targetKairos: matchingRelease,
			hostOS:       osReleaseWithoutKairos,
		})
		Expect(msg).To(BeEmpty())
	})

	It("proceeds when no files exist at all", func() {
		msg := runScript(files{})
		Expect(msg).To(BeEmpty())
	})

	It("falls back from kairos-release to os-release when kairos-release is missing", func() {
		// os-release on a Kairos host CAN carry KAIROS_* — exercise that path.
		msg := runScript(files{
			targetOS:   matchingRelease,
			hostKairos: matchingRelease,
		})
		Expect(msg).To(ContainSubstring("node is already at"))
	})
})

// reconcileNodeOpUpgrade is a helper that reconciles a NodeOpUpgrade and returns the resulting NodeOp
func reconcileNodeOpUpgrade(ctx context.Context, k8sClient client.Client,
	nodeOpUpgradeName string) (*kairosiov1alpha1.NodeOp, error) {
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
	if err != nil {
		return nil, err
	}

	nodeOp := &kairosiov1alpha1.NodeOp{}
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      nodeOpUpgradeName,
		Namespace: "default",
	}, nodeOp)
	return nodeOp, err
}

var _ = Describe("NodeOpUpgrade Controller", func() {
	Context("When reconciling a NodeOpUpgrade resource", func() {
		var (
			nodeOpUpgradeName string
			nodeOpUpgrade     *kairosiov1alpha1.NodeOpUpgrade
			ctx               context.Context
			createdNodeNames  []string
		)

		BeforeEach(func() {
			ctx = context.Background()
			// Generate a unique name for this test
			nodeOpUpgradeName = fmt.Sprintf("test-nodeopupgrade-%d", time.Now().UnixNano())
			createdNodeNames = []string{}

			// Create a basic NodeOpUpgrade resource
			nodeOpUpgrade = &kairosiov1alpha1.NodeOpUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
					Image:           "quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1",
					UpgradeActive:   asBool(true),
					UpgradeRecovery: asBool(false),
					Force:           asBool(false),
					Concurrency:     1,
					StopOnFailure:   asBool(false),
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

			// Clean up any created corev1.Node resources
			for _, nodeName := range createdNodeNames {
				node := &corev1.Node{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
				if err == nil {
					_ = k8sClient.Delete(ctx, node)
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
			Expect(nodeOp.Spec.NodeSelector).To(Equal(nodeOpUpgrade.Spec.NodeSelector))
			Expect(nodeOp.Spec.HostMountPath).To(Equal("/host"))
			Expect(*nodeOp.Spec.Cordon).To(BeTrue())
			Expect(*nodeOp.Spec.RebootOnSuccess).To(BeTrue())
			Expect(nodeOp.Spec.DrainOptions).NotTo(BeNil())
			Expect(*nodeOp.Spec.DrainOptions.Enabled).To(BeTrue())

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

		It("should pass ImagePullSecrets from NodeOpUpgrade to created NodeOp", func() {
			By("Creating a NodeOpUpgrade with ImagePullSecrets")
			nodeOpUpgradeWithSecrets := &kairosiov1alpha1.NodeOpUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-imagepull", nodeOpUpgradeName),
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
					Image:           "quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1",
					UpgradeActive:   asBool(true),
					UpgradeRecovery: asBool(false),
					Force:           asBool(false),
					Concurrency:     1,
					StopOnFailure:   asBool(false),
					ImagePullSecrets: []corev1.LocalObjectReference{
						{Name: "registry-secret"},
						{Name: "docker-secret"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeOpUpgradeWithSecrets)).To(Succeed())

			// Cleanup this test's NodeOpUpgrade
			DeferCleanup(func() {
				Eventually(func() error {
					return k8sClient.Delete(ctx, nodeOpUpgradeWithSecrets)
				}, timeout, interval).Should(Succeed())
			})

			By("Reconciling the created NodeOpUpgrade")
			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeWithSecrets.Name,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying that a NodeOp was created with correct ImagePullSecrets")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeWithSecrets.Name,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			By("Verifying NodeOp has correct ImagePullSecrets")
			Expect(nodeOp.Spec.ImagePullSecrets).To(HaveLen(2))
			Expect(nodeOp.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "registry-secret"}))
			Expect(nodeOp.Spec.ImagePullSecrets).To(ContainElement(corev1.LocalObjectReference{Name: "docker-secret"}))

			By("Verifying NodeOp has other correct configuration")
			Expect(nodeOp.Spec.Image).To(Equal(nodeOpUpgradeWithSecrets.Spec.Image))
			Expect(nodeOp.Spec.Concurrency).To(Equal(nodeOpUpgradeWithSecrets.Spec.Concurrency))
			Expect(nodeOp.Spec.StopOnFailure).To(Equal(nodeOpUpgradeWithSecrets.Spec.StopOnFailure))
			Expect(nodeOp.Spec.NodeSelector).To(Equal(nodeOpUpgradeWithSecrets.Spec.NodeSelector))
		})

		It("should generate correct upgrade command for active partition only", func() {
			By("Creating the NodeOpUpgrade resource with active upgrade only")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)
			nodeOpUpgrade.Spec.Force = asBool(false)

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
			nodeOpUpgrade.Spec.UpgradeActive = asBool(false)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
			nodeOpUpgrade.Spec.Force = asBool(false)

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
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
			nodeOpUpgrade.Spec.Force = asBool(false)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("# Upgrade recovery partition"))
			Expect(script).To(ContainSubstring("kairos-agent upgrade --recovery --source dir:/"))
			Expect(script).To(ContainSubstring("# Upgrade active partition"))
			Expect(script).To(ContainSubstring("kairos-agent upgrade --source dir:/"))
		})

		It("should generate correct upgrade command with force enabled", func() {
			By("Creating the NodeOpUpgrade resource with force enabled")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)
			nodeOpUpgrade.Spec.Force = asBool(true)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command")
			script := nodeOp.Spec.Command[2]
			Expect(script).NotTo(ContainSubstring("get_version()"))
			Expect(script).To(ContainSubstring("mount --rbind"))
			Expect(script).NotTo(ContainSubstring("--recovery"))
			Expect(script).NotTo(ContainSubstring("export FORCE=true"))
		})

		It("should generate upgrade command with --debug when debug is enabled", func() {
			By("Creating the NodeOpUpgrade resource with debug enabled")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)
			nodeOpUpgrade.Spec.Debug = asBool(true)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command uses the global --debug flag")
			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent --debug upgrade --source dir:/"))
		})

		It("should generate recovery upgrade command with --debug when debug is enabled", func() {
			By("Creating the NodeOpUpgrade resource with debug enabled and recovery only")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(false)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
			nodeOpUpgrade.Spec.Debug = asBool(true)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command uses the global --debug flag")
			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent --debug upgrade --recovery --source dir:/"))
		})

		It("should generate both-partition upgrade command with --debug on each invocation", func() {
			By("Creating the NodeOpUpgrade resource with debug enabled and both partitions")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
			nodeOpUpgrade.Spec.Debug = asBool(true)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command uses the global --debug flag on both invocations")
			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent --debug upgrade --recovery --source dir:/"))
			Expect(script).To(ContainSubstring("kairos-agent --debug upgrade --source dir:/"))
		})

		It("should not add --debug to the upgrade command when debug is unset", func() {
			By("Creating the NodeOpUpgrade resource without debug set")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)

			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling the created NodeOpUpgrade")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the generated command does not include --debug")
			script := nodeOp.Spec.Command[2]
			Expect(script).To(ContainSubstring("kairos-agent upgrade --source dir:/"))
			Expect(script).NotTo(ContainSubstring("--debug"))
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
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)
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
			Expect(*nodeOp.Spec.RebootOnSuccess).To(BeTrue())

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
					UpgradeActive:   asBool(false),
					UpgradeRecovery: asBool(true),
					Force:           asBool(false),
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
			Expect(*nodeOp2.Spec.RebootOnSuccess).To(BeFalse())

			By("Cleaning up second test resources")
			Expect(k8sClient.Delete(ctx, nodeOpUpgrade2)).To(Succeed())
			Expect(k8sClient.Delete(ctx, nodeOp2)).To(Succeed())
		})

		It("should set RebootOnSuccess to true when UpgradeActive is true", func() {
			By("Creating NodeOpUpgrade with UpgradeActive=true")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(false)
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
			Expect(*nodeOp.Spec.RebootOnSuccess).To(BeTrue())
		})

		It("should set RebootOnSuccess to false when UpgradeActive is false", func() {
			By("Creating NodeOpUpgrade with UpgradeActive=false")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(false)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
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
			Expect(*nodeOp.Spec.RebootOnSuccess).To(BeFalse())
		})

		It("should set RebootOnSuccess to true when upgrading both partitions", func() {
			By("Creating NodeOpUpgrade with both UpgradeActive and UpgradeRecovery true")
			nodeOpUpgrade.Spec.UpgradeActive = asBool(true)
			nodeOpUpgrade.Spec.UpgradeRecovery = asBool(true)
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
			Expect(*nodeOp.Spec.RebootOnSuccess).To(BeTrue())
		})

		It("should upgrade master nodes before worker nodes in canary upgrade", func() {
			By("Creating 3 master and 3 worker nodes in random order")
			uniqueSuffix := fmt.Sprintf("-%d", time.Now().UnixNano())
			nodeNames := []string{"master-1", "worker-1", "master-2", "worker-2", "worker-3", "master-3"}
			nodes := make([]*corev1.Node, len(nodeNames))
			for i, name := range nodeNames {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:   name + uniqueSuffix,
						Labels: map[string]string{},
					},
				}
				if strings.HasPrefix(name, "master") {
					node.Labels["node-role.kubernetes.io/master"] = labelTrue
				}
				nodes[i] = node
				Expect(k8sClient.Create(ctx, node)).To(Succeed())
				createdNodeNames = append(createdNodeNames, name+uniqueSuffix)
			}

			By("Targeting 2 masters and 2 workers for upgrade")
			targetNodeNames := []string{"master-1", "master-2", "worker-1", "worker-2"}
			targetLabel := "upgrade-test-" + uniqueSuffix
			for _, n := range nodes {
				for _, t := range targetNodeNames {
					if n.Name == t+uniqueSuffix {
						if n.Labels == nil {
							n.Labels = map[string]string{}
						}
						n.Labels[targetLabel] = labelTrue
						Expect(k8sClient.Update(ctx, n)).To(Succeed())
						break // Exit inner loop once we find a match
					}
				}
			}

			for _, targetNodeName := range targetNodeNames {
				node := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: targetNodeName + uniqueSuffix}, node)).To(Succeed())
				Expect(node.Labels[targetLabel]).To(Equal(labelTrue))
				// Verify master nodes still have master label
				if strings.HasPrefix(targetNodeName, "master") {
					Expect(node.Labels["node-role.kubernetes.io/master"]).To(Equal(labelTrue))
				}
			}

			nodeOpUpgradeName := fmt.Sprintf("test-master-first-upgrade-%d", time.Now().UnixNano())
			nodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
				Spec: kairosiov1alpha1.NodeOpUpgradeSpec{
					Image:           "quay.io/kairos/opensuse:leap-15.6-standard-amd64-generic-v3.4.2-k3sv1.30.11-k3s1",
					UpgradeActive:   asBool(false), // Avoid having to complete reboot pods
					UpgradeRecovery: asBool(true),
					Concurrency:     1,
					Force:           asBool(true), // Bypass preflight; this test focuses on NodeOp ordering
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{targetLabel: labelTrue},
					},
				},
			}
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			controllerReconciler := &NodeOpUpgradeReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling to create the NodeOp")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying NodeOp was created and has correct configuration")
			nodeOp := &kairosiov1alpha1.NodeOp{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeOpUpgradeName,
				Namespace: "default",
			}, nodeOp)).To(Succeed())

			// Verify NodeOp has the correct node selector
			Expect(nodeOp.Spec.NodeSelector).NotTo(BeNil())
			Expect(nodeOp.Spec.NodeSelector.MatchLabels).To(HaveKeyWithValue(targetLabel, labelTrue))
			Expect(nodeOp.Spec.Concurrency).To(Equal(int32(1)))

			By("Reconciling NodeOp to start jobs one-by-one and simulating completions")
			nodeOpReconciler := &NodeOpReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			// Track the order of job creation/completion
			var jobOrder []string
			completedJobs := make([]string, 0, len(targetNodeNames))

			for i := range len(targetNodeNames) {
				By(fmt.Sprintf("Iteration %d: Reconciling NodeOp to create next job", i+1))

				// Reconcile NodeOp to create the next job
				_, err := nodeOpReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      nodeOpUpgradeName,
						Namespace: "default",
					},
				})
				Expect(err).NotTo(HaveOccurred())

				jobList := &batchv1.JobList{}
				err = k8sClient.List(ctx, jobList, client.InNamespace("default"), client.MatchingLabels{
					"kairos.io/nodeop": nodeOpUpgradeName,
				})
				Expect(err).NotTo(HaveOccurred())

				// Find a job that hasn't been completed yet
				var activeJob *batchv1.Job
				for _, job := range jobList.Items {
					jobName := job.Name
					if !contains(completedJobs, jobName) {
						jobCopy := job
						activeJob = &jobCopy
						break
					}
				}

				Expect(activeJob).NotTo(BeNil(), "Should find an active job to complete")

				// Get the node name from the job label and track the order
				if nodeName, exists := activeJob.Labels["kairos.io/node"]; exists {
					jobOrder = append(jobOrder, nodeName)
				}

				// Complete the active job
				Expect(markJobAsCompleted(ctx, k8sClient, activeJob)).To(Succeed())

				// Track this job as completed
				completedJobs = append(completedJobs, activeJob.Name)
				By(fmt.Sprintf("Completed job %s", activeJob.Name))
			}

			// Ensure we processed exactly 4 nodes
			Expect(jobOrder).To(HaveLen(4), "Expected to process exactly 4 nodes")

			By("Verifying that master nodes were upgraded before worker nodes")
			// Fetch fresh node objects from API server to verify labels
			// The first two in jobOrder should be master nodes
			for i := 0; i < 2; i++ {
				name := jobOrder[i]
				node := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
				_, isMaster := node.Labels["node-role.kubernetes.io/master"]
				Expect(isMaster).To(BeTrue(), fmt.Sprintf("Node %s should be a master", name))
			}
			// The last two should be workers
			for i := 2; i < 4; i++ {
				name := jobOrder[i]
				node := &corev1.Node{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, node)).To(Succeed())
				_, isMaster := node.Labels["node-role.kubernetes.io/master"]
				Expect(isMaster).To(BeFalse(), fmt.Sprintf("Node %s should be a worker", name))
			}
		})

		It("populates Spec.Preflight on the created NodeOp when Spec.Force is false", func() {
			By("Creating the NodeOpUpgrade with Force=false (default)")
			nodeOpUpgrade.Spec.Force = asBool(false)
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the NodeOp has Spec.Preflight set")
			Expect(nodeOp.Spec.Preflight).NotTo(BeNil(),
				"NodeOpUpgrade should populate Spec.Preflight on the NodeOp when Force=false")
			Expect(nodeOp.Spec.Preflight.Command).NotTo(BeEmpty())

			By("Verifying the preflight Command is a shell invocation that uses /dev/termination-log to report skip")
			Expect(nodeOp.Spec.Preflight.Command).To(HaveLen(3))
			Expect(nodeOp.Spec.Preflight.Command[0]).To(Equal("/bin/sh"))
			Expect(nodeOp.Spec.Preflight.Command[1]).To(Equal("-c"))
			Expect(nodeOp.Spec.Preflight.Command[2]).To(ContainSubstring("TERMINATION_LOG"),
				"the script must communicate skip via the TERMINATION_LOG path (defaults to /dev/termination-log)")

			By("Verifying Spec.Preflight.Image is empty so it defaults to Spec.Image at the NodeOp level")
			Expect(nodeOp.Spec.Preflight.Image).To(BeEmpty())

			By("Verifying Spec.Preflight.ActiveDeadlineSeconds is set")
			Expect(nodeOp.Spec.Preflight.ActiveDeadlineSeconds).NotTo(BeNil())
			Expect(*nodeOp.Spec.Preflight.ActiveDeadlineSeconds).To(BeNumerically(">", 0))
		})

		It("leaves Spec.Preflight nil on the created NodeOp when Spec.Force is true", func() {
			By("Creating the NodeOpUpgrade with Force=true")
			nodeOpUpgrade.Spec.Force = asBool(true)
			Expect(k8sClient.Create(ctx, nodeOpUpgrade)).To(Succeed())

			By("Reconciling")
			nodeOp, err := reconcileNodeOpUpgrade(ctx, k8sClient, nodeOpUpgradeName)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying Spec.Preflight is nil so the upgrade runs everywhere with no preflight check")
			Expect(nodeOp.Spec.Preflight).To(BeNil())
		})
	})
})

// Helper for string slice contains
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
