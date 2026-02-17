package e2e

import (
	"context"
	"fmt"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// verifyImportersInISO returns a shell script that extracts the ISO, then:
//   - checks the ISO filesystem root for the overlay-iso marker (isoMarker)
//   - extracts the squashfs and checks for the overlay-rootfs marker (rootfsMarker)
//   - checks the squashfs for a build-context marker that was COPY'd via the
//     Dockerfile during the kaniko build (buildCtxMarker)
func verifyImportersInISO(isoMarker, rootfsMarker, buildCtxMarker string) string {
	return fmt.Sprintf(`
set -e

apt-get update && apt-get install -y --no-install-recommends p7zip-full squashfs-tools

iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
[ -z "$iso_file" ] && echo "ERROR: no .iso file found" && ls -la /artifacts/ && exit 1
[ ! -s "$iso_file" ] && echo "ERROR: ISO is empty" && exit 1
echo "Found ISO: $iso_file"

# --- Verify overlay-iso marker inside the ISO filesystem ---
7z x -o/tmp/iso "$iso_file" > /dev/null

if ! [ -f /tmp/iso/marker.txt ]; then
    echo "ERROR: overlay-iso marker not found in ISO root"
    echo "ISO root contents:"
    ls -laR /tmp/iso/
    exit 1
fi

if ! grep -q %q /tmp/iso/marker.txt; then
    echo "ERROR: overlay-iso marker has wrong content"
    cat /tmp/iso/marker.txt
    exit 1
fi
echo "PASS: overlay-iso marker found in ISO root"

# --- Verify overlay-rootfs and build-context markers inside the squashfs ---
squashfs_file=$(find /tmp/iso -name "*.squashfs" -print -quit)
if [ -z "$squashfs_file" ]; then
    echo "ERROR: no .squashfs found inside ISO"
    find /tmp/iso -type f
    exit 1
fi
echo "Found squashfs: $squashfs_file"

unsquashfs -d /tmp/rootfs "$squashfs_file" etc/marker.txt etc/build-context-marker.txt > /dev/null 2>&1 || true

if ! [ -f /tmp/rootfs/etc/marker.txt ]; then
    echo "ERROR: overlay-rootfs marker not found in squashfs at etc/marker.txt"
    unsquashfs -l "$squashfs_file" | head -60 || true
    exit 1
fi

if ! grep -q %q /tmp/rootfs/etc/marker.txt; then
    echo "ERROR: overlay-rootfs marker has wrong content"
    cat /tmp/rootfs/etc/marker.txt
    exit 1
fi
echo "PASS: overlay-rootfs marker found in squashfs"

if ! [ -f /tmp/rootfs/etc/build-context-marker.txt ]; then
    echo "ERROR: build-context marker not found in squashfs at etc/build-context-marker.txt"
    unsquashfs -l "$squashfs_file" | head -60 || true
    exit 1
fi

if ! grep -q %q /tmp/rootfs/etc/build-context-marker.txt; then
    echo "ERROR: build-context marker has wrong content"
    cat /tmp/rootfs/etc/build-context-marker.txt
    exit 1
fi
echo "PASS: build-context marker found in squashfs"

echo "All importer verifications passed"
`, isoMarker, rootfsMarker, buildCtxMarker)
}

// importerVolumes returns emptyDir volumes for ISO overlay, rootfs overlay, and build context.
func importerVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name:         "iso-overlay",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name:         "rootfs-overlay",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name:         "build-context",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
}

// importerContainers returns init containers that populate the overlay and build-context
// volumes with marker files. Each importer writes a known file, proving it ran and had
// the volume mounted correctly.
func importerContainers() []corev1.Container {
	return []corev1.Container{
		{
			Name:    "populate-iso-overlay",
			Image:   "busybox:latest",
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{"echo 'iso-overlay-content' > /overlay/marker.txt && echo 'ISO overlay populated'"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "iso-overlay", MountPath: "/overlay"},
			},
		},
		{
			Name:    "populate-rootfs-overlay",
			Image:   "busybox:latest",
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{"mkdir -p /overlay/etc && echo 'rootfs-overlay-content' > /overlay/etc/marker.txt && echo 'Rootfs overlay populated'"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "rootfs-overlay", MountPath: "/overlay"},
			},
		},
		{
			Name:    "populate-build-context",
			Image:   "busybox:latest",
			Command: []string{"/bin/sh", "-c"},
			Args:    []string{"echo 'build-context-content' > /ctx/build-context-marker.txt && echo 'Build context populated'"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "build-context", MountPath: "/ctx"},
			},
		},
	}
}

// importerBindings maps the user-defined volumes to their roles in the build.
func importerBindings() *buildv1alpha2.VolumeBindings {
	return &buildv1alpha2.VolumeBindings{
		OverlayISO:    "iso-overlay",
		OverlayRootfs: "rootfs-overlay",
		BuildContext:  "build-context",
	}
}

var _ = Describe("OSArtifact Importers", func() {
	var tc *TestClients

	BeforeEach(func() {
		tc = SetupTestClients()
	})

	// This test verifies the full importer → volume → build pipeline:
	//  1. A Secret holds a Dockerfile that COPYs a file from the build context
	//  2. Three emptyDir volumes are created: ISO overlay, rootfs overlay, build context
	//  3. Importer init containers populate each volume with a marker file
	//  4. VolumeBindings wire the volumes to overlayISO, overlayRootfs, and buildContext
	//  5. Kaniko builds the Dockerfile, COPYing the build-context marker into the image
	//  6. build-iso receives --overlay-iso and --overlay-rootfs flags with the overlay mounts
	//  7. The exporter extracts the ISO and squashfs to verify all three markers are present
	It("populates overlay and build-context volumes via importers and bakes them into the ISO", func() {
		secret, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "importers-dockerfile-",
				Namespace:    "default",
			},
			StringData: map[string]string{
				"Dockerfile": "FROM quay.io/kairos/hadron:v0.0.1-core-amd64-generic-v3.7.2\nCOPY build-context-marker.txt /etc/build-context-marker.txt\n",
			},
			Type: corev1.SecretTypeOpaque,
		}, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred())

		spec := buildv1alpha2.OSArtifactSpec{
			BaseImageDockerfile: &buildv1alpha2.SecretKeySelector{
				Name: secret.Name,
				Key:  "Dockerfile",
			},
			ISO:            true,
			Volumes:        importerVolumes(),
			Importers:      importerContainers(),
			VolumeBindings: importerBindings(),
		}

		verifyScript := verifyImportersInISO("iso-overlay-content", "rootfs-overlay-content", "build-context-content")

		artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "importers-", spec, verifyScript)
		runArtifactTest(tc, artifactName, artifactLabelSelector)
	})
})
