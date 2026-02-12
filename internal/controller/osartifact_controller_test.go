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

var _ = Describe("OSArtifactReconciler", func() {
	var r *OSArtifactReconciler
	var artifact *buildv1alpha2.OSArtifact
	var namespace string
	var restConfig *rest.Config
	var clientset *kubernetes.Clientset
	var err error

	// findContainerByName finds a container by name in a pod
	findContainerByName := func(pod *corev1.Pod, name string) *corev1.Container {
		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == name {
				return &pod.Spec.Containers[i]
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

		artifact.Spec.CloudConfigRef = &buildv1alpha2.SecretKeySelector{
			Name: secretName,
			Key:  "cloud-config.yaml",
		}

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
		When("BaseImageDockerfile is set", func() {
			BeforeEach(func() {
				secretName := artifact.Name + "-dockerfile"

				_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      secretName,
							Namespace: namespace,
						},
						StringData: map[string]string{
							"Dockerfile": "FROM ubuntu",
						},
						Type: "Opaque",
					}, metav1.CreateOptions{})
				Expect(err).ToNot(HaveOccurred())

				artifact.Spec.BaseImageDockerfile = &buildv1alpha2.SecretKeySelector{
					Name: secretName,
					Key:  "Dockerfile",
				}

				// Whatever, just to let it work
				artifact.Spec.ImageName = "quay.io/kairos-ci/" + artifact.Name + ":latest"
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

	Describe("Dockerfile Templating", func() {
		var dockerfileSecretName string
		var renderedSecretName string
		var valuesSecretName string

		BeforeEach(func() {
			dockerfileSecretName = artifact.Name + "-dockerfile"
			renderedSecretName = artifact.Name + "-rendered-dockerfile"
			valuesSecretName = artifact.Name + "-template-values"

			_, err := clientset.CoreV1().Secrets(namespace).Create(context.TODO(),
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      dockerfileSecretName,
						Namespace: namespace,
					},
					StringData: map[string]string{
						"Dockerfile": "FROM {{ .BaseImage }}\nRUN {{ .InstallCmd }}\n",
					},
					Type: "Opaque",
				}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			artifact.Spec.BaseImageDockerfile = &buildv1alpha2.SecretKeySelector{
				Name: dockerfileSecretName,
				Key:  "Dockerfile",
			}
			artifact.Spec.ImageName = "my-registry.example.com/" + artifact.Name + ":latest"
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

				artifact.Spec.DockerfileTemplateValuesFrom = &buildv1alpha2.SecretKeySelector{
					Name: valuesSecretName,
				}
			})

			It("renders the Dockerfile with Secret values", func() {
				err := r.renderDockerfile(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				// The rendered Secret should exist
				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data["Dockerfile"])
				Expect(rendered).To(Equal("FROM opensuse/leap:15.6\nRUN zypper install -y curl\n"))
			})

			It("updates the rendered Secret when the values Secret changes", func() {
				err := r.renderDockerfile(context.TODO(), artifact)
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
				artifact.Spec.BaseImageDockerfile.Name = dockerfileSecretName
				artifact.Spec.DockerfileTemplateValuesFrom = &buildv1alpha2.SecretKeySelector{
					Name: valuesSecretName,
				}

				err = r.renderDockerfile(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data["Dockerfile"])
				Expect(rendered).To(Equal("FROM alpine:3.18\nRUN zypper install -y curl\n"))
			})
		})

		When("inline template values are provided", func() {
			BeforeEach(func() {
				artifact.Spec.DockerfileTemplateValues = map[string]string{
					"BaseImage":  "alpine:3.18",
					"InstallCmd": "apk add curl",
				}
			})

			It("renders the Dockerfile with inline values", func() {
				err := r.renderDockerfile(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data["Dockerfile"])
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

				artifact.Spec.DockerfileTemplateValuesFrom = &buildv1alpha2.SecretKeySelector{
					Name: valuesSecretName,
				}
				// Inline values override Secret values
				artifact.Spec.DockerfileTemplateValues = map[string]string{
					"BaseImage": "alpine:3.18",
				}
			})

			It("inline values take precedence over Secret values", func() {
				err := r.renderDockerfile(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data["Dockerfile"])
				// BaseImage from inline, InstallCmd from Secret
				Expect(rendered).To(Equal("FROM alpine:3.18\nRUN zypper install -y curl\n"))
			})
		})

		When("no template values are provided", func() {
			It("renders template variables as empty strings", func() {
				err := r.renderDockerfile(context.TODO(), artifact)
				Expect(err).ToNot(HaveOccurred())

				renderedSecret, err := clientset.CoreV1().Secrets(namespace).Get(
					context.TODO(), renderedSecretName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				rendered := string(renderedSecret.Data["Dockerfile"])
				Expect(rendered).To(Equal("FROM \nRUN \n"))
			})
		})
	})

	Describe("Auroraboot Commands", func() {
		BeforeEach(func() {
			artifact.Spec.ImageName = "quay.io/kairos/opensuse:leap-15.6-core-amd64-generic-v3.6.0"
		})

		When("CloudImage is enabled", func() {
			BeforeEach(func() {
				artifact.Spec.CloudImage = true
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
					artifact.Spec.DiskSize = "32768"
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
				artifact.Spec.Netboot = true
				artifact.Spec.ISO = true
				artifact.Spec.NetbootURL = "http://example.com"
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
				artifact.Spec.AzureImage = true
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
				artifact.Spec.GCEImage = true
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
