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

// builtImageName returns the name of the built image (for the packed tarball). Only used when building; defaults to the OSArtifact name.
func builtImageName(artifact *buildv1alpha2.OSArtifact) string {
	if artifact.Spec.Image.BuiltImageName != "" {
		return artifact.Spec.Image.BuiltImageName
	}
	return artifact.Name
}

func createImageContainer(containerImage string, artifact *buildv1alpha2.OSArtifact) corev1.Container {
	imageName := builtImageName(artifact)
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
	logger := log.FromContext(ctx)
	arch, err := artifact.Spec.ArchSanitized()
	if err != nil {
		logger.Error(err, "reading arch from spec")
	}

	volumeMounts, overlayISO, overlayRootfs := builderVolumeMounts(artifact)
	artifacts := artifact.Spec.Artifacts

	buildIsoContainer := makeBuildISOContainer(r.ToolImage, artifact, arch, overlayISO, overlayRootfs, volumeMounts)
	buildCloudImageContainer := makeCloudImageContainer(r.ToolImage, artifact, arch, volumeMounts, artifacts)
	extractNetboot := makeNetbootContainer(r.ToolImage, artifact, volumeMounts, artifacts)
	buildAzureCloudImageContainer := makeAzureCloudImageContainer(r.ToolImage, artifact, arch, volumeMounts, artifacts)
	buildGCECloudImageContainer := makeGCECloudImageContainer(r.ToolImage, artifact, arch, volumeMounts, artifacts)

	volumes := builderPodBaseVolumes(pvcName, artifact)
	volumes = appendOCISpecVolume(volumes, artifact)
	volumes = appendCloudConfigVolume(volumes, artifacts)
	volumes = append(volumes, artifact.Spec.Volumes...)

	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: ptr(false),
		RestartPolicy:                corev1.RestartPolicyNever,
		Volumes:                      volumes,
		ImagePullSecrets:             artifact.Spec.ImagePullSecrets,
		InitContainers:               append([]corev1.Container{}, artifact.Spec.Importers...),
	}

	buildCtxVol := ""
	if artifact.Spec.Image.OCISpec != nil {
		buildCtxVol = artifact.Spec.Image.OCISpec.BuildContextVolume
	}
	hasOCISpecRef := artifact.Spec.Image.OCISpec != nil && artifact.Spec.Image.OCISpec.Ref != nil
	hasBuildOptions := artifact.Spec.Image.BuildOptions != nil

	switch {
	case artifact.Spec.Image.Ref != "":
		podSpec.InitContainers = append(podSpec.InitContainers,
			unpackContainer("baseimage", r.ToolImage, artifact.Spec.Image.Ref, arch))
	case hasOCISpecRef:
		podSpec.InitContainers = append(podSpec.InitContainers, baseImageBuildContainers(buildCtxVol)...)
	case hasBuildOptions:
		return &corev1.Pod{}
	default:
		return &corev1.Pod{}
	}

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

// builderVolumeMounts returns volume mounts for the builder pod and overlay volume names from artifacts.
func builderVolumeMounts(artifact *buildv1alpha2.OSArtifact) ([]corev1.VolumeMount, string, string) {
	mounts := []corev1.VolumeMount{
		{Name: "artifacts", MountPath: "/artifacts"},
		{Name: "rootfs", MountPath: "/rootfs"},
	}
	overlayISO, overlayRootfs := "", ""
	if artifact.Spec.Artifacts != nil {
		overlayISO = artifact.Spec.Artifacts.OverlayISOVolume
		overlayRootfs = artifact.Spec.Artifacts.OverlayRootfsVolume
	}
	if artifact.Spec.Artifacts != nil && artifact.Spec.Artifacts.GRUBConfig != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: "/iso/iso-overlay/boot/grub2/grub.cfg",
			SubPath:   "grub.cfg",
		})
	}
	mounts = appendOverlayVolumeMounts(mounts, overlayISO, overlayRootfs)
	if artifact.Spec.Artifacts != nil && artifact.Spec.Artifacts.CloudConfigRef != nil {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "cloudconfig",
			MountPath: "/cloud-config.yaml",
			SubPath:   artifact.Spec.Artifacts.CloudConfigRef.Key,
		})
	}
	return mounts, overlayISO, overlayRootfs
}

func buildISOCommand(artifact *buildv1alpha2.OSArtifact, arch, overlayISO, overlayRootfs string) string {
	var cmd strings.Builder
	cmd.WriteString("auroraboot --debug build-iso")
	fmt.Fprintf(&cmd, " --override-name %s", artifact.Name)
	cmd.WriteString(" --date=false")
	cmd.WriteString(" --output /artifacts")
	if arch != "" {
		fmt.Fprintf(&cmd, " --arch %s", arch)
	}
	appendOverlayFlags(&cmd, overlayISO, overlayRootfs)
	cmd.WriteString(" dir:/rootfs")
	if artifact.Spec.Artifacts != nil && (artifact.Spec.Artifacts.CloudConfigRef != nil || artifact.Spec.Artifacts.GRUBConfig != "") {
		cmd.WriteString(" --cloud-config /cloud-config.yaml")
	}
	return cmd.String()
}

func makeBuildISOContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, arch, overlayISO, overlayRootfs string, mounts []corev1.VolumeMount) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-iso",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{buildISOCommand(artifact, arch, overlayISO, overlayRootfs)},
		VolumeMounts:    mounts,
	}
}

func buildCloudImageCmd(artifact *buildv1alpha2.OSArtifact, arch string, artifacts *buildv1alpha2.ArtifactSpec) string {
	var c strings.Builder
	c.WriteString("auroraboot --debug")
	c.WriteString(" --set 'disk.raw=true'")
	c.WriteString(" --set 'disable_netboot=true'")
	c.WriteString(" --set 'disable_http_server=true'")
	c.WriteString(" --set 'state_dir=/artifacts'")
	c.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&c, " --set 'arch=%s'", arch)
	}
	if artifacts != nil && artifacts.DiskSize != "" {
		fmt.Fprintf(&c, " --set 'disk.size=%s'", artifacts.DiskSize)
	}
	if artifacts != nil && artifacts.CloudConfigRef != nil {
		c.WriteString(" --cloud-config /cloud-config.yaml")
	}
	fmt.Fprintf(&c,
		" && file=$(ls /artifacts/*.raw 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.raw",
		artifact.Name)
	return c.String()
}

func makeCloudImageContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, arch string, mounts []corev1.VolumeMount, artifacts *buildv1alpha2.ArtifactSpec) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-cloud-image",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{buildCloudImageCmd(artifact, arch, artifacts)},
		VolumeMounts:    mounts,
	}
}

func buildNetbootCmd(artifact *buildv1alpha2.OSArtifact) string {
	var c strings.Builder
	c.WriteString("auroraboot --debug netboot")
	fmt.Fprintf(&c, " /artifacts/%s.iso", artifact.Name)
	c.WriteString(" /artifacts")
	fmt.Fprintf(&c, " %s", artifact.Name)
	return c.String()
}

func netbootURL(artifacts *buildv1alpha2.ArtifactSpec) string {
	if artifacts != nil {
		return artifacts.NetbootURL
	}
	return ""
}

func makeNetbootContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, mounts []corev1.VolumeMount, artifacts *buildv1alpha2.ArtifactSpec) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-netboot",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Env:             []corev1.EnvVar{{Name: "URL", Value: netbootURL(artifacts)}},
		Args:            []string{buildNetbootCmd(artifact)},
		VolumeMounts:    mounts,
	}
}

func buildAzureCmd(artifact *buildv1alpha2.OSArtifact, arch string, artifacts *buildv1alpha2.ArtifactSpec) string {
	var c strings.Builder
	c.WriteString("auroraboot --debug")
	c.WriteString(" --set 'disk.vhd=true'")
	c.WriteString(" --set 'disable_netboot=true'")
	c.WriteString(" --set 'disable_http_server=true'")
	c.WriteString(" --set 'state_dir=/artifacts'")
	c.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&c, " --set 'arch=%s'", arch)
	}
	if artifacts != nil && artifacts.CloudConfigRef != nil {
		c.WriteString(" --cloud-config /cloud-config.yaml")
	}
	fmt.Fprintf(&c,
		" && file=$(ls /artifacts/*.vhd 2>/dev/null | head -n1) && [ -n \"$file\" ] && mv \"$file\" /artifacts/%s.vhd",
		artifact.Name)
	return c.String()
}

func makeAzureCloudImageContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, arch string, mounts []corev1.VolumeMount, artifacts *buildv1alpha2.ArtifactSpec) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-azure-cloud-image",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{buildAzureCmd(artifact, arch, artifacts)},
		VolumeMounts:    mounts,
	}
}

func buildGCECmd(artifact *buildv1alpha2.OSArtifact, arch string, artifacts *buildv1alpha2.ArtifactSpec) string {
	var c strings.Builder
	c.WriteString("auroraboot --debug")
	c.WriteString(" --set 'disk.gce=true'")
	c.WriteString(" --set 'disable_netboot=true'")
	c.WriteString(" --set 'disable_http_server=true'")
	c.WriteString(" --set 'state_dir=/artifacts'")
	c.WriteString(" --set 'container_image=dir:/rootfs'")
	if arch != "" {
		fmt.Fprintf(&c, " --set 'arch=%s'", arch)
	}
	if artifacts != nil && artifacts.CloudConfigRef != nil {
		c.WriteString(" --cloud-config /cloud-config.yaml")
	}
	fmt.Fprintf(&c,
		" && file=$(ls /artifacts/*.raw.gce.tar.gz 2>/dev/null | head -n1) && [ -n \"$file\" ] && "+
			"mv \"$file\" /artifacts/%s.gce.tar.gz",
		artifact.Name)
	return c.String()
}

func makeGCECloudImageContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, arch string, mounts []corev1.VolumeMount, artifacts *buildv1alpha2.ArtifactSpec) corev1.Container {
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-gce-cloud-image",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{buildGCECmd(artifact, arch, artifacts)},
		VolumeMounts:    mounts,
	}
}

func builderPodBaseVolumes(pvcName string, artifact *buildv1alpha2.OSArtifact) []corev1.Volume {
	return []corev1.Volume{
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
					LocalObjectReference: corev1.LocalObjectReference{Name: artifact.Name},
				},
			},
		},
	}
}

func appendOCISpecVolume(volumes []corev1.Volume, artifact *buildv1alpha2.OSArtifact) []corev1.Volume {
	if artifact.Spec.Image.OCISpec != nil && artifact.Spec.Image.OCISpec.Ref != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "ocispec",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: artifact.Spec.Image.OCISpec.Ref.Name,
				},
			},
		})
	}
	return volumes
}

func appendCloudConfigVolume(volumes []corev1.Volume, artifacts *buildv1alpha2.ArtifactSpec) []corev1.Volume {
	if artifacts != nil && artifacts.CloudConfigRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "cloudconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: artifacts.CloudConfigRef.Name,
					Optional:   ptr(true),
				},
			},
		})
	}
	return volumes
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
				"--destination", "whatever", // TODO: when spec.image.push is true, use builtImageName and push (mount PushCredentialsSecretRef for kaniko auth)
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
