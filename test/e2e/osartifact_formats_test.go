package e2e

import (
	"fmt"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	. "github.com/onsi/ginkgo/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	overrideName = "chronos"
)

// createArtifactWithExporter creates an OSArtifact with a custom exporter verification script
func createArtifactWithExporter(tc *TestClients, namePrefix string, spec buildv1alpha2.OSArtifactSpec,
	verifyScript string,
) (string, labels.Selector) {
	spec.Exporters = []batchv1.JobSpec{
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
								{
									Name:      "artifacts",
									ReadOnly:  true,
									MountPath: "/artifacts",
								},
							},
						},
					},
				},
			},
		},
	}

	artifact := &buildv1alpha2.OSArtifact{
		TypeMeta: metav1.TypeMeta{
			Kind:       "OSArtifact",
			APIVersion: buildv1alpha2.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: namePrefix,
		},
		Spec: spec,
	}

	return tc.CreateArtifact(artifact)
}

// runArtifactTest is a helper that runs the standard artifact test pattern
func runArtifactTest(tc *TestClients, artifactName string, artifactLabelSelector labels.Selector) {
	DeferCleanup(func() { tc.Cleanup(artifactName, artifactLabelSelector) })
	tc.WaitForBuildCompletion(artifactName, artifactLabelSelector)
	tc.WaitForExportCompletion(artifactLabelSelector)
}

var _ = Describe("OSArtifact NameOverride Tests", func() {
	var tc *TestClients

	BeforeEach(func() {
		tc = SetupTestClients()
	})

	Describe("NameOverride with ISO", func() {
		It("produces an ISO with the override name instead of metadata.name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				iso_file=$(ls /artifacts/%s.iso 2>/dev/null | head -n1)
				if [ -z "$iso_file" ]; then
					echo "ERROR: expected /artifacts/%s.iso not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				if [ ! -s "$iso_file" ]; then
					echo "ERROR: ISO is empty"
					exit 1
				fi
				echo "PASS: ISO produced with nameOverride: $iso_file"
			`, overrideName, overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					ISO:  true,
					Arch: "amd64",
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					ISO: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-iso-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("NameOverride with CloudImage (Raw)", func() {
		It("produces a raw disk with the override name instead of metadata.name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				raw_file=$(ls /artifacts/%s.raw 2>/dev/null | head -n1)
				if [ -z "$raw_file" ]; then
					echo "ERROR: expected /artifacts/%s.raw not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				if [ ! -s "$raw_file" ]; then
					echo "ERROR: Raw file is empty"
					exit 1
				fi
				echo "PASS: Raw disk produced with nameOverride: $raw_file"
			`, overrideName, overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					CloudImage: true,
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					CloudImage: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-raw-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("NameOverride with Azure (VHD)", func() {
		It("produces a VHD with the override name instead of metadata.name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				vhd_file=$(ls /artifacts/%s.vhd 2>/dev/null | head -n1)
				if [ -z "$vhd_file" ]; then
					echo "ERROR: expected /artifacts/%s.vhd not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				if [ ! -s "$vhd_file" ]; then
					echo "ERROR: VHD file is empty"
					exit 1
				fi
				tail -c 512 "$vhd_file" | grep -q "conectix" || {
					echo "ERROR: VHD does not have valid footer"
					exit 1
				}
				echo "PASS: VHD produced with nameOverride: $vhd_file"
			`, overrideName, overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					AzureImage: true,
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					AzureImage: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-azure-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("NameOverride with GCE", func() {
		It("produces a GCE image with the override name instead of metadata.name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				gce_file=$(ls /artifacts/%s.gce.tar.gz 2>/dev/null | head -n1)
				if [ -z "$gce_file" ]; then
					echo "ERROR: expected /artifacts/%s.gce.tar.gz not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				if [ ! -s "$gce_file" ]; then
					echo "ERROR: GCE tar.gz is empty"
					exit 1
				fi
				temp_dir=$(mktemp -d)
				tar -xzf "$gce_file" -C "$temp_dir"
				if [ ! -f "$temp_dir/disk.raw" ]; then
					echo "ERROR: GCE archive does not contain disk.raw"
					exit 1
				fi
				if [ ! -s "$temp_dir/disk.raw" ]; then
					echo "ERROR: disk.raw in archive is empty"
					exit 1
				fi
				echo "PASS: GCE image produced with nameOverride: $gce_file"
			`, overrideName, overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					GCEImage: true,
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					GCEImage: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-gce-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("NameOverride with UKI", func() {
		It("produces UKI artifacts with the override name instead of metadata.name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				# UKI produces <name_override>.iso
				uki_iso=$(ls /artifacts/%s.iso 2>/dev/null | head -n1)
				if [ -z "$uki_iso" ]; then
					echo "ERROR: expected /artifacts/%s.iso not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				if [ ! -s "$uki_iso" ]; then
					echo "ERROR: UKI ISO is empty"
					exit 1
				fi
				echo "PASS: UKI ISO produced with nameOverride: $uki_iso"
			`, overrideName, overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					Arch: "amd64",
					UKI: &buildv1alpha2.UKISpec{
						ISO:        true,
						KeysVolume: "uki-keys",
					},
				},
				Importers: []corev1.Container{
					{
						Name:  "generate-uki-keys",
						Image: "quay.io/kairos/auroraboot:latest",
						Command: []string{
							"/bin/sh",
							"-c",
						},
						Args: []string{
							"auroraboot genkey my-uki -o keys",
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "uki-keys",
								MountPath: "/keys",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "uki-keys",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					UKI: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-uki-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("NameOverride with Netboot", func() {
		It("produces netboot artifacts with the override name", func() {
			verifyScript := fmt.Sprintf(`
				set -e
				# ISO is still built (netboot requires it) and must be named from
				# metadata.name - the netboot override must not leak into the ISO name
				iso_file=$(ls /artifacts/*.iso 2>/dev/null | head -n1)
				if [ -z "$iso_file" ]; then
					echo "ERROR: no .iso file found"
					ls -la /artifacts/ || true
					exit 1
				fi
				case "$(basename "$iso_file")" in
					%[1]s*) ;;
					*)
						echo "ERROR: ISO $(basename "$iso_file") is not named from metadata.name (expected prefix %[1]s)"
						exit 1
						;;
				esac
				if [ ! -s "$iso_file" ]; then
					echo "ERROR: ISO is empty"
					exit 1
				fi
				if [ -e "/artifacts/%[2]s.iso" ]; then
					echo "ERROR: /artifacts/%[2]s.iso must not exist (ISO named from netboot override)"
					ls -la /artifacts/ || true
					exit 1
				fi
				kernel_file=$(ls /artifacts/%[2]s-kernel 2>/dev/null | head -n1)
				if [ -z "$kernel_file" ]; then
					echo "ERROR: expected /artifacts/%[2]s-kernel not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				initrd_file=$(ls /artifacts/%[2]s-initrd 2>/dev/null | head -n1)
				if [ -z "$initrd_file" ]; then
					echo "ERROR: expected /artifacts/%[2]s-initrd not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				squashfs_file=$(ls /artifacts/%[2]s.squashfs 2>/dev/null | head -n1)
				if [ -z "$squashfs_file" ]; then
					echo "ERROR: expected /artifacts/%[2]s.squashfs not found"
					ls -la /artifacts/ || true
					exit 1
				fi
				for file in "$kernel_file" "$initrd_file" "$squashfs_file"; do
					if [ ! -s "$file" ]; then
						echo "ERROR: file is empty: $file"
						exit 1
					fi
				done
				echo "PASS: Netboot artifacts produced with nameOverride, ISO keeps metadata.name"
			`, "nameoverride-netboot-", overrideName)

			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{
					Netboot:    true,
					NetbootURL: "https://kairos.io",
				},
				NameOverride: buildv1alpha2.NameOverrideSpec{
					Netboot: overrideName,
				},
			}
			artifactName, artifactLabelSelector := createArtifactWithExporter(tc, "nameoverride-netboot-", spec, verifyScript)
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("OSArtifact Format Tests", func() {
		var artifactName string
		var artifactLabelSelector labels.Selector

		BeforeEach(func() {
			verifyScript := `
				set -e
				# Check that raw file exists
				raw_file=$(ls /artifacts/*.raw 2>/dev/null | head -n1)
				if [ -z "$raw_file" ]; then
					echo "No .raw file found"
					exit 1
				fi
				# Check that it's a valid disk image (has non-zero size)
				if [ ! -s "$raw_file" ]; then
					echo "Raw file is empty"
					exit 1
				fi
				# Check file size is reasonable (at least 100MB)
				size=$(stat -c%s "$raw_file")
				if [ "$size" -lt 104857600 ]; then
					echo "Raw file too small: $size bytes"
					exit 1
				fi
				echo "Raw disk verification passed: $raw_file"
			`
			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{CloudImage: true},
			}
			artifactName, artifactLabelSelector = createArtifactWithExporter(tc, "cloudimage-", spec, verifyScript)
		})

		It("builds a valid raw disk image", func() {
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("Netboot", func() {
		var artifactName string
		var artifactLabelSelector labels.Selector

		BeforeEach(func() {
			artifact := &buildv1alpha2.OSArtifact{
				TypeMeta: metav1.TypeMeta{
					Kind:       "OSArtifact",
					APIVersion: buildv1alpha2.GroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "netboot-",
				},
				Spec: buildv1alpha2.OSArtifactSpec{
					Image: buildv1alpha2.ImageSpec{
						Ref: HadronPreKairosified,
					},
					Artifacts: &buildv1alpha2.ArtifactSpec{
						ISO:        true,
						Netboot:    true,
						NetbootURL: "http://example.com",
					},
					Exporters: []batchv1.JobSpec{
						{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name:    "verify",
											Image:   "debian:latest",
											Command: []string{"bash"},
											Args: []string{
												"-xec",
												`
													set -e
													# Check for kernel file (pattern: *-kernel)
													kernel_file=$(ls /artifacts/*-kernel 2>/dev/null | head -n1)
													if [ -z "$kernel_file" ]; then
														echo "No kernel file found (pattern: *-kernel)"
														ls -la /artifacts/ || true
														exit 1
													fi
													# Check for initrd file (pattern: *-initrd)
													initrd_file=$(ls /artifacts/*-initrd 2>/dev/null | head -n1)
													if [ -z "$initrd_file" ]; then
														echo "No initrd file found (pattern: *-initrd)"
														ls -la /artifacts/ || true
														exit 1
													fi
													# Check for squashfs file (pattern: *.squashfs)
													squashfs_file=$(ls /artifacts/*.squashfs 2>/dev/null | head -n1)
													if [ -z "$squashfs_file" ]; then
														echo "No squashfs file found (pattern: *.squashfs)"
														ls -la /artifacts/ || true
														exit 1
													fi
													# Verify files are non-empty
													for file in "$kernel_file" "$initrd_file" "$squashfs_file"; do
														if [ ! -s "$file" ]; then
															echo "File is empty: $file"
															exit 1
														fi
													done
													echo "Netboot artifacts verification passed"
													echo "Kernel: $kernel_file"
													echo "Initrd: $initrd_file"
													echo "Squashfs: $squashfs_file"
												`,
											},
											VolumeMounts: []corev1.VolumeMount{
												{
													Name:      "artifacts",
													ReadOnly:  true,
													MountPath: "/artifacts",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			}

			artifactName, artifactLabelSelector = tc.CreateArtifact(artifact)
		})

		It("builds valid netboot artifacts", func() {
			tc.WaitForBuildCompletion(artifactName, artifactLabelSelector)
			tc.WaitForExportCompletion(artifactLabelSelector)
			tc.Cleanup(artifactName, artifactLabelSelector)
		})
	})

	Describe("AzureImage (VHD)", func() {
		var artifactName string
		var artifactLabelSelector labels.Selector

		BeforeEach(func() {
			verifyScript := `
				set -e
				# Check that VHD file exists
				vhd_file=$(ls /artifacts/*.vhd 2>/dev/null | head -n1)
				if [ -z "$vhd_file" ]; then
					echo "No .vhd file found"
					exit 1
				fi
				# Check that it's non-empty
				if [ ! -s "$vhd_file" ]; then
					echo "VHD file is empty"
					exit 1
				fi
				# Check file size is reasonable (at least 100MB)
				size=$(stat -c%s "$vhd_file")
				if [ "$size" -lt 104857600 ]; then
					echo "VHD file too small: $size bytes"
					exit 1
				fi
				# Check VHD footer (last 512 bytes should contain VHD signature)
				# VHD footer starts at offset -512 and contains "conectix" string
				tail -c 512 "$vhd_file" | grep -q "conectix" || {
					echo "VHD file does not have valid VHD footer"
					exit 1
				}
				echo "VHD verification passed: $vhd_file"
			`
			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{AzureImage: true},
			}
			artifactName, artifactLabelSelector = createArtifactWithExporter(tc, "azure-", spec, verifyScript)
		})

		It("builds a valid Azure VHD image", func() {
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})

	Describe("GCEImage", func() {
		var artifactName string
		var artifactLabelSelector labels.Selector

		BeforeEach(func() {
			verifyScript := `
				set -e
				# Check that GCE tar.gz file exists
				gce_file=$(ls /artifacts/*.gce.tar.gz 2>/dev/null | head -n1)
				if [ -z "$gce_file" ]; then
					echo "No .gce.tar.gz file found"
					exit 1
				fi
				# Check that it's non-empty
				if [ ! -s "$gce_file" ]; then
					echo "GCE tar.gz file is empty"
					exit 1
				fi
				# Extract and verify it contains disk.raw
				temp_dir=$(mktemp -d)
				trap "rm -rf $temp_dir" EXIT
				tar -xzf "$gce_file" -C "$temp_dir"
				if [ ! -f "$temp_dir/disk.raw" ]; then
					echo "GCE archive does not contain disk.raw"
					exit 1
				fi
				# Verify disk.raw is non-empty and reasonable size
				if [ ! -s "$temp_dir/disk.raw" ]; then
					echo "disk.raw in archive is empty"
					exit 1
				fi
				size=$(stat -c%s "$temp_dir/disk.raw")
				if [ "$size" -lt 104857600 ]; then
					echo "disk.raw too small: $size bytes"
					exit 1
				fi
				echo "GCE verification passed: $gce_file"
			`
			spec := buildv1alpha2.OSArtifactSpec{
				Image: buildv1alpha2.ImageSpec{
					Ref: HadronPreKairosified,
				},
				Artifacts: &buildv1alpha2.ArtifactSpec{GCEImage: true},
			}
			artifactName, artifactLabelSelector = createArtifactWithExporter(tc, "gce-", spec, verifyScript)
		})

		It("builds a valid GCE image", func() {
			runArtifactTest(tc, artifactName, artifactLabelSelector)
		})
	})
})
