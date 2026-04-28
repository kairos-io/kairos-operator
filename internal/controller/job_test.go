package controller

import (
	"context"
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testBuildahImage = "quay.io/buildah/stable:" + CompatibleBuildahVersion

var _ = Describe("buildahBuildContainer", func() {
	var artifact *buildv1alpha2.OSArtifact

	newArtifactWithOCISpec := func(name string) *buildv1alpha2.OSArtifact {
		return &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					OCISpec: &buildv1alpha2.OCISpec{
						Ref: &buildv1alpha2.SecretKeySelector{Name: "ocispec-secret", Key: OCISpecSecretKey},
					},
				},
			},
		}
	}

	BeforeEach(func() {
		artifact = newArtifactWithOCISpec("my-artifact")
	})

	It("uses the correct container name and image", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		Expect(c.Name).To(Equal("buildah-build"))
		Expect(c.Image).To(Equal(testBuildahImage))
	})

	It("always sets BUILDAH_ISOLATION=chroot and STORAGE_DRIVER=vfs in env", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		envMap := envToMap(c.Env)
		Expect(envMap).To(HaveKeyWithValue("BUILDAH_ISOLATION", "chroot"))
		Expect(envMap).To(HaveKeyWithValue("STORAGE_DRIVER", "vfs"))
	})

	It("sets security context with SETUID+SETGID capabilities and allowPrivilegeEscalation", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		Expect(c.SecurityContext).ToNot(BeNil())
		Expect(c.SecurityContext.Capabilities).ToNot(BeNil())
		Expect(c.SecurityContext.Capabilities.Add).To(ContainElements(
			corev1.Capability("SETUID"),
			corev1.Capability("SETGID"),
		))
		Expect(c.SecurityContext.AllowPrivilegeEscalation).ToNot(BeNil())
		Expect(*c.SecurityContext.AllowPrivilegeEscalation).To(BeTrue())
	})

	It("always mounts ocispec and artifacts volumes", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		mountNames := volumeMountNames(c)
		Expect(mountNames).To(ContainElements("ocispec", "artifacts"))
	})

	It("shell script runs buildah bud then buildah push to docker-archive", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		Expect(c.Command).To(Equal([]string{"/bin/bash", "-cxe"}))
		Expect(c.Args).To(HaveLen(1))
		script := c.Args[0]
		Expect(script).To(ContainSubstring("buildah bud"))
		Expect(script).To(ContainSubstring("-t localhost/my-artifact:latest"))
		Expect(script).To(ContainSubstring("buildah push"))
		Expect(script).To(ContainSubstring("docker-archive:/artifacts/my-artifact.tar"))
		Expect(strings.Index(script, "buildah bud")).To(BeNumerically("<", strings.Index(script, "buildah push")))
	})

	It("references the ocispec file path in bud command", func() {
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		Expect(c.Args[0]).To(ContainSubstring("-f /ocispec/" + OCISpecSecretKey))
	})

	It("does not push to registry when Push is false", func() {
		artifact.Spec.Image.Push = false
		c := buildahBuildContainer(artifact, "", "", testBuildahImage)
		// Only two commands: bud and push to docker-archive; no docker:// transport.
		Expect(c.Args[0]).ToNot(ContainSubstring("docker://"))
	})

	When("arch is set", func() {
		It("adds --arch to buildah bud", func() {
			c := buildahBuildContainer(artifact, "", "amd64", testBuildahImage)
			Expect(c.Args[0]).To(ContainSubstring("--arch amd64"))
		})
	})

	When("BuildOptions is set", func() {
		BeforeEach(func() {
			artifact.Spec.Image.BuildOptions = &buildv1alpha2.BuildOptions{
				Version:          "v3.6.0",
				BaseImage:        "ubuntu:22.04",
				Model:            "generic",
				KubernetesDistro: "k3s",
			}
		})

		It("adds --build-arg flags for each non-empty option", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			script := c.Args[0]
			Expect(script).To(ContainSubstring("--build-arg VERSION=v3.6.0"))
			Expect(script).To(ContainSubstring("--build-arg BASE_IMAGE=ubuntu:22.04"))
			Expect(script).To(ContainSubstring("--build-arg MODEL=generic"))
			Expect(script).To(ContainSubstring("--build-arg KUBERNETES_DISTRO=k3s"))
		})

		It("omits --build-arg for empty options", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			Expect(c.Args[0]).ToNot(ContainSubstring("KUBERNETES_VERSION"))
		})
	})

	When("BuildOptions with FIPS=true", func() {
		It("adds --build-arg FIPS=fips", func() {
			artifact.Spec.Image.BuildOptions = &buildv1alpha2.BuildOptions{
				Version: "v3.6.0", BaseImage: "ubuntu:22.04", FIPS: true,
			}
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			Expect(c.Args[0]).To(ContainSubstring("--build-arg FIPS=fips"))
		})
	})

	When("BuildOptions with TrustedBoot=true", func() {
		It("adds --build-arg TRUSTED_BOOT=true", func() {
			artifact.Spec.Image.BuildOptions = &buildv1alpha2.BuildOptions{
				Version: "v3.6.0", BaseImage: "ubuntu:22.04", TrustedBoot: true,
			}
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			Expect(c.Args[0]).To(ContainSubstring("--build-arg TRUSTED_BOOT=true"))
		})
	})

	When("buildContextVolume is set", func() {
		It("mounts it at /workspace", func() {
			c := buildahBuildContainer(artifact, "my-context", "", testBuildahImage)
			var hasMount bool
			for _, vm := range c.VolumeMounts {
				if vm.Name == "my-context" && vm.MountPath == "/workspace" {
					hasMount = true
				}
			}
			Expect(hasMount).To(BeTrue(), "build context volume should be mounted at /workspace")
		})
	})

	When("buildContextVolume is not set", func() {
		It("has exactly ocispec and artifacts volume mounts (no extra workspace mount)", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			names := volumeMountNames(c)
			Expect(names).To(ConsistOf("ocispec", "artifacts"))
		})
	})

	When("ImageCredentialsSecretRef is set", func() {
		BeforeEach(func() {
			artifact.Spec.Image.ImageCredentialsSecretRef = &buildv1alpha2.SecretKeySelector{Name: "registry-creds"}
		})

		It("mounts image-credentials at /root/.docker", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			var hasMount bool
			for _, vm := range c.VolumeMounts {
				if vm.Name == imageCredentialsVolumeName && vm.MountPath == "/root/.docker" && vm.ReadOnly {
					hasMount = true
				}
			}
			Expect(hasMount).To(BeTrue(), "credentials volume should be mounted read-only at /root/.docker")
		})

		It("passes --authfile to both buildah bud and buildah push", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			script := c.Args[0]
			Expect(strings.Count(script, "--authfile /root/.docker/config.json")).To(Equal(2),
				"--authfile should appear in both bud and push commands")
		})

		It("sets DOCKER_CONFIG env var", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			Expect(envToMap(c.Env)).To(HaveKeyWithValue("DOCKER_CONFIG", "/root/.docker"))
		})
	})

	When("CACertificatesVolume is set", func() {
		BeforeEach(func() {
			artifact.Spec.Image.CACertificatesVolume = "my-ca-certs"
		})

		It("mounts the CA certs volume at /etc/ssl/buildah/certs read-only", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			var hasMount bool
			for _, vm := range c.VolumeMounts {
				if vm.Name == "my-ca-certs" && vm.MountPath == "/etc/ssl/buildah/certs" && vm.ReadOnly {
					hasMount = true
				}
			}
			Expect(hasMount).To(BeTrue(), "CA certs volume should be mounted read-only at /etc/ssl/buildah/certs")
		})

		It("passes --cert-dir to both buildah bud and buildah push", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			script := c.Args[0]
			Expect(strings.Count(script, "--cert-dir /etc/ssl/buildah/certs")).To(Equal(2),
				"--cert-dir should appear in both bud and push commands")
		})
	})

	When("Push is true and BuildImage is set", func() {
		BeforeEach(func() {
			artifact.Spec.Image.Push = true
			artifact.Spec.Image.BuildImage = &buildv1alpha2.BuildImage{
				Registry:   "registry.example.com",
				Repository: "my-ns/my-image",
				Tag:        "v1.0",
			}
		})

		It("appends a third buildah push to the registry destination", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			script := c.Args[0]
			Expect(script).To(ContainSubstring("docker://registry.example.com/my-ns/my-image:v1.0"))
			// Three buildah commands total.
			Expect(strings.Count(script, "buildah push")).To(Equal(2),
				"should push to both docker-archive and the registry")
		})

		It("does not add --tls-verify=false by default", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			Expect(c.Args[0]).ToNot(ContainSubstring("--tls-verify=false"))
		})

		When("PushInsecureRegistry is true", func() {
			BeforeEach(func() {
				artifact.Spec.Image.PushInsecureRegistry = true
			})

			It("adds --tls-verify=false only to the registry push, not the tarball push", func() {
				c := buildahBuildContainer(artifact, "", "", testBuildahImage)
				script := c.Args[0]
				lastAnd := strings.LastIndex(script, " && ")
				registryPushPart := script[lastAnd:]
				tarballPushPart := script[:lastAnd]
				Expect(registryPushPart).To(ContainSubstring("docker://"))
				Expect(tarballPushPart).To(ContainSubstring("docker-archive:"))
				Expect(registryPushPart).To(ContainSubstring("--tls-verify=false"))
				Expect(tarballPushPart).ToNot(ContainSubstring("--tls-verify=false"))
			})
		})

		When("both PullInsecureRegistry and PushInsecureRegistry are true", func() {
			BeforeEach(func() {
				artifact.Spec.Image.PullInsecureRegistry = true
				artifact.Spec.Image.PushInsecureRegistry = true
			})

			It("adds --tls-verify=false to bud and registry push but not tarball push", func() {
				c := buildahBuildContainer(artifact, "", "", testBuildahImage)
				script := c.Args[0]
				budStart := strings.Index(script, "buildah bud")
				tarStart := strings.Index(script, "buildah push")
				regStart := strings.LastIndex(script, "buildah push")
				budPart := script[budStart:tarStart]
				tarballPushPart := script[tarStart:regStart]
				registryPushPart := script[regStart:]
				Expect(tarballPushPart).To(ContainSubstring("docker-archive:"))
				Expect(registryPushPart).To(ContainSubstring("docker://"))
				Expect(budPart).To(ContainSubstring("--tls-verify=false"))
				Expect(tarballPushPart).ToNot(ContainSubstring("--tls-verify=false"))
				Expect(registryPushPart).To(ContainSubstring("--tls-verify=false"))
			})
		})
	})

	When("PullInsecureRegistry is true", func() {
		BeforeEach(func() {
			artifact.Spec.Image.PullInsecureRegistry = true
		})

		It("adds --tls-verify=false to buildah bud", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			script := c.Args[0]
			budPart := script[:strings.Index(script, " && buildah push")]
			Expect(budPart).To(ContainSubstring("--tls-verify=false"))
		})
	})

	When("buildEnv is set", func() {
		BeforeEach(func() {
			artifact.Spec.Image.BuildEnv = []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:3128"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			}
		})

		It("appends buildEnv vars after the Buildah-specific env vars", func() {
			c := buildahBuildContainer(artifact, "", "", testBuildahImage)
			envMap := envToMap(c.Env)
			Expect(envMap).To(HaveKeyWithValue("HTTP_PROXY", "http://proxy.example.com:3128"))
			Expect(envMap).To(HaveKeyWithValue("NO_PROXY", "localhost,127.0.0.1"))
			// Buildah-specific vars still present.
			Expect(envMap).To(HaveKeyWithValue("BUILDAH_ISOLATION", "chroot"))
		})
	})
})

// envToMap converts a slice of EnvVar to a name→value map for convenient assertions.
func envToMap(env []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		m[e.Name] = e.Value
	}
	return m
}

// volumeMountNames returns the names of all volume mounts in a container.
func volumeMountNames(c corev1.Container) []string {
	names := make([]string, 0, len(c.VolumeMounts))
	for _, vm := range c.VolumeMounts {
		names = append(names, vm.Name)
	}
	return names
}

var _ = Describe("buildISOCommand", func() {
	// Tests for --cloud-config flag order
	// See: https://github.com/kairos-io/kairos-operator/pull/73

	When("CloudConfigRef is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cc", Key: "config.yaml"},
					},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("GRUBConfig is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("neither CloudConfigRef nor GRUBConfig is set", func() {
		It("does not include --cloud-config flag", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			}
			cmd := buildISOCommand(artifact, "amd64", "", "")
			Expect(cmd).ToNot(ContainSubstring("--cloud-config"))
			Expect(cmd).To(ContainSubstring("dir:/rootfs"))
		})
	})
})

var _ = Describe("buildUKICommand", func() {
	// Tests for --cloud-config flag order
	// See: https://github.com/kairos-io/kairos-operator/pull/73

	When("CloudConfigRef is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						CloudConfigRef: &buildv1alpha2.SecretKeySelector{Name: "cc", Key: "config.yaml"},
					},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("GRUBConfig is set", func() {
		It("places --cloud-config before dir:/rootfs", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{
						GRUBConfig: "set timeout=10",
					},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(strings.Index(cmd, "--cloud-config")).To(BeNumerically("<", strings.Index(cmd, "dir:/rootfs")))
		})
	})

	When("neither CloudConfigRef nor GRUBConfig is set", func() {
		It("does not include --cloud-config flag", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "test-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{},
				},
			}
			cmd := buildUKICommand(artifact, "iso")
			Expect(cmd).ToNot(ContainSubstring("--cloud-config"))
			Expect(cmd).To(ContainSubstring("dir:/rootfs"))
		})
	})
})

var _ = Describe("newBuilderPod scheduling", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var pvc *corev1.PersistentVolumeClaim

	BeforeEach(func() {
		r = &OSArtifactReconciler{ToolImage: "tool-image"}
		artifact = &buildv1alpha2.OSArtifact{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec: buildv1alpha2.OSArtifactSpec{
				Image:     buildv1alpha2.ImageSpec{Ref: "myimage:latest"},
				Artifacts: &buildv1alpha2.ArtifactSpec{ISO: true},
			},
		}
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pvc"},
		}
	})

	When("nodeSelector is set", func() {
		It("propagates nodeSelector to the pod spec", func() {
			artifact.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": "arm64"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.NodeSelector).To(Equal(map[string]string{"kubernetes.io/arch": "arm64"}))
		})
	})

	When("tolerations are set", func() {
		It("propagates tolerations to the pod spec", func() {
			artifact.Spec.Tolerations = []corev1.Toleration{
				{Key: "arm", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.Tolerations).To(ContainElement(
				corev1.Toleration{Key: "arm", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			))
		})
	})

	When("affinity is set", func() {
		It("propagates affinity to the pod spec", func() {
			artifact.Spec.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "kubernetes.io/arch",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"arm64"},
							}},
						}},
					},
				},
			}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.Affinity).To(Equal(artifact.Spec.Affinity))
		})
	})

	When("no scheduling fields are set", func() {
		It("leaves nodeSelector, tolerations, and affinity unset", func() {
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Spec.NodeSelector).To(BeNil())
			Expect(pod.Spec.Tolerations).To(BeNil())
			Expect(pod.Spec.Affinity).To(BeNil())
		})
	})

	When("pod labels are set", func() {
		It("propagates labels to the pod metadata", func() {
			artifact.Spec.PodLabels = map[string]string{"team": "platform", "env": "prod"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Labels).To(HaveKeyWithValue("team", "platform"))
			Expect(pod.Labels).To(HaveKeyWithValue("env", "prod"))
		})
	})

	When("pod annotations are set", func() {
		It("propagates annotations to the pod metadata", func() {
			artifact.Spec.PodAnnotations = map[string]string{"prometheus.io/scrape": "false"}
			pod := r.newBuilderPod(context.Background(), artifact, pvc)
			Expect(pod.Annotations).To(HaveKeyWithValue("prometheus.io/scrape", "false"))
		})
	})
})

var _ = Describe("volumeForExportArtifacts", func() {
	When("artifacts.volume is set and no PVC exists", func() {
		It("returns volume named artifacts with source from spec.volumes", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{Volume: "my-artifacts"},
					Volumes: []corev1.Volume{
						{Name: "my-artifacts", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			vol, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol.Name).To(Equal("artifacts"))
			Expect(vol.EmptyDir).ToNot(BeNil())
		})
	})

	When("artifacts.volume is empty and a labeled PVC exists", func() {
		It("returns volume named artifacts backed by the PVC (read-only)", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact", Namespace: "default"},
				Spec:       buildv1alpha2.OSArtifactSpec{Artifacts: &buildv1alpha2.ArtifactSpec{}},
			}
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-artifact-artifacts",
					Namespace: "default",
					Labels:    map[string]string{artifactLabel: "my-artifact"},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pvc).Build()
			vol, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).ToNot(HaveOccurred())
			Expect(vol.Name).To(Equal("artifacts"))
			Expect(vol.PersistentVolumeClaim).ToNot(BeNil())
			Expect(vol.PersistentVolumeClaim.ClaimName).To(Equal("my-artifact-artifacts"))
			Expect(vol.PersistentVolumeClaim.ReadOnly).To(BeTrue())
		})
	})

	When("artifacts.volume is set but volume not in spec.volumes", func() {
		It("returns error", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact"},
				Spec: buildv1alpha2.OSArtifactSpec{
					Artifacts: &buildv1alpha2.ArtifactSpec{Volume: "missing-vol"},
					Volumes:   []corev1.Volume{},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			_, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing-vol"))
		})
	})

	When("artifacts is nil and no PVC exists", func() {
		It("returns error", func() {
			artifact := &buildv1alpha2.OSArtifact{
				ObjectMeta: metav1.ObjectMeta{Name: "my-artifact", Namespace: "default"},
				Spec:       buildv1alpha2.OSArtifactSpec{},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
			_, err := volumeForExportArtifacts(context.Background(), cl, artifact)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("artifacts.volume"))
		})
	})
})
