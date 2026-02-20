package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const testImageName = "quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0"

var _ = Describe("OSArtifactReconciler", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var namespace string
	var restConfig *rest.Config
	var clientset *kubernetes.Clientset
	var err error

	// boolPtr returns a pointer to the given bool value
	boolPtr := func(v bool) *bool {
		return &v
	}

	// findContainerByName finds a container by name in a pod's containers
	findContainerByName := func(pod *corev1.Pod, name string) *corev1.Container {
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == name {
				return &pod.Spec.Containers[i]
			}
		}
		return nil
	}

	// findInitContainerByName finds an init container by name in a pod
	findInitContainerByName := func(pod *corev1.Pod, name string) *corev1.Container {
		for i := range pod.Spec.InitContainers {
			if pod.Spec.InitContainers[i].Name == name {
				return &pod.Spec.InitContainers[i]
			}
		}
		return nil
	}

	// testContainerCommand tests that a container exists with expected command substrings
	testContainerCommand := func(containerName string, expectedSubstrings []string) {
		pvc, err := r.createPVC(context.TODO(), artifact)
		Expect(err).ToNot(HaveOccurred())

		pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
		Expect(err).ToNot(HaveOccurred())

		container := findContainerByName(pod, containerName)
		Expect(container).ToNot(BeNil())
		Expect(container.Args).To(HaveLen(1))
		for _, substr := range expectedSubstrings {
			Expect(container.Args[0]).To(ContainSubstring(substr))
		}
	}

	// testCloudConfigInclusion tests that cloud-config flag is included in container args
	testCloudConfigInclusion := func(containerName string) {
		secretName := artifact.Name + "-cloudconfig"
		_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: namespace,
				},
				StringData: map[string]string{
					"cloud-config.yaml": "#cloud-config\nusers:\n  - name: test",
				},
				Type: "Opaque",
			}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		if artifact.Spec.Artifacts == nil {
			artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{}
		}
		artifact.Spec.Artifacts.CloudConfigRef = &buildv1alpha2.SecretKeySelector{Name: secretName, Key: "cloud-config.yaml"}
		testContainerCommand(containerName, []string{"--cloud-config /cloud-config.yaml"})
	}

	BeforeEach(func() {
		// These tests require a real cluster (USE_EXISTING_CLUSTER=true)
		// Skip if running in envtest environment
		if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
			Skip("OSArtifact tests require USE_EXISTING_CLUSTER=true - skipping in envtest environment")
		}

		restConfig = ctrl.GetConfigOrDie()
		clientset, err = kubernetes.NewForConfig(restConfig)
		Expect(err).ToNot(HaveOccurred())
		namespace = createRandomNamespace(clientset)

		artifact = &buildv1alpha2.OSArtifact{
			TypeMeta: metav1.TypeMeta{
				Kind:       "OSArtifact",
				APIVersion: buildv1alpha2.GroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      randStringRunes(10),
			},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{Ref: testImageName},
			},
		}

		scheme := runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
		utilruntime.Must(buildv1alpha2.AddToScheme(scheme))

		r = &OSArtifactReconciler{
			ToolImage: fmt.Sprintf("quay.io/kairos/auroraboot:%s", CompatibleAurorabootVersion),
		}

		// Create a direct client (no cache) for tests - we don't need reconciliation
		// This avoids the complexity of managing a running manager
		directClient, err := client.New(restConfig, client.Options{Scheme: scheme})
		Expect(err).ToNot(HaveOccurred())
		r.Client = directClient
		r.Scheme = scheme
	})

	JustBeforeEach(func() {
		k8s := dynamic.NewForConfigOrDie(restConfig)
		artifacts := k8s.Resource(
			schema.GroupVersionResource{
				Group:    buildv1alpha2.GroupVersion.Group,
				Version:  buildv1alpha2.GroupVersion.Version,
				Resource: "osartifacts"}).Namespace(namespace)

		uArtifact := unstructured.Unstructured{}
		uArtifact.Object, _ = runtime.DefaultUnstructuredConverter.ToUnstructured(artifact)
		resp, err := artifacts.Create(context.TODO(), &uArtifact, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())
		// Update the local object with the one fetched from k8s
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(resp.Object, artifact)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		deleteNamespace(clientset, namespace)
	})

	Describe("CreateConfigMap", func() {
		It("creates a ConfigMap with no error", func() {
			ctx := context.Background()
			err := r.CreateConfigMap(ctx, artifact)
			Expect(err).ToNot(HaveOccurred())
			c, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.TODO(), artifact.Name, metav1.GetOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(c).ToNot(BeNil())
		})
	})

	Describe("CreateBuilderPod", func() {
		When("image.ociSpec is set", func() {
			BeforeEach(func() {
				secretName := artifact.Name + "-ocispec"

				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      secretName,
							Namespace: namespace,
						},
						StringData: map[string]string{
							OCISpecSecretKey: "FROM ubuntu",
						},
						Type: "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.Image = buildv1alpha2.ImageSpec{
					OCISpec: &buildv1alpha2.OCISpec{
						Ref: &buildv1alpha2.SecretKeySelector{Name: secretName, Key: OCISpecSecretKey},
					},
				}
			})

			It("creates an Init Container to build the image", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				By("checking if an init container was created")
				initContainerNames := make([]string, 0, len(pod.Spec.InitContainers))
				for _, c := range pod.Spec.InitContainers {
					initContainerNames = append(initContainerNames, c.Name)
				}
				Expect(initContainerNames).To(ContainElement("kaniko-build"))

				By("checking if init containers complete successfully")
				Eventually(func() bool {
					p, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
					Expect(err).ToNot(HaveOccurred())

					var allReady = false
					if len(p.Status.InitContainerStatuses) > 0 {
						allReady = true
					}
					for _, c := range p.Status.InitContainerStatuses {
						allReady = allReady && c.Ready
					}

					return allReady
				}, 2*time.Minute, 5*time.Second).Should(BeTrue())
			})
		})
	})

	Describe("Importers and User Volumes", func() {
		BeforeEach(func() {
			artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
		})

		When("spec.importers is set", func() {
			BeforeEach(func() {
				artifact.Spec.Volumes = []corev1.Volume{
					{
						Name:         "my-overlay",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				}
				artifact.Spec.Importers = []corev1.Container{
					{
						Name:    "fetch-files",
						Image:   "curlimages/curl:latest",
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{"curl -L https://example.com/files.tar.gz | tar xz -C /my-overlay"},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "my-overlay", MountPath: "/my-overlay"},
						},
					},
					{
						Name:    "process-files",
						Image:   "busybox",
						Command: []string{"/bin/sh", "-c"},
						Args:    []string{"echo done"},
					},
				}
			})

			It("prepends importers before build init containers", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				Expect(len(pod.Spec.InitContainers)).To(BeNumerically(">=", 3))
				Expect(pod.Spec.InitContainers[0].Name).To(Equal("fetch-files"))
				Expect(pod.Spec.InitContainers[1].Name).To(Equal("process-files"))
				// The build init container (image unpack) comes after importers
				Expect(pod.Spec.InitContainers[2].Name).To(HavePrefix("pull-image-"))
			})

			It("adds user volumes to the pod spec", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				volumeNames := make([]string, 0, len(pod.Spec.Volumes))
				for _, v := range pod.Spec.Volumes {
					volumeNames = append(volumeNames, v.Name)
				}
				Expect(volumeNames).To(ContainElement("my-overlay"))
				Expect(volumeNames).To(ContainElement("artifacts"))
				Expect(volumeNames).To(ContainElement("rootfs"))
			})
		})

		When("spec.importers is empty", func() {
			It("init containers are unchanged", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				Expect(pod.Spec.InitContainers[0].Name).To(HavePrefix("pull-image-"))
			})
		})

		When("spec.volumes is set without importers", func() {
			BeforeEach(func() {
				artifact.Spec.Volumes = []corev1.Volume{
					{
						Name:         "extra-data",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				}
			})

			It("still adds user volumes to the pod spec", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				volumeNames := make([]string, 0, len(pod.Spec.Volumes))
				for _, v := range pod.Spec.Volumes {
					volumeNames = append(volumeNames, v.Name)
				}
				Expect(volumeNames).To(ContainElement("extra-data"))
			})
		})
	})

	Describe("Volume Bindings", func() {
		BeforeEach(func() {
			artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
		})

		Describe("buildContext binding (image.ociSpec.buildContextVolume)", func() {
			BeforeEach(func() {
				secretName := artifact.Name + "-ocispec"

				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      secretName,
							Namespace: namespace,
						},
						StringData: map[string]string{
							OCISpecSecretKey: "FROM ubuntu",
						},
						Type: "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.Image = buildv1alpha2.ImageSpec{
					OCISpec: &buildv1alpha2.OCISpec{
						Ref:                &buildv1alpha2.SecretKeySelector{Name: secretName, Key: OCISpecSecretKey},
						BuildContextVolume: "my-context",
					},
				}
				artifact.Spec.Volumes = []corev1.Volume{
					{Name: "my-context", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}
			})

			When("buildContextVolume is set", func() {
				BeforeEach(func() {})

				It("mounts the build context volume at /workspace on kaniko", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					kaniko := findInitContainerByName(pod, "kaniko-build")
					Expect(kaniko).ToNot(BeNil())

					var hasContextMount bool
					for _, vm := range kaniko.VolumeMounts {
						if vm.Name == "my-context" && vm.MountPath == "/workspace" {
							hasContextMount = true
						}
					}
					Expect(hasContextMount).To(BeTrue(), "kaniko should have my-context mounted at /workspace")
				})

				It("preserves the ocispec mount at /workspace/ocispec", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					kaniko := findInitContainerByName(pod, "kaniko-build")
					Expect(kaniko).ToNot(BeNil())

					var hasOCISpecMount bool
					for _, vm := range kaniko.VolumeMounts {
						if vm.Name == "ocispec" && vm.MountPath == "/workspace/ocispec" {
							hasOCISpecMount = true
						}
					}
					Expect(hasOCISpecMount).To(BeTrue(), "kaniko should still have ocispec mounted at /workspace/ocispec")
				})
			})

			When("buildContextVolume is not set", func() {
				BeforeEach(func() {
					// Parent BeforeEach already created the secret and set OCISpec with BuildContextVolume.
					// Only override to clear BuildContextVolume and Volumes so kaniko has no extra workspace mount.
					artifact.Spec.Image.OCISpec.BuildContextVolume = ""
					artifact.Spec.Volumes = nil
				})
				It("kaniko does not have an extra workspace mount", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					kaniko := findInitContainerByName(pod, "kaniko-build")
					Expect(kaniko).ToNot(BeNil())

					mountNames := make([]string, 0, len(kaniko.VolumeMounts))
					for _, vm := range kaniko.VolumeMounts {
						mountNames = append(mountNames, vm.Name)
					}
					Expect(mountNames).To(ConsistOf("rootfs", "ocispec"))
				})
			})
		})

		Describe("overlay bindings on build-iso (artifacts.overlayISOVolume / overlayRootfsVolume)", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{ISO: true}
			})

			When("overlayISOVolume is set", func() {
				BeforeEach(func() {
					artifact.Spec.Volumes = []corev1.Volume{
						{Name: "my-iso-overlay", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					}
					artifact.Spec.Artifacts.OverlayISOVolume = "my-iso-overlay"
				})

				It("adds --overlay-iso flag and volume mount to build-iso", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					buildIso := findInitContainerByName(pod, "build-iso")
					Expect(buildIso).ToNot(BeNil())
					Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-iso /overlay-iso"))

					var hasMount bool
					for _, vm := range buildIso.VolumeMounts {
						if vm.Name == "my-iso-overlay" && vm.MountPath == "/overlay-iso" {
							hasMount = true
						}
					}
					Expect(hasMount).To(BeTrue(), "build-iso should have my-iso-overlay mounted at /overlay-iso")
				})
			})

			When("overlayRootfsVolume is set", func() {
				BeforeEach(func() {
					artifact.Spec.Volumes = []corev1.Volume{
						{Name: "my-rootfs-overlay", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					}
					artifact.Spec.Artifacts.OverlayRootfsVolume = "my-rootfs-overlay"
				})

				It("adds --overlay-rootfs flag and volume mount to build-iso", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					buildIso := findInitContainerByName(pod, "build-iso")
					Expect(buildIso).ToNot(BeNil())
					Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-rootfs /overlay-rootfs"))

					var hasMount bool
					for _, vm := range buildIso.VolumeMounts {
						if vm.Name == "my-rootfs-overlay" && vm.MountPath == "/overlay-rootfs" {
							hasMount = true
						}
					}
					Expect(hasMount).To(BeTrue(), "build-iso should have my-rootfs-overlay mounted at /overlay-rootfs")
				})
			})

			When("both overlayISOVolume and overlayRootfsVolume are set", func() {
				BeforeEach(func() {
					artifact.Spec.Volumes = []corev1.Volume{
						{Name: "iso-ov", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						{Name: "rootfs-ov", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					}
					artifact.Spec.Artifacts.OverlayISOVolume = "iso-ov"
					artifact.Spec.Artifacts.OverlayRootfsVolume = "rootfs-ov"
				})

				It("includes both flags in build-iso command", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					buildIso := findInitContainerByName(pod, "build-iso")
					Expect(buildIso).ToNot(BeNil())
					Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-iso /overlay-iso"))
					Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-rootfs /overlay-rootfs"))
				})
			})

			When("no overlay bindings are set", func() {
				It("build-iso command has no overlay flags", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					buildIso := findInitContainerByName(pod, "build-iso")
					Expect(buildIso).ToNot(BeNil())
					Expect(buildIso.Args[0]).ToNot(ContainSubstring("--overlay-iso"))
					Expect(buildIso.Args[0]).ToNot(ContainSubstring("--overlay-rootfs"))
				})
			})
		})
	})

	Describe("Two-stage API (spec.image / spec.artifacts)", func() {
		When("spec.image.ref is set (pre-built image)", func() {
			BeforeEach(func() {
				artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
				artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{
					Arch:           "amd64",
					ISO:            true,
					CloudImage:     true,
					CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cloud-config", Key: "userdata"},
				}
			})

			It("uses image.ref for unpack and does not run kaniko", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				Expect(findInitContainerByName(pod, "kaniko-build")).To(BeNil())
				unpack := findInitContainerByName(pod, "pull-image-baseimage")
				Expect(unpack).ToNot(BeNil())
				Expect(unpack.Args[0]).To(ContainSubstring(testImageName))
			})

			It("includes build-iso and build-cloud-image from artifacts", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				Expect(findInitContainerByName(pod, "build-iso")).ToNot(BeNil())
				Expect(findContainerByName(pod, "build-cloud-image")).ToNot(BeNil())
			})
		})

		When("spec.artifacts.overlayISOVolume and overlayRootfsVolume are set", func() {
			BeforeEach(func() {
				artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
				artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{
					ISO:                 true,
					OverlayISOVolume:    "iso-overlay",
					OverlayRootfsVolume: "rootfs-overlay",
				}
				artifact.Spec.Volumes = []corev1.Volume{
					{Name: "iso-overlay", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					{Name: "rootfs-overlay", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}
			})

			It("adds overlay flags and volume mounts to build-iso", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				buildIso := findInitContainerByName(pod, "build-iso")
				Expect(buildIso).ToNot(BeNil())
				Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-iso /overlay-iso"))
				Expect(buildIso.Args[0]).To(ContainSubstring("--overlay-rootfs /overlay-rootfs"))

				var hasISO, hasRootfs bool
				for _, vm := range buildIso.VolumeMounts {
					if vm.Name == "iso-overlay" && vm.MountPath == "/overlay-iso" {
						hasISO = true
					}
					if vm.Name == "rootfs-overlay" && vm.MountPath == "/overlay-rootfs" {
						hasRootfs = true
					}
				}
				Expect(hasISO).To(BeTrue())
				Expect(hasRootfs).To(BeTrue())
			})
		})

		When("spec.image.ociSpec with buildContextVolume is set", func() {
			BeforeEach(func() {
				secretName := artifact.Name + "-ocispec"
				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
						StringData: map[string]string{OCISpecSecretKey: "FROM ubuntu"},
						Type:       "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.Image = buildv1alpha2.ImageSpec{
					OCISpec: &buildv1alpha2.OCISpec{
						Ref:                &buildv1alpha2.SecretKeySelector{Name: secretName, Key: OCISpecSecretKey},
						BuildContextVolume: "build-ctx",
					},
				}
				artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{ISO: true}
				artifact.Spec.Volumes = []corev1.Volume{
					{Name: "build-ctx", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}
			})

			It("mounts buildContextVolume at /workspace on kaniko", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				kaniko := findInitContainerByName(pod, "kaniko-build")
				Expect(kaniko).ToNot(BeNil())
				var hasCtx bool
				for _, vm := range kaniko.VolumeMounts {
					if vm.Name == "build-ctx" && vm.MountPath == "/workspace" {
						hasCtx = true
						break
					}
				}
				Expect(hasCtx).To(BeTrue(), "kaniko should have build-ctx mounted at /workspace")
			})
		})

		When("spec.image.buildOptions is set (default Dockerfile mode)", func() {
			BeforeEach(func() {
				artifact.Spec.Image = buildv1alpha2.ImageSpec{
					BuildOptions: &buildv1alpha2.BuildOptions{Version: "v3.6.0"},
				}
			})

			It("startBuild succeeds and creates a pod with kaniko build-args", func() {
				result, err := r.startBuild(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())
				Expect(result.Requeue).To(BeFalse())

				var pods corev1.PodList
				Expect(r.List(context.TODO(), &pods, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(labels.Set{artifactLabel: artifact.Name}),
				})).To(Succeed())
				Expect(pods.Items).ToNot(BeEmpty())
				kaniko := findInitContainerByName(&pods.Items[0], "kaniko-build")
				Expect(kaniko).ToNot(BeNil())
				Expect(kaniko.Args).To(ContainElement("--build-arg"))
				Expect(kaniko.Args).To(ContainElement("VERSION=v3.6.0"))
			})
		})
	})

	Describe("Spec Validation in startBuild", func() {
		BeforeEach(func() {
			artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
			artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{ISO: true}
		})

		When("artifacts.overlayISOVolume references a volume not in spec.volumes", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts.OverlayISOVolume = "nonexistent-volume"
			})

			It("returns an error and does not create a PVC", func() {
				result, err := r.startBuild(context.TODO(), artifact)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("overlayISOVolume"))
				Expect(result.RequeueAfter).To(BeZero())

				var pvcs corev1.PersistentVolumeClaimList
				Expect(r.List(context.TODO(), &pvcs, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(labels.Set{
						artifactLabel: artifact.Name,
					}),
				})).To(Succeed())
				Expect(pvcs.Items).To(BeEmpty())
			})
		})

		When("a volume uses a reserved name", func() {
			BeforeEach(func() {
				artifact.Spec.Volumes = []corev1.Volume{
					{
						Name:         "artifacts",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					},
				}
			})

			It("returns an error and does not create a PVC", func() {
				result, err := r.startBuild(context.TODO(), artifact)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("reserved"))
				Expect(result.RequeueAfter).To(BeZero())

				var pvcs corev1.PersistentVolumeClaimList
				Expect(r.List(context.TODO(), &pvcs, &client.ListOptions{
					LabelSelector: labels.SelectorFromSet(labels.Set{
						artifactLabel: artifact.Name,
					}),
				})).To(Succeed())
				Expect(pvcs.Items).To(BeEmpty())
			})
		})

		When("spec is valid", func() {
			BeforeEach(func() {
				artifact.Spec.Volumes = []corev1.Volume{
					{Name: "my-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				}
				artifact.Spec.Artifacts.OverlayISOVolume = "my-vol"
			})

			It("does not return a validation error", func() {
				Expect(artifact.Spec.Validate()).ToNot(HaveOccurred())
			})
		})
	})

	Describe("OCI build templating", func() {
		var ocispecSecretName string
		var renderedSecretName string
		var valuesSecretName string

		BeforeEach(func() {
			ocispecSecretName = artifact.Name + "-ocispec"
			renderedSecretName = artifact.Name + "-rendered-ocispec"
			valuesSecretName = artifact.Name + "-template-values"

			_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ocispecSecretName,
						Namespace: namespace,
					},
					StringData: map[string]string{
						OCISpecSecretKey: "FROM {{ .BaseImage }}\nRUN {{ .InstallCmd }}\n",
					},
					Type: "Opaque",
				}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			artifact.Spec.Image = buildv1alpha2.ImageSpec{
				OCISpec: &buildv1alpha2.OCISpec{
					Ref: &buildv1alpha2.SecretKeySelector{Name: ocispecSecretName, Key: OCISpecSecretKey},
				},
			}
		})

		When("a Secret with template values is referenced", func() {
			BeforeEach(func() {
				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      valuesSecretName,
							Namespace: namespace,
						},
						StringData: map[string]string{
							"BaseImage":  "opensuse/leap:15.6",
							"InstallCmd": "zypper install -y curl",
						},
						Type: "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.Image.OCISpec.TemplateValuesFrom = &buildv1alpha2.SecretKeySelector{Name: valuesSecretName}
			})

			It("renders the OCI build definition with Secret values", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				// The rendered Secret should exist
				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				Expect(rendered).To(Equal("FROM opensuse/leap:15.6\nRUN zypper install -y curl\n"))
			})

			It("updates the rendered Secret when the values Secret changes", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				// Update the values Secret
				valuesSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), valuesSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				valuesSecret.StringData = map[string]string{
					"BaseImage":  "alpine:3.18",
					"InstallCmd": "zypper install -y curl",
				}
				_, err = clientset.CoreV1().Secrets(namespace).Update(
					context.TODO(), valuesSecret, metav1.UpdateOptions{})
				Expect(err).ToNot(HaveOccurred())

				// Reset in-memory mutation to simulate a new reconciliation
				artifact.Spec.Image.OCISpec.Ref.Name = ocispecSecretName
				artifact.Spec.Image.OCISpec.TemplateValuesFrom = &buildv1alpha2.SecretKeySelector{Name: valuesSecretName}

				err = r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				Expect(rendered).To(Equal("FROM alpine:3.18\nRUN zypper install -y curl\n"))
			})
		})

		When("inline template values are provided", func() {
			BeforeEach(func() {
				artifact.Spec.Image.OCISpec.TemplateValues = map[string]string{
					"BaseImage":  "alpine:3.18",
					"InstallCmd": "apk add curl",
				}
			})

			It("renders the OCI build definition with inline values", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				Expect(rendered).To(Equal("FROM alpine:3.18\nRUN apk add curl\n"))
			})
		})

		When("both inline and Secret values are provided", func() {
			BeforeEach(func() {
				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      valuesSecretName,
							Namespace: namespace,
						},
						StringData: map[string]string{
							"BaseImage":  "opensuse/leap:15.6",
							"InstallCmd": "zypper install -y curl",
						},
						Type: "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.Image.OCISpec.TemplateValuesFrom = &buildv1alpha2.SecretKeySelector{Name: valuesSecretName}
				// Inline values override Secret values
				artifact.Spec.Image.OCISpec.TemplateValues = map[string]string{"BaseImage": "alpine:3.18"}
			})

			It("inline values take precedence over Secret values", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				// BaseImage from inline, InstallCmd from Secret
				Expect(rendered).To(Equal("FROM alpine:3.18\nRUN zypper install -y curl\n"))
			})
		})

		When("no template values are provided", func() {
			It("renders template variables as empty strings", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				Expect(rendered).To(Equal("FROM \nRUN \n"))
			})
		})

		When("a Secret with the rendered name already exists", func() {
			When("the Secret is owned by a different OSArtifact", func() {
				JustBeforeEach(func() {
					// Create another OSArtifact in K8s so the garbage collector
					// doesn't orphan-collect the Secret before the test runs.
					otherArtifact := &buildv1alpha2.OSArtifact{
						TypeMeta: metav1.TypeMeta{
							Kind:       "OSArtifact",
							APIVersion: buildv1alpha2.GroupVersion.String(),
						},
						ObjectMeta: metav1.ObjectMeta{
							Namespace: namespace,
							Name:      "other-artifact",
						},
						Spec: buildv1alpha2.OSArtifactSpec{
							Image: buildv1alpha2.ImageSpec{Ref: "quay.io/fake:1"},
						},
					}

					k8s := dynamic.NewForConfigOrDie(restConfig)
					artifacts := k8s.Resource(
						schema.GroupVersionResource{
							Group:    buildv1alpha2.GroupVersion.Group,
							Version:  buildv1alpha2.GroupVersion.Version,
							Resource: "osartifacts"}).Namespace(namespace)
					uOther := unstructured.Unstructured{}
					uOther.Object, _ = runtime.DefaultUnstructuredConverter.ToUnstructured(otherArtifact)
					resp, err := artifacts.Create(context.TODO(), &uOther, metav1.CreateOptions{})
					Expect(err).ToNot(HaveOccurred())
					err = runtime.DefaultUnstructuredConverter.FromUnstructured(resp.Object, otherArtifact)
					Expect(err).ToNot(HaveOccurred())

					// Create an existing Secret owned by the other OSArtifact
					// Note: Use the same name pattern that would be generated
					_, err = clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      renderedSecretName,
								Namespace: namespace,
								OwnerReferences: []metav1.OwnerReference{
									{
										APIVersion: otherArtifact.APIVersion,
										Kind:       otherArtifact.Kind,
										Name:       otherArtifact.Name,
										UID:        otherArtifact.UID,
										Controller: boolPtr(true),
									},
								},
							},
							StringData: map[string]string{
								OCISpecSecretKey: "FROM other-image",
							},
							Type: "Opaque",
						}, metav1.CreateOptions{})
					Expect(err).ToNot(HaveOccurred())
				})

				It("refuses to update the Secret and returns an error", func() {
					err := r.resolveFinalOCISpec(context.TODO(), artifact)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))
					Expect(err.Error()).To(ContainSubstring(string(artifact.UID)))
				})
			})

			When("the Secret is not owned by any OSArtifact", func() {
				BeforeEach(func() {
					// Create an existing Secret without an owner
					_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      renderedSecretName,
								Namespace: namespace,
							},
							StringData: map[string]string{
								OCISpecSecretKey: "FROM unrelated-image",
							},
							Type: "Opaque",
						}, metav1.CreateOptions{})
					Expect(err).ToNot(HaveOccurred())
				})

				It("refuses to update the Secret and returns an error", func() {
					err := r.resolveFinalOCISpec(context.TODO(), artifact)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("already exists and is not owned by this OSArtifact"))
				})
			})

			When("the Secret is owned by this OSArtifact", func() {
				JustBeforeEach(func() {
					// Create a Secret owned by this artifact (must run after JustBeforeEach
					// that creates the artifact in K8s, so artifact.UID is populated)
					_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
						&corev1.Secret{
							ObjectMeta: metav1.ObjectMeta{
								Name:      renderedSecretName,
								Namespace: namespace,
								OwnerReferences: []metav1.OwnerReference{
									{
										APIVersion: artifact.APIVersion,
										Kind:       artifact.Kind,
										Name:       artifact.Name,
										UID:        artifact.UID,
										Controller: boolPtr(true),
									},
								},
							},
							StringData: map[string]string{
								OCISpecSecretKey: "FROM old-image",
							},
							Type: "Opaque",
						}, metav1.CreateOptions{})
					Expect(err).ToNot(HaveOccurred())

					// Setup inline values for this test
					artifact.Spec.Image.OCISpec.TemplateValues = map[string]string{
						"BaseImage":  "alpine:3.18",
						"InstallCmd": "apk add curl",
					}
				})

				It("updates the Secret successfully", func() {
					err := r.resolveFinalOCISpec(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
						context.TODO(), renderedSecretName, metav1.GetOptions{})
					Expect(err).ToNot(HaveOccurred())

					rendered := string(renderedSecret.Data[OCISpecSecretKey])
					Expect(rendered).To(Equal("FROM alpine:3.18\nRUN apk add curl\n"))
				})
			})
		})

		When("the image.ociSpec.ref key is omitted", func() {
			BeforeEach(func() {
				artifact.Spec.Image.OCISpec.Ref.Key = ""
			})

			It("defaults to reading the ociSpec key from the Secret", func() {
				err := r.resolveFinalOCISpec(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data[OCISpecSecretKey])
				Expect(rendered).To(Equal("FROM \nRUN \n"))
			})
		})
	})

	Describe("Auroraboot Commands", func() {
		BeforeEach(func() {
			artifact.Spec.Image = buildv1alpha2.ImageSpec{Ref: testImageName}
			artifact.Spec.Artifacts = &buildv1alpha2.ArtifactSpec{}
		})

		When("CloudImage is enabled", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts.CloudImage = true
			})

			It("creates build-cloud-image container with correct auroraboot command", func() {
				testContainerCommand("build-cloud-image", []string{
					"auroraboot --debug --set 'disk.raw=true'",
					"--set 'state_dir=/artifacts'",
					"dir:/rootfs",
					fmt.Sprintf("file=$(ls /artifacts/*.raw 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.raw",
						artifact.Name),
				})
			})

			When("DiskSize is set", func() {
				BeforeEach(func() {
					artifact.Spec.Artifacts.DiskSize = "32768"
				})

				It("passes disk.size to auroraboot via --set flag instead of EXTEND env var", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					container := findContainerByName(pod, "build-cloud-image")
					Expect(container).ToNot(BeNil())
					Expect(container.Args).To(HaveLen(1))
					Expect(container.Args[0]).To(ContainSubstring("--set 'disk.size=32768'"))

					// Ensure the deprecated EXTEND env var is not used
					for _, env := range container.Env {
						Expect(env.Name).ToNot(Equal("EXTEND"))
					}
				})
			})

			When("DiskSize is not set", func() {
				It("does not include disk.size in auroraboot command", func() {
					pvc, err := r.createPVC(context.TODO(), artifact)
					Expect(err).ToNot(HaveOccurred())

					pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
					Expect(err).ToNot(HaveOccurred())

					container := findContainerByName(pod, "build-cloud-image")
					Expect(container).ToNot(BeNil())
					Expect(container.Args).To(HaveLen(1))
					Expect(container.Args[0]).ToNot(ContainSubstring("disk.size"))
				})
			})

			When("CloudConfigRef is set", func() {
				It("includes cloud-config flag in auroraboot command", func() {
					testCloudConfigInclusion("build-cloud-image")
				})
			})
		})

		When("Netboot is enabled", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts.Netboot = true
				artifact.Spec.Artifacts.ISO = true
				artifact.Spec.Artifacts.NetbootURL = "http://example.com"
			})

			It("creates build-netboot container with correct auroraboot netboot command", func() {
				pvc, err := r.createPVC(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				pod, err := r.createBuilderPod(context.TODO(), artifact, pvc)
				Expect(err).ToNot(HaveOccurred())

				var netbootContainer *corev1.Container
				for i := range pod.Spec.Containers {
					if pod.Spec.Containers[i].Name == "build-netboot" {
						netbootContainer = &pod.Spec.Containers[i]
						break
					}
				}
				Expect(netbootContainer).ToNot(BeNil())
				Expect(netbootContainer.Args).To(HaveLen(1))
				Expect(netbootContainer.Args[0]).To(ContainSubstring("auroraboot --debug netboot"))
				Expect(netbootContainer.Args[0]).To(ContainSubstring(fmt.Sprintf("/artifacts/%s.iso", artifact.Name)))
				Expect(netbootContainer.Args[0]).To(ContainSubstring("/artifacts"))
				Expect(netbootContainer.Args[0]).To(ContainSubstring(artifact.Name))
			})
		})

		When("AzureImage is enabled", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts.AzureImage = true
			})

			It("creates build-azure-cloud-image container with correct auroraboot command", func() {
				testContainerCommand("build-azure-cloud-image", []string{
					"auroraboot --debug --set 'disk.vhd=true'",
					"--set 'state_dir=/artifacts'",
					"dir:/rootfs",
					fmt.Sprintf("file=$(ls /artifacts/*.vhd 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.vhd",
						artifact.Name),
				})
			})

			When("CloudConfigRef is set", func() {
				It("includes cloud-config flag in auroraboot command", func() {
					testCloudConfigInclusion("build-azure-cloud-image")
				})
			})
		})

		When("GCEImage is enabled", func() {
			BeforeEach(func() {
				artifact.Spec.Artifacts.GCEImage = true
			})

			It("creates build-gce-cloud-image container with correct auroraboot command", func() {
				testContainerCommand("build-gce-cloud-image", []string{
					"auroraboot --debug --set 'disk.gce=true'",
					"--set 'state_dir=/artifacts'",
					"dir:/rootfs",
					fmt.Sprintf("file=$(ls /artifacts/*.raw.gce.tar.gz 2>/dev/null | head -n1) && [ -n \"$file\" ] && "+
						"mv \"$file\" /artifacts/%s.gce.tar.gz",
						artifact.Name),
				})
			})

			When("CloudConfigRef is set", func() {
				It("includes cloud-config flag in auroraboot command", func() {
					testCloudConfigInclusion("build-gce-cloud-image")
				})
			})
		})
	})
})
