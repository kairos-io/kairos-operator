/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	buildv1alpha2 "github.com/kairos-io/kairos-operator/api/v1alpha2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func unpackContainer(id, containerImage, pullImage, arch string) corev1.Container {
	cmd := "auroraboot unpack"
	if arch != "" {
		cmd = fmt.Sprintf("%s --arch %s", cmd, arch)
	}
	cmd = fmt.Sprintf("%s %s /rootfs", cmd, pullImage)

	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            fmt.Sprintf("pull-image-%s", id),
		Image:           containerImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{cmd},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "rootfs",
				MountPath: "/rootfs",
			},
		},
	}
}

func pushImageName(artifact *buildv1alpha2.OSArtifact) string {
	if artifact.Spec.Image.Push != nil && artifact.Spec.Image.Push.ImageName != "" {
		return artifact.Spec.Image.Push.ImageName
	}
	if artifact.Spec.Image.Ref != "" {
		return artifact.Spec.Image.Ref
	}
	return artifact.Name
}

func createImageContainer(containerImage string, artifact *buildv1alpha2.OSArtifact) corev1.Container {
	imageName := pushImageName(artifact)
	arch, _ := artifact.Spec.ArchSanitized()

	packCmd := "luet util pack"
	if arch != "" {
		packCmd = fmt.Sprintf("luet util pack --arch %s --os linux", arch)
	}
	packCmd = fmt.Sprintf("%s %s test.tar %s.tar", packCmd, imageName, artifact.Name)

	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "create-image",
		Image:           containerImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			fmt.Sprintf(
				"tar -czvpf test.tar -C /rootfs . && %s && chmod +r %s.tar && mv %s.tar /artifacts",
				packCmd,
				artifact.Name,
				artifact.Name,
			),
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "rootfs",
				MountPath: "/rootfs",
			},
			{
				Name:      "artifacts",
				MountPath: "/artifacts",
			},
		},
	}
}

func osReleaseContainer(containerImage string) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "os-release",
		Image:           containerImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			"cp -rfv /etc/os-release /rootfs/etc/os-release",
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "config",
				MountPath: "/etc/os-release",
				SubPath:   "os-release",
			},
			{
				Name:      "rootfs",
				MountPath: "/rootfs",
			},
		},
	}
}

func kairosReleaseContainer(containerImage string) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "kairos-release",
		Image:           containerImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			"cp -rfv /etc/kairos-release /rootfs/etc/kairos-release",
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "config",
				MountPath: "/etc/kairos-release",
				SubPath:   "kairos-release",
			},
			{
				Name:      "rootfs",
				MountPath: "/rootfs",
			},
		},
	}
}

func (r *OSArtifactReconciler) newArtifactPVC(artifact *buildv1alpha2.OSArtifact) *corev1.PersistentVolumeClaim {
	if artifact.Spec.Volume == nil {
		artifact.Spec.Volume = &corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: map[corev1.ResourceName]resource.Quantity{
					"storage": resource.MustParse("10Gi"),
				},
			},
		}
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      artifact.Name + "-artifacts",
			Namespace: artifact.Namespace,
		},
		Spec: *artifact.Spec.Volume,
	}

	return pvc
}

func (r *OSArtifactReconciler) newBuilderPod(ctx context.Context, pvcName string, artifact *buildv1alpha2.OSArtifact) *corev1.Pod {
	var cmd strings.Builder

	logger := log.FromContext(ctx)
	arch, err := artifact.Spec.ArchSanitized()
	if err != nil {
		logger.Error(err, "reading arch from spec")
	}

	cmd.WriteString("auroraboot --debug build-iso")
	fmt.Fprintf(&cmd, " --override-name %s", artifact.Name)
	cmd.WriteString(" --date=false")
	cmd.WriteString(" --output /artifacts")
	if arch != "" {
		fmt.Fprintf(&cmd, " --arch %s", arch)
	}
	artifacts := artifact.Spec.Artifacts
	overlayISO, overlayRootfs := "", ""
	if artifacts != nil {
		overlayISO, overlayRootfs = artifacts.OverlayISOVolume, artifacts.OverlayRootfsVolume
	}
	appendOverlayFlags(&cmd, overlayISO, overlayRootfs)
	cmd.WriteString(" dir:/rootfs")

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "artifacts",
			MountPath: "/artifacts",
		},
		{
			Name:      "rootfs",
			MountPath: "/rootfs",
		},
	}

	if artifacts != nil && artifacts.GRUBConfig != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: "/iso/iso-overlay/boot/grub2/grub.cfg",
			SubPath:   "grub.cfg",
		})
	}

	volumeMounts = appendOverlayVolumeMounts(volumeMounts, overlayISO, overlayRootfs)

	var cloudImgCmd strings.Builder
	cloudImgCmd.WriteString("auroraboot --debug")
	cloudImgCmd.WriteString(" --set 'disk.raw=true'")
	cloudImgCmd.WriteString(" --set 'disable_netboot=true'")
	cloudImgCmd.WriteString(" --set 'disable_http_server=true'")
	cloudImgCmd.WriteString(" --set 'state_dir=/artifacts'")
	cloudImgCmd.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&cloudImgCmd, " --set 'arch=%s'", arch)
	}

	if artifacts != nil && artifacts.DiskSize != "" {
		fmt.Fprintf(&cloudImgCmd, " --set 'disk.size=%s'", artifacts.DiskSize)
	}

	if artifacts != nil && artifacts.CloudConfigRef != nil {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "cloudconfig",
			MountPath: "/cloud-config.yaml",
			SubPath:   artifacts.CloudConfigRef.Key,
		})
		cloudImgCmd.WriteString(" --cloud-config /cloud-config.yaml")
	}

	fmt.Fprintf(&cloudImgCmd,
		" && file=$(ls /artifacts/*.raw 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.raw",
		artifact.Name)

	if artifacts != nil && (artifacts.CloudConfigRef != nil || artifacts.GRUBConfig != "") {
		cmd.WriteString(" --cloud-config /cloud-config.yaml")
	}

	buildIsoContainer := corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-iso",
		Image:           r.ToolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			cmd.String(),
		},
		VolumeMounts: volumeMounts,
	}

	buildCloudImageContainer := corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-cloud-image",
		Image:           r.ToolImage,

		Command: []string{"/bin/bash", "-cxe"},
		Args: []string{
			cloudImgCmd.String(),
		},
		VolumeMounts: volumeMounts,
	}

	var netbootCmd strings.Builder
	netbootCmd.WriteString("auroraboot --debug netboot")
	fmt.Fprintf(&netbootCmd, " /artifacts/%s.iso", artifact.Name)
	netbootCmd.WriteString(" /artifacts")
	fmt.Fprintf(&netbootCmd, " %s", artifact.Name)

	extractNetboot := corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-netboot",
		Image:           r.ToolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Env: []corev1.EnvVar{{
			Name: "URL",
			Value: func() string {
				if artifacts != nil {
					return artifacts.NetbootURL
				}
				return ""
			}(),
		}},
		Args: []string{
			netbootCmd.String(),
		},
		VolumeMounts: volumeMounts,
	}

	var azureCmd strings.Builder
	azureCmd.WriteString("auroraboot --debug")
	azureCmd.WriteString(" --set 'disk.vhd=true'")
	azureCmd.WriteString(" --set 'disable_netboot=true'")
	azureCmd.WriteString(" --set 'disable_http_server=true'")
	azureCmd.WriteString(" --set 'state_dir=/artifacts'")
	azureCmd.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&azureCmd, " --set 'arch=%s'", arch)
	}

	if artifacts != nil && artifacts.CloudConfigRef != nil {
		azureCmd.WriteString(" --cloud-config /cloud-config.yaml")
	}

	fmt.Fprintf(&azureCmd,
		" && file=$(ls /artifacts/*.vhd 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.vhd",
		artifact.Name)
	buildAzureCloudImageContainer := corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-azure-cloud-image",
		Image:           r.ToolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			azureCmd.String(),
		},
		VolumeMounts: volumeMounts,
	}

	var gceCmd strings.Builder
	gceCmd.WriteString("auroraboot --debug")
	gceCmd.WriteString(" --set 'disk.gce=true'")
	gceCmd.WriteString(" --set 'disable_netboot=true'")
	gceCmd.WriteString(" --set 'disable_http_server=true'")
	gceCmd.WriteString(" --set 'state_dir=/artifacts'")
	gceCmd.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&gceCmd, " --set 'arch=%s'", arch)
	}

	if artifacts != nil && artifacts.CloudConfigRef != nil {
		gceCmd.WriteString(" --cloud-config /cloud-config.yaml")
	}

	fmt.Fprintf(&gceCmd,
		" && file=$(ls /artifacts/*.raw.gce.tar.gz 2>/dev/null | head -n1) && [ -n \"$file\" ] && "+
			"mv \"$file\" /artifacts/%s.gce.tar.gz",
		artifact.Name)
	buildGCECloudImageContainer := corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-gce-cloud-image",
		Image:           r.ToolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args: []string{
			gceCmd.String(),
		},
		VolumeMounts: volumeMounts,
	}

	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: ptr(false),
		RestartPolicy:                corev1.RestartPolicyNever,
		Volumes: []corev1.Volume{
			{
				Name: "artifacts",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
						ReadOnly:  false,
					},
				},
			},
			{
				Name:         "rootfs",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
			{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: artifact.Name,
						},
					},
				},
			},
		},
	}

	if artifact.Spec.Image.OCISpec != nil && artifact.Spec.Image.OCISpec.Ref != nil {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "ocispec",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: artifact.Spec.Image.OCISpec.Ref.Name,
				},
			},
		})
	}

	if artifacts != nil && artifacts.CloudConfigRef != nil {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "cloudconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: artifacts.CloudConfigRef.Name,
					Optional:   ptr(true),
				},
			},
		})
	}

	podSpec.Volumes = append(podSpec.Volumes, artifact.Spec.Volumes...)

	podSpec.ImagePullSecrets = append(podSpec.ImagePullSecrets, artifact.Spec.ImagePullSecrets...)

	podSpec.InitContainers = append([]corev1.Container{}, artifact.Spec.Importers...)
	// Build context volume is only used when building from OCISpec (user may COPY from context); not used for BuildOptions-only.
	buildCtxVol := ""
	if artifact.Spec.Image.OCISpec != nil {
		buildCtxVol = artifact.Spec.Image.OCISpec.BuildContextVolume
	}
	hasOCISpecRef := artifact.Spec.Image.OCISpec != nil && artifact.Spec.Image.OCISpec.Ref != nil
	hasBuildOptions := artifact.Spec.Image.BuildOptions != nil
	if artifact.Spec.Image.Ref != "" {
		// Pre-built image
		podSpec.InitContainers = append(podSpec.InitContainers,
			unpackContainer("baseimage", r.ToolImage, artifact.Spec.Image.Ref, arch))
	} else if hasOCISpecRef {
		// Build from user's OCI spec (OCISpec only, or OCISpec + BuildOptions: inject FROM baseImage at top, user spec, then kairos-init at bottom)
		// TODO: when BuildOptions set, inject FROM baseImage at top and append kairos-init block; pass BuildOptions as --build-arg
		podSpec.InitContainers = append(podSpec.InitContainers, baseImageBuildContainers(buildCtxVol)...)
	} else if hasBuildOptions {
		// BuildOptions only: operator constructs default OCI build definition
		// TODO: Phase 1 - default OCI build + build-args (see docs/osartifact-two-stage-api/README.md)
		return &corev1.Pod{}
	} else {
		// Validation ensures we don't reach here (at least one of buildOptions or ociSpec when ref empty)
		return &corev1.Pod{}
	}

	// If base image was a non kairos one, either one we built with kaniko or prebuilt,
	// convert it to a Kairos one, in a best effort manner.
	// TODO: Implement this conversion with kairos-init?
	// if artifact.Spec.Image.OCISpec != nil || (artifact.Spec.Image.BuildOptions != nil && artifact.Spec.Image.BuildOptions.BaseImage != "") {
	// 	podSpec.InitContainers = append(podSpec.InitContainers,
	// 		corev1.Container{
	// 			ImagePullPolicy: corev1.PullAlways,
	// 			Name:            "convert-to-kairos",
	// 			Image:           "busybox",
	// 			Command:         []string{"/bin/echo"},
	// 			Args:            []string{"TODO"},
	// 			VolumeMounts: []corev1.VolumeMount{
	// 				{
	// 					Name:      "rootfs",
	// 					MountPath: "/rootfs",
	// 				},
	// 			},
	// 		})
	// }

	if artifacts != nil {
		for i, bundle := range artifacts.Bundles {
			podSpec.InitContainers = append(podSpec.InitContainers, unpackContainer(fmt.Sprint(i), r.ToolImage, bundle, arch))
		}
		if artifacts.OSRelease != "" {
			podSpec.InitContainers = append(podSpec.InitContainers, osReleaseContainer(r.ToolImage))
		}
		if artifacts.KairosRelease != "" {
			podSpec.InitContainers = append(podSpec.InitContainers, kairosReleaseContainer(r.ToolImage))
		}
	}

	// build-iso runs as an init container when ISO or Netboot requested
	if artifacts != nil && (artifacts.ISO || artifacts.Netboot) {
		podSpec.InitContainers = append(podSpec.InitContainers, buildIsoContainer)
	}

	if artifacts != nil && artifacts.Netboot {
		podSpec.Containers = append(podSpec.Containers, extractNetboot)
	}

	if artifacts != nil && artifacts.CloudImage {
		podSpec.Containers = append(podSpec.Containers, buildCloudImageContainer)
	}

	if artifacts != nil && artifacts.AzureImage {
		podSpec.Containers = append(podSpec.Containers, buildAzureCloudImageContainer)
	}

	if artifacts != nil && artifacts.GCEImage {
		podSpec.Containers = append(podSpec.Containers, buildGCECloudImageContainer)
	}

	podSpec.Containers = append(podSpec.Containers, createImageContainer(r.ToolImage, artifact))

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: artifact.Name + "-",
			Namespace:    artifact.Namespace,
		},
		Spec: podSpec,
	}
}

func ptr[T any](val T) *T {
	return &val
}

func appendOverlayFlags(cmd *strings.Builder, overlayISO, overlayRootfs string) {
	if overlayISO != "" {
		cmd.WriteString(" --overlay-iso /overlay-iso")
	}
	if overlayRootfs != "" {
		cmd.WriteString(" --overlay-rootfs /overlay-rootfs")
	}
}

func appendOverlayVolumeMounts(mounts []corev1.VolumeMount, overlayISO, overlayRootfs string) []corev1.VolumeMount {
	if overlayISO != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      overlayISO,
			MountPath: "/overlay-iso",
		})
	}
	if overlayRootfs != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      overlayRootfs,
			MountPath: "/overlay-rootfs",
		})
	}
	return mounts
}

func baseImageBuildContainers(buildContextVolume string) []corev1.Container {
	kanikoVolumeMounts := []corev1.VolumeMount{
		{
			Name:      "rootfs",
			MountPath: "/rootfs",
		},
		{
			Name:      "ocispec",
			MountPath: "/workspace/ocispec",
		},
	}

	if buildContextVolume != "" {
		kanikoVolumeMounts = append(kanikoVolumeMounts, corev1.VolumeMount{
			Name:      buildContextVolume,
			MountPath: "/workspace",
		})
	}

	return []corev1.Container{
		{
			ImagePullPolicy: corev1.PullAlways,
			Name:            "kaniko-build",
			Image:           "gcr.io/kaniko-project/executor:latest",
			Args: []string{
				"--dockerfile", "ocispec/Dockerfile",
				"--context", "dir:///workspace",
				"--destination", "whatever", // We don't push, but it needs this
				"--tar-path", "/rootfs/image.tar",
				"--no-push",
				"--ignore-path=/product_uuid", // Mounted by kubelet, can't be deleted between stages
			},
			VolumeMounts: kanikoVolumeMounts,
		},
		{
			ImagePullPolicy: corev1.PullAlways,
			Name:            "image-extractor",
			Image:           "quay.io/luet/base",
			Args: []string{
				"util", "unpack", "--local", "file:////rootfs/image.tar", "/rootfs",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "rootfs",
					MountPath: "/rootfs",
				},
			},
		},
		{
			ImagePullPolicy: corev1.PullAlways,
			Name:            "cleanup",
			Image:           "busybox",
			Command:         []string{"/bin/rm"},
			Args: []string{
				"/rootfs/image.tar",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "rootfs",
					MountPath: "/rootfs",
				},
			},
		},
	}
}
