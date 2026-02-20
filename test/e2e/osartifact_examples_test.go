package e2e

import (
	"context"
	"fmt"

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

func verifyISOExists() string {
	return `set -e
iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
[ -z "$iso_file" ] && echo "ERROR: no .iso file found" && ls -la /artifacts/ && exit 1
[ ! -s "$iso_file" ] && echo "ERROR: ISO empty" && exit 1
echo "PASS: ISO produced"`
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
grep -q %q /tmp/rootfs/%s || (echo "ERROR: %s content mismatch" && exit 1)
echo "PASS: %s"
`, path, path, content, path, path, path)
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
		It("is skipped until BuildOptions-only is implemented", func() {
			Skip("image.buildOptions (default OCI build mode) is not yet implemented")
		})
	})

	Describe("BuildOptions with custom baseImage", func() {
		It("is skipped until BuildOptions-only is implemented", func() {
			Skip("image.buildOptions (default OCI build mode) is not yet implemented")
		})
	})

	Describe("BuildOptions with preKairosInitSteps", func() {
		It("is skipped until BuildOptions-only is implemented", func() {
			Skip("image.buildOptions (default OCI build mode) is not yet implemented")
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
		It("is skipped until BuildOptions-only is implemented", func() {
			Skip("image.buildOptions (default OCI build mode) is not yet implemented")
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
				StringData: map[string]string{ // TODO: remove "FROM" as soon as we implement buildOptions + ociSpec
					"ociSpec": "FROM {{ .BaseImage }}\n{{ .DashboardPart }}\n{{ .UserPart }}\n",
				},
				Type: corev1.SecretTypeOpaque,
			}, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred())

			valuesSecret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "values-", Namespace: "default"},
				StringData: map[string]string{
					"BaseImage":     HadronPreKairosified,
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
      kubernetesVersion: "v1.28.0"
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
