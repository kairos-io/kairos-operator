package e2e

import (
	"fmt"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
)

// verifyOverlaysInISO returns a shell script that extracts the ISO, then:
//   - checks the ISO filesystem root for the overlay-iso marker (isoMarker)
//   - extracts the squashfs and checks the rootfs for the overlay-rootfs marker (rootfsMarker)
func verifyOverlaysInISO(isoMarker, rootfsMarker string) string {
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

# --- Verify overlay-rootfs marker inside the squashfs ---
squashfs_file=$(find /tmp/iso -name "*.squashfs" -print -quit)
if [ -z "$squashfs_file" ]; then
    echo "ERROR: no .squashfs found inside ISO"
    find /tmp/iso -type f
    exit 1
fi
echo "Found squashfs: $squashfs_file"

unsquashfs -d /tmp/rootfs "$squashfs_file" etc/marker.txt > /dev/null 2>&1 || true

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

echo "All overlay verifications passed"
`, isoMarker, rootfsMarker)
}

// overlayVolumes returns two emptyDir volumes for ISO and rootfs overlays.
func overlayVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name:         "iso-overlay",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		{
			Name:         "rootfs-overlay",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
}

// overlayImporters returns init containers that populate the overlay volumes with marker files.
// Each importer writes a known file, proving it ran and had the volume mounted correctly.
func overlayImporters() []corev1.Container {
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
	}
}

// overlayBindings maps the user-defined volumes to their overlay roles in the build.
func overlayBindings() *buildv1alpha2.VolumeBindings {
	return &buildv1alpha2.VolumeBindings{
		OverlayISO:    "iso-overlay",
		OverlayRootfs: "rootfs-overlay",
	}
}

var _ = Describe("OSArtifact Importers", func() {
	var tc *TestClients

	BeforeEach(func() {
		tc = SetupTestClients()
	})

	// This test verifies the full importer → volume → build pipeline:
	//  1. Two emptyDir volumes are created on the builder pod
	//  2. Importer init containers run first: one writes a marker into the ISO overlay,
	//     another writes a marker into the rootfs overlay
	//  3. VolumeBindings map those volumes to overlayISO / overlayRootfs
	//  4. build-iso receives --overlay-iso and --overlay-rootfs flags with the mounts
	//  5. The exporter extracts the ISO and squashfs to verify both markers are present
	It("populates overlay volumes via importers and bakes them into the ISO", func() {
		spec := buildv1alpha2.OSArtifactSpec{
			ImageName:      "quay.io/kairos/hadron:v0.0.1-core-amd64-generic-v3.7.2",
			ISO:            true,
			Volumes:        overlayVolumes(),
			Importers:      overlayImporters(),
			VolumeBindings: overlayBindings(),
		}

		verifyScript := verifyOverlaysInISO("iso-overlay-content", "rootfs-overlay-content")

		artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "importers-", spec, verifyScript)
		runArtifactTest(tc, artifactName, artifactLabelSelector)
	})
})
