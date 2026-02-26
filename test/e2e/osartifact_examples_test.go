package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// Hadron images used by example e2e tests (fast, small).
const (
	HadronPreKairosified = "quay.io/kairos/hadron:v0.0.1-core-amd64-generic-v3.7.2"
	HadronBase           = "ghcr.io/kairos-io/hadron:v0.0.1"
)

// In-cluster registry URLs and credentials (must match config/registry).
const (
	RegistryHost     = "registry.registry.svc.cluster.local:5000"
	RegistryUser     = "e2epush"
	RegistryPassword = "e2epushpass"
	// No-auth registry for push-without-credentials e2e.
	RegistryNoAuthHost = "registry-noauth.registry.svc.cluster.local:5001"
)

// artifactFromYAML unmarshals an OSArtifact from YAML. Validates that the YAML is parseable.
func artifactFromYAML(y string) *buildv1alpha2.OSArtifact {
	var a buildv1alpha2.OSArtifact
	err := yaml.UnmarshalStrict([]byte(y), &a)
	Expect(err).ToNot(HaveOccurred(), "OSArtifact YAML should be valid")
	a.TypeMeta = metav1.TypeMeta{Kind: "OSArtifact", APIVersion: buildv1alpha2.GroupVersion.String()}
	return &a
}

// exporterWithVerifyScript returns an exporter JobSpec that runs the given verification script.
func exporterWithVerifyScript(verifyScript string) []batchv1.JobSpec {
	return []batchv1.JobSpec{
		{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "verify",
							Image:   "debian:latest",
							Command: []string{"bash"},
							Args:    []string{"-xec", verifyScript},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "artifacts", ReadOnly: true, MountPath: "/artifacts"},
							},
						},
					},
				},
			},
		},
	}
}

// createExampleArtifact creates an OSArtifact (from YAML or in-memory), sets GenerateName and Exporters, creates it, returns name and label selector.
func createExampleArtifact(tc *TestClients, artifact *buildv1alpha2.OSArtifact, namePrefix string, verifyScript string) (string, labels.Selector) {
	artifact.GenerateName = namePrefix
	artifact.Spec.Exporters = exporterWithVerifyScript(verifyScript)
	return tc.CreateArtifact(artifact)
}

// createArtifactNoExporters creates an OSArtifact with GenerateName and no Exporters (for OCI-only build+push tests).
func createArtifactNoExporters(tc *TestClients, artifact *buildv1alpha2.OSArtifact, namePrefix string) (string, labels.Selector) {
	artifact.Name = ""
	artifact.GenerateName = namePrefix
	artifact.Spec.Exporters = nil
	return tc.CreateArtifact(artifact)
}

// runArtifactTestBuildOnly waits for build completion then cleans up (no export phase). Use for OCI-only push tests.
func runArtifactTestBuildOnly(tc *TestClients, artifactName string, artifactLabelSelector labels.Selector) {
	tc.WaitForBuildCompletion(artifactName, artifactLabelSelector)
	tc.Cleanup(artifactName, artifactLabelSelector)
}

func verifyISOExists() string {
	return `set -e
iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
[ -z "$iso_file" ] && echo "ERROR: no .iso file found" && ls -la /artifacts/ && exit 1
[ ! -s "$iso_file" ] && echo "ERROR: ISO empty" && exit 1
echo "PASS: ISO produced"`
}

func verifyPackedImageExists() string {
	return `set -e
tar_file=$(ls /artifacts/*.tar 2>/dev/null | head -n1)
[ -z "$tar_file" ] && echo "ERROR: no .tar file found" && ls -la /artifacts/ && exit 1
[ ! -s "$tar_file" ] && echo "ERROR: tarball empty" && exit 1
echo "PASS: packed image tarball produced"`
}

// verifyFilesInSquashfs builds a script that extracts ISO, finds squashfs, extracts the given paths, and checks each contains expected content.
func verifyFilesInSquashfs(pathToContent map[string]string) string {
	script := `
set -e
apt-get update && apt-get install -y --no-install-recommends p7zip-full squashfs-tools
iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
[ -z "$iso_file" ] && echo "ERROR: no .iso file found" && ls -la /artifacts/ && exit 1
7z x -o/tmp/iso "$iso_file" > /dev/null
squashfs_file=$(find /tmp/iso -name "*.squashfs" -print -quit)
[ -z "$squashfs_file" ] && echo "ERROR: no .squashfs in ISO" && exit 1
rm -rf /tmp/rootfs && mkdir -p /tmp/rootfs
`
	paths := make([]string, 0, len(pathToContent))
	for p := range pathToContent {
		paths = append(paths, p)
	}
	if len(paths) > 0 {
		script += fmt.Sprintf("unsquashfs -d /tmp/rootfs \"$squashfs_file\" %s > /dev/null 2>&1 || true\n", joinQuoted(paths))
	}
	for path, content := range pathToContent {
		script += fmt.Sprintf(`
[ ! -f /tmp/rootfs/%s ] && echo "ERROR: file not found: %s" && exit 1
grep -q %q /tmp/rootfs/%s || (echo "ERROR: %s content mismatch" && echo "--- content of %s ---" && cat /tmp/rootfs/%s && exit 1)
echo "PASS: %s"
`, path, path, content, path, path, path, path, path)
	}
	script += "\necho \"All checks passed\"\n"
	return script
}

func joinQuoted(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// uniqueTestSuffix returns a short random suffix for artifact and image names to avoid collisions when tests run in parallel.
func uniqueTestSuffix() string {
	b := make([]byte, 4)
	if n, err := rand.Read(b); err == nil && n == len(b) {
		return hex.EncodeToString(b)
	}
	// Fall back to a timestamp-based suffix if entropy is unavailable.
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// craneInstallScript returns a shell script that installs crane and sets CRANE. Append a line that uses "$CRANE" (e.g. export or pull).
func craneInstallScript() string {
	return `set -e
apk add --no-cache curl
curl -sL -o /tmp/crane.tgz https://github.com/google/go-containerregistry/releases/download/v0.19.0/go-containerregistry_Linux_x86_64.tar.gz
tar -xzf /tmp/crane.tgz -C /tmp
CRANE=$(find /tmp -maxdepth 2 -name crane -type f 2>/dev/null | head -1)
`
}

// craneVerifyImageScript returns a shell script that installs crane and runs crane export IMAGE - | tar -xOf - path | grep -q content.
// Uses alpine so /bin/sh exists; crane:debug is distroless and has no shell.
func craneVerifyImageScript(imageRef, path, content string) string {
	return craneInstallScript() + fmt.Sprintf(`"$CRANE" export %s - | tar -xOf - %s | grep -q %q
`, imageRef, path, content)
}

// dockerConfigJSON returns a Kubernetes .dockerconfigjson value for the in-cluster registry.
func dockerConfigJSON(registryHost, user, password string) []byte {
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	cfg := map[string]interface{}{
		"auths": map[string]interface{}{
			registryHost: map[string]string{
				"username": user,
				"password": password,
				"auth":     auth,
			},
		},
	}
	out, err := json.Marshal(cfg)
	Expect(err).ToNot(HaveOccurred())
	return out
}

// --- OCI-only push test helpers ---

// createOCISpecSecret creates a Secret holding a Dockerfile (FROM baseImage + RUN echo content > path). Caller must defer Delete.
func createOCISpecSecret(generateName, baseImage, markerPath, markerContent string) *corev1.Secret {
	secret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{GenerateName: generateName, Namespace: "default"},
		StringData: map[string]string{
			"ociSpec": fmt.Sprintf("FROM %s\nRUN echo %q > %s\n", baseImage, markerContent, markerPath),
		},
		Type: corev1.SecretTypeOpaque,
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return secret
}

// createRegistryCredentialsSecret creates a Secret of type DockerConfigJson for the given registry. Caller must defer Delete.
func createRegistryCredentialsSecret(registryHost, user, password string) *corev1.Secret {
	secret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "registry-credentials-", Namespace: "default"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: dockerConfigJSON(registryHost, user, password)},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	return secret
}

// ociPushArtifactWithCredentials returns an OSArtifact (from YAML) that builds from ociSpec, pushes to registry with credentials.
func ociPushArtifactWithCredentials(suffix, ociSpecSecretName, registryHost, repo, credsSecretName string) *buildv1alpha2.OSArtifact {
	y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: oci-push-%s
  namespace: default
spec:
  image:
    ociSpec:
      ref:
        name: %s
        key: ociSpec
    buildImage:
      registry: %s
      repository: %s
      tag: latest
    push: true
    pushCredentialsSecretRef:
      name: %s
`, suffix, ociSpecSecretName, registryHost, repo, credsSecretName)
	return artifactFromYAML(y)
}

// ociPushArtifactNoCredentials returns an OSArtifact (from YAML) that builds from ociSpec, pushes to registry without credentials.
func ociPushArtifactNoCredentials(suffix, ociSpecSecretName, registryHost, repo string) *buildv1alpha2.OSArtifact {
	y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: oci-push-noauth-%s
  namespace: default
spec:
  image:
    ociSpec:
      ref:
        name: %s
        key: ociSpec
    buildImage:
      registry: %s
      repository: %s
      tag: latest
    push: true
`, suffix, ociSpecSecretName, registryHost, repo)
	return artifactFromYAML(y)
}

// runBuildAndPushOnly creates the artifact and runs build-only (no export phase).
func runBuildAndPushOnly(tc *TestClients, artifact *buildv1alpha2.OSArtifact, namePrefix string) {
	artifactName, artifactLabelSelector := createArtifactNoExporters(tc, artifact, namePrefix)
	runArtifactTestBuildOnly(tc, artifactName, artifactLabelSelector)
}

// verifyPullWithCredentials runs a job that pulls imageRef using credentialsSecretName and checks filePathInImage contains expectedContent.
func verifyPullWithCredentials(imageRef, credentialsSecretName, filePathInImage, expectedContent string) {
	job, err := clientset.BatchV1().Jobs("default").Create(context.TODO(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "verify-pull-", Namespace: "default"},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "verify",
						Image:   "alpine:3.19",
						Env:     []corev1.EnvVar{{Name: "DOCKER_CONFIG", Value: "/root/.docker"}},
						Command: []string{"/bin/sh", "-ec"},
						Args:    []string{craneVerifyImageScript(imageRef, filePathInImage, expectedContent)},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "docker-config",
							MountPath: "/root/.docker",
							ReadOnly:  true,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "docker-config",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: credentialsSecretName,
								Items:      []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
							},
						},
					}},
				},
			},
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	jobName := job.Name
	defer func() {
		if CurrentSpecReport().Failed() {
			dumpJobLogs("default", jobName)
		}
		_ = clientset.BatchV1().Jobs("default").Delete(context.TODO(), job.Name, metav1.DeleteOptions{})
	}()
	Eventually(func(g Gomega) {
		j, e := clientset.BatchV1().Jobs("default").Get(context.TODO(), job.Name, metav1.GetOptions{})
		g.Expect(e).ToNot(HaveOccurred())
		g.Expect(j.Status.Succeeded).To(BeNumerically(">=", 1), "pull-with-auth verification job should succeed")
	}).WithTimeout(5 * time.Minute).Should(Succeed())
}

// verifyPullFailsWithoutCredentials runs a job that tries to pull imageRef with no credentials; the job must fail.
func verifyPullFailsWithoutCredentials(imageRef string) {
	script := craneInstallScript() + fmt.Sprintf(`"$CRANE" export %s - > /dev/null
`, imageRef)
	job, err := clientset.BatchV1().Jobs("default").Create(context.TODO(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "verify-noauth-", Namespace: "default"},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "pull",
						Image:   "alpine:3.19",
						Command: []string{"/bin/sh", "-ec"},
						Args:    []string{script},
					}},
				},
			},
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	jobName := job.Name
	defer func() {
		if CurrentSpecReport().Failed() {
			dumpJobLogs("default", jobName)
		}
		_ = clientset.BatchV1().Jobs("default").Delete(context.TODO(), job.Name, metav1.DeleteOptions{})
	}()
	Eventually(func(g Gomega) {
		j, e := clientset.BatchV1().Jobs("default").Get(context.TODO(), job.Name, metav1.GetOptions{})
		g.Expect(e).ToNot(HaveOccurred())
		g.Expect(j.Status.Failed).To(BeNumerically(">=", 1), "pull-without-auth should fail (registry requires credentials)")
	}).WithTimeout(2 * time.Minute).Should(Succeed())
}

// verifyPullSucceedsWithoutCredentials runs a job that pulls imageRef with no credentials and checks filePathInImage contains expectedContent.
func verifyPullSucceedsWithoutCredentials(imageRef, filePathInImage, expectedContent string) {
	job, err := clientset.BatchV1().Jobs("default").Create(context.TODO(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "verify-pull-noauth-", Namespace: "default"},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:    "verify",
						Image:   "alpine:3.19",
						Command: []string{"/bin/sh", "-ec"},
						Args:    []string{craneVerifyImageScript(imageRef, filePathInImage, expectedContent)},
					}},
				},
			},
		},
	}, metav1.CreateOptions{})
	Expect(err).ToNot(HaveOccurred())
	jobName := job.Name
	defer func() {
		if CurrentSpecReport().Failed() {
			dumpJobLogs("default", jobName)
		}
		_ = clientset.BatchV1().Jobs("default").Delete(context.TODO(), job.Name, metav1.DeleteOptions{})
	}()
	Eventually(func(g Gomega) {
		j, e := clientset.BatchV1().Jobs("default").Get(context.TODO(), job.Name, metav1.GetOptions{})
		g.Expect(e).ToNot(HaveOccurred())
		g.Expect(j.Status.Succeeded).To(BeNumerically(">=", 1), "pull-without-auth from no-auth registry should succeed")
	}).WithTimeout(5 * time.Minute).Should(Succeed())
}

var _ = Describe("OSArtifact examples", func() {
	var tc *TestClients

	BeforeEach(func() {
		tc = SetupTestClients()
	})

	Describe("Prebuilt image", func() {
		It("uses a pre-built Hadron image and produces an ISO", func() {
			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: prebuilt-hadron
  namespace: default
spec:
  image:
    ref: %s
  artifacts:
    arch: amd64
    iso: true
`, HadronPreKairosified)
			artifact := artifactFromYAML(y)
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "prebuilt-", verifyISOExists())
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("BuildOptions only (default Dockerfile)", func() {
		It("kairosifies the base image (default OCI spec) and produces an ISO", func() {
			// buildOptions means kairosify: baseImage is a non-kairosified base (e.g. HadronBase).
			// No ociSpec; operator uses default Dockerfile (FROM baseImage + kairos-init).
			// Include k3s so we verify the full kairosify path works.
			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: buildoptions-only
  namespace: default
spec:
  image:
    buildOptions:
      baseImage: %s
      version: v3.6.0
      model: generic
      kubernetesDistro: k3s
    buildImage:
      registry: my-registry.example.com
      repository: e2e/buildoptions-only
      tag: v3.6.0
  artifacts:
    arch: amd64
    iso: true
`, HadronBase)
			artifact := artifactFromYAML(y)
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "buildoptions-", verifyISOExists())
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})

		It("builds with fips: true and produces an ISO with FIPS enabled in the image", func() {
			// Full build with buildOptions.fips: true. Verifies the Dockerfile built successfully
			// and that kairos-init received --fips (not as a quoted string): the image must contain
			// /etc/kairos-release with KAIROS_FIPS=true (written by kairos-init when --fips is used).
			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: buildoptions-fips
  namespace: default
spec:
  image:
    buildOptions:
      baseImage: %s
      version: v3.6.0
      model: generic
      fips: true
    buildImage:
      registry: my-registry.example.com
      repository: e2e/buildoptions-fips
      tag: v3.6.0
  artifacts:
    arch: amd64
    iso: true
`, HadronBase)
			artifact := artifactFromYAML(y)
			// Verify ISO exists and rootfs contains KAIROS_FIPS="true" (proves --fips was applied).
			verifyScript := verifyFilesInSquashfs(map[string]string{"etc/kairos-release": `KAIROS_FIPS="true"`})
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "buildoptions-fips-", verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("OCISpec only (custom Dockerfile)", func() {
		It("builds from a custom Dockerfile and respects buildImage", func() {
			secret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "ocispec-", Namespace: "default"},
				StringData: map[string]string{
					"ociSpec": fmt.Sprintf("FROM %s\nRUN echo 'oci-only-marker' > /etc/e2e-oci-only.txt\n", HadronPreKairosified),
				},
				Type: corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: oci-only
  namespace: default
spec:
  image:
    ociSpec:
      ref:
        name: %s
        key: ociSpec
    buildImage:
      registry: my-registry.example.com
      repository: e2e/oci-only-built
      tag: latest
  artifacts:
    arch: amd64
    iso: true
`, secret.Name)
			artifact := artifactFromYAML(y)
			verifyScript := verifyFilesInSquashfs(map[string]string{"etc/e2e-oci-only.txt": "oci-only-marker"})
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "oci-only-", verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("OCI-only build (no Stage 2)", func() {
		It("builds OCI image only (no ISO/artifacts) and produces packed tarball in /artifacts", func() {
			// No spec.artifacts: OCI-only build; Kaniko always writes the image tarball to /artifacts/<name>.tar for exporters.
			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: oci-only
  namespace: default
spec:
  image:
    buildOptions:
      baseImage: %s
      version: v3.6.0
      model: generic
      kubernetesDistro: k3s
    buildImage:
      registry: my-registry.example.com
      repository: e2e/oci-only-built
      tag: latest
`, HadronBase)
			artifact := artifactFromYAML(y)
			// Exporter runs verifyPackedImageExists(): ensures /artifacts/*.tar exists and is non-empty; test fails otherwise.
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "oci-only-", verifyPackedImageExists())
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})

		It("builds from custom Dockerfile, pushes to registry with credentials, and pull without credentials fails", func() {
			suffix := uniqueTestSuffix()

			ociSpecSecret := createOCISpecSecret("oci-push-ocispec-", HadronPreKairosified, "/etc/e2e-push-marker.txt", "e2e-push-marker")
			defer func() {
				_ = clientset.CoreV1().Secrets("default").Delete(context.TODO(), ociSpecSecret.Name, metav1.DeleteOptions{})
			}()

			registryCredsSecret := createRegistryCredentialsSecret(RegistryHost, RegistryUser, RegistryPassword)
			defer func() {
				_ = clientset.CoreV1().Secrets("default").Delete(context.TODO(), registryCredsSecret.Name, metav1.DeleteOptions{})
			}()

			repo := "e2e/oci-push-" + suffix
			artifact := ociPushArtifactWithCredentials(suffix, ociSpecSecret.Name, RegistryHost, repo, registryCredsSecret.Name)
			runBuildAndPushOnly(tc, artifact, "oci-push-"+suffix+"-")

			imageRef := RegistryHost + "/" + repo + ":latest"
			verifyPullWithCredentials(imageRef, registryCredsSecret.Name, "etc/e2e-push-marker.txt", "e2e-push-marker")
			verifyPullFailsWithoutCredentials(imageRef)
		})

		It("builds from custom Dockerfile, pushes to no-auth registry without credentials, and pull without auth succeeds", func() {
			suffix := uniqueTestSuffix()

			ociSpecSecret := createOCISpecSecret("oci-push-noauth-ocispec-", HadronPreKairosified, "/etc/e2e-push-noauth-marker.txt", "e2e-push-noauth-marker")
			defer func() {
				_ = clientset.CoreV1().Secrets("default").Delete(context.TODO(), ociSpecSecret.Name, metav1.DeleteOptions{})
			}()

			repo := "e2e/oci-push-noauth-" + suffix
			artifact := ociPushArtifactNoCredentials(suffix, ociSpecSecret.Name, RegistryNoAuthHost, repo)
			runBuildAndPushOnly(tc, artifact, "oci-push-noauth-"+suffix+"-")

			imageRef := RegistryNoAuthHost + "/" + repo + ":latest"
			verifyPullSucceedsWithoutCredentials(imageRef, "etc/e2e-push-noauth-marker.txt", "e2e-push-noauth-marker")
		})
	})

	Describe("Importers and overlay bindings", func() {
		It("uses prebuilt Hadron and importers to populate ISO and rootfs overlays", func() {
			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: importers-overlay
  namespace: default
spec:
  image:
    ref: %s
  volumes:
    - name: iso-overlay
      emptyDir: {}
    - name: rootfs-overlay
      emptyDir: {}
  importers:
    - name: populate-iso-overlay
      image: busybox:latest
      command: ["/bin/sh", "-c"]
      args:
        - echo "On ISO root (live)." > /overlay/README-ISO.txt
      volumeMounts:
        - name: iso-overlay
          mountPath: /overlay
    - name: populate-rootfs-overlay
      image: busybox:latest
      command: ["/bin/sh", "-c"]
      args:
        - mkdir -p /overlay/etc && echo "Baked into OS rootfs." > /overlay/etc/README-ROOTFS.txt
      volumeMounts:
        - name: rootfs-overlay
          mountPath: /overlay
  artifacts:
    arch: amd64
    iso: true
    overlayISOVolume: iso-overlay
    overlayRootfsVolume: rootfs-overlay
`, HadronPreKairosified)
			artifact := artifactFromYAML(y)
			verifyScript := `
set -e
apt-get update && apt-get install -y --no-install-recommends p7zip-full squashfs-tools
iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
[ -z "$iso_file" ] && echo "ERROR: no .iso" && exit 1
7z x -o/tmp/iso "$iso_file" > /dev/null
if ! [ -f /tmp/iso/README-ISO.txt ]; then echo "ERROR: overlay-iso not in ISO root"; exit 1; fi
grep -q "On ISO root" /tmp/iso/README-ISO.txt || exit 1
squashfs_file=$(find /tmp/iso -name "*.squashfs" -print -quit)
[ -z "$squashfs_file" ] && echo "ERROR: no squashfs" && exit 1
unsquashfs -d /tmp/rootfs "$squashfs_file" etc/README-ROOTFS.txt > /dev/null 2>&1 || true
[ ! -f /tmp/rootfs/etc/README-ROOTFS.txt ] && echo "ERROR: overlay-rootfs not in squashfs" && exit 1
grep -q "Baked into OS rootfs" /tmp/rootfs/etc/README-ROOTFS.txt || exit 1
echo "PASS: overlay bindings respected"
`
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "importers-overlay-", verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("OCISpec with build context volume", func() {
		It("builds with Dockerfile COPY from build context; importer populates context", func() {
			secret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "ocispec-ctx-", Namespace: "default"},
				StringData: map[string]string{
					"ociSpec": fmt.Sprintf("FROM %s\nCOPY build-info.txt /etc/kairos-build-info.txt\n", HadronPreKairosified),
				},
				Type: corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: build-context
  namespace: default
spec:
  image:
    ociSpec:
      ref:
        name: %s
        key: ociSpec
      buildContextVolume: build-context
  volumes:
    - name: build-context
      emptyDir: {}
  importers:
    - name: populate-build-context
      image: busybox:latest
      command: ["/bin/sh", "-c"]
      args:
        - echo "Built with importers at e2e" > /ctx/build-info.txt
      volumeMounts:
        - name: build-context
          mountPath: /ctx
  artifacts:
    arch: amd64
    iso: true
`, secret.Name)
			artifact := artifactFromYAML(y)
			verifyScript := verifyFilesInSquashfs(map[string]string{"etc/kairos-build-info.txt": "Built with importers at e2e"})
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "build-ctx-", verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("OCISpec + BuildOptions with template and importer", func() {
		It("combines templated Dockerfile (dashboard + user parts), importer, and buildImage", func() {
			templateSecret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "template-", Namespace: "default"},
				StringData: map[string]string{
					"ociSpec": "{{ .DashboardPart }}\n{{ .UserPart }}\n",
				},
				Type: corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			valuesSecret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "values-", Namespace: "default"},
				StringData: map[string]string{
					"DashboardPart": "RUN echo 'dashboard-part' > /etc/dashboard-done.txt\nCOPY dashboard-bundle.txt /etc/dashboard-bundle.txt\n",
					"UserPart":      "RUN echo 'user-part' > /etc/user-done.txt\n",
				},
				Type: corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			y := fmt.Sprintf(`
apiVersion: build.kairos.io/v1alpha2
kind: OSArtifact
metadata:
  name: template-importer
  namespace: default
spec:
  image:
    ociSpec:
      ref:
        name: %s
        key: ociSpec
      templateValuesFrom:
        name: %s
      buildContextVolume: build-context
    buildOptions:
      baseImage: %s
      version: v3.6.0
      model: generic
      kubernetesDistro: k3s
      kubernetesVersion: "v1.35.1+k3s1"
    buildImage:
      registry: my-registry.example.com
      repository: e2e/dashboard-user-kairos
      tag: v3.6.0
  volumes:
    - name: build-context
      emptyDir: {}
  importers:
    - name: populate-build-context
      image: busybox:latest
      command: ["/bin/sh", "-c"]
      args:
        - echo "Dashboard-added packages and files" > /ctx/dashboard-bundle.txt
      volumeMounts:
        - name: build-context
          mountPath: /ctx
  artifacts:
    arch: amd64
    iso: true
`, templateSecret.Name, valuesSecret.Name, HadronPreKairosified)
			artifact := artifactFromYAML(y)
			verifyScript := verifyFilesInSquashfs(map[string]string{
				"etc/dashboard-done.txt":   "dashboard-part",
				"etc/user-done.txt":        "user-part",
				"etc/dashboard-bundle.txt": "Dashboard-added packages and files",
			})
			artifactName, artifactLabelSelector := createExampleArtifact(tc, artifact, "template-", verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})
})
