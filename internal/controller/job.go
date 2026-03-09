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

const aurorabootUnpackCmd = "auroraboot unpack"

func unpackContainer(id, containerImage, pullImage, arch string) corev1.Container {
	cmd := aurorabootUnpackCmd
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

// unpackAndPackToArtifactsContainer unpacks image.ref to /rootfs and writes the packed tarball to /artifacts.
// Stage 1 for ref: one container puts the image tarball in the artifacts volume.
// AuroraBoot uses go-containerregistry's DefaultKeychain, which reads from DOCKER_CONFIG (not Kubernetes ImagePullSecrets).
// When ImageCredentialsSecretRef is set, we mount the credentials and set DOCKER_CONFIG so private image.ref can be pulled.
func unpackAndPackToArtifactsContainer(artifact *buildv1alpha2.OSArtifact, toolImage string) corev1.Container {
	arch, _ := artifact.Spec.ArchSanitized()
	unpackCmd := aurorabootUnpackCmd
	if arch != "" {
		unpackCmd = fmt.Sprintf("%s --arch %s", unpackCmd, arch)
	}
	unpackCmd = fmt.Sprintf("%s %s /rootfs", unpackCmd, artifact.Spec.Image.Ref)

	imageName := builtImageName(artifact)
	packCmd := "luet util pack"
	if arch != "" {
		packCmd = fmt.Sprintf("%s --arch %s --os linux", packCmd, arch)
	}
	packCmd = fmt.Sprintf("%s %s test.tar %s.tar", packCmd, imageName, artifact.Name)

	script := fmt.Sprintf(
		"%s && tar -czvpf test.tar -C /rootfs . && %s && chmod +r %s.tar && mv %s.tar /artifacts",
		unpackCmd, packCmd, artifact.Name, artifact.Name,
	)
	volMounts := []corev1.VolumeMount{
		{Name: "rootfs", MountPath: "/rootfs"},
		{Name: "artifacts", MountPath: "/artifacts"},
	}
	env := []corev1.EnvVar{}
	if artifact.Spec.Image.ImageCredentialsSecretRef != nil {
		volMounts = append(volMounts, corev1.VolumeMount{
			Name:      imageCredentialsVolumeName,
			MountPath: "/root/.docker",
			ReadOnly:  true,
		})
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/root/.docker"})
	}
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "pull-image-baseimage",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{script},
		Env:             env,
		VolumeMounts:    volMounts,
	}
}

// builtImageName returns the name of the built image (for the packed tarball). Only used when building; defaults to the OSArtifact name.
func builtImageName(artifact *buildv1alpha2.OSArtifact) string {
	if artifact.Spec.Image.BuildImage != nil && artifact.Spec.Image.BuildImage.ImageRef() != "" {
		return artifact.Spec.Image.BuildImage.ImageRef()
	}
	return artifact.Name
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
	volumes = appendImageCredentialsVolume(volumes, artifact)
	volumes = appendCloudConfigVolume(volumes, artifacts)
	volumes = append(volumes, artifact.Spec.Volumes...)

	// Two slices: sequential (inits) and parallel (mains). If no mains, promote last init to main so the pod has a main container.
	// Importers are part of inits (they run first).
	inits := append([]corev1.Container{}, artifact.Spec.Importers...)
	var mains []corev1.Container

	buildCtxVol := ""
	if artifact.Spec.Image.OCISpec != nil {
		buildCtxVol = artifact.Spec.Image.OCISpec.BuildContextVolume
	}
	hasOCISpecRef := artifact.Spec.Image.OCISpec != nil && artifact.Spec.Image.OCISpec.Ref != nil

	switch {
	case artifact.Spec.Image.Ref != "":
		inits = append(inits, unpackAndPackToArtifactsContainer(artifact, r.ToolImage))
	case hasOCISpecRef:
		inits = append(inits, kanikoBuildContainer(artifact, buildCtxVol, arch))
	default:
		return &corev1.Pod{}
	}

	if artifacts != nil {
		if hasOCISpecRef {
			inits = append(inits, imageExtractorContainer(r.ToolImage, arch, artifact.Name))
		}
		for i, bundle := range artifacts.Bundles {
			inits = append(inits, unpackContainer(fmt.Sprint(i), r.ToolImage, bundle, arch))
		}
		if artifacts.OSRelease != "" {
			inits = append(inits, osReleaseContainer(r.ToolImage))
		}
		if artifacts.KairosRelease != "" {
			inits = append(inits, kairosReleaseContainer(r.ToolImage))
		}
		// UKI (signed) artifacts: run build-uki for each requested output type (<name>-uki.iso, etc.).
		if artifacts.UKI != nil && (artifacts.UKI.ISO || artifacts.UKI.Container || artifacts.UKI.EFI) {
			if artifacts.UKI.ISO {
				inits = append(inits, makeBuildUKIContainer(r.ToolImage, artifact, "iso", volumeMounts))
			}
			if artifacts.UKI.Container {
				inits = append(inits, makeBuildUKIContainer(r.ToolImage, artifact, "container", volumeMounts))
			}
			if artifacts.UKI.EFI {
				inits = append(inits, makeBuildUKIContainer(r.ToolImage, artifact, "uki", volumeMounts))
			}
		}
		// Unsigned ISO when artifacts.ISO or Netboot is set (output <name>.iso); can run alongside UKI ISO (<name>-uki.iso).
		if artifacts.ISO || artifacts.Netboot {
			inits = append(inits, buildIsoContainer)
		}
		if artifacts.Netboot {
			inits = append(inits, extractNetboot)
		}
		// These can run in parallel (all read from rootfs/artifacts; no ordering between them).
		if artifacts.CloudImage {
			mains = append(mains, buildCloudImageContainer)
		}
		if artifacts.AzureImage {
			mains = append(mains, buildAzureCloudImageContainer)
		}
		if artifacts.GCEImage {
			mains = append(mains, buildGCECloudImageContainer)
		}
	}

	if len(inits) == 0 {
		return &corev1.Pod{}
	}
	// If no main containers, make the last init the main container so the pod has exactly one main.
	if len(mains) == 0 {
		mains = inits[len(inits)-1:]
		inits = inits[:len(inits)-1]
	}
	podSpec := corev1.PodSpec{
		AutomountServiceAccountToken: ptr(false),
		RestartPolicy:                corev1.RestartPolicyNever,
		Volumes:                      volumes,
		ImagePullSecrets:             builderImagePullSecrets(artifact),
		InitContainers:               inits,
		Containers:                   mains,
	}

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
	if artifact.Spec.Artifacts != nil && artifact.Spec.Artifacts.UKI != nil && artifact.Spec.Artifacts.UKI.KeysVolume != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      artifact.Spec.Artifacts.UKI.KeysVolume,
			MountPath: "/uki-keys",
		})
	}
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
	if artifact.Spec.Artifacts != nil && (artifact.Spec.Artifacts.CloudConfigRef != nil || artifact.Spec.Artifacts.GRUBConfig != "") {
		cmd.WriteString(" --cloud-config /cloud-config.yaml")
	}
	cmd.WriteString(" dir:/rootfs")
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

const ukiKeysMountPath = "/uki-keys"

// ukiArtifactName returns the basename for UKI outputs so they do not collide with unsigned artifacts (e.g. <name>-uki.iso vs <name>.iso).
func ukiArtifactName(artifactName string) string {
	return artifactName + "-uki"
}

func buildUKICommand(artifact *buildv1alpha2.OSArtifact, outputType string) string {
	var cmd strings.Builder
	fmt.Fprintf(&cmd, "auroraboot --debug build-uki")
	fmt.Fprintf(&cmd, " --name %s", ukiArtifactName(artifact.Name))
	fmt.Fprintf(&cmd, " --output-dir %s", "/artifacts")
	fmt.Fprintf(&cmd, " --output-type %s", outputType)
	fmt.Fprintf(&cmd, " --public-keys %s", ukiKeysMountPath)
	fmt.Fprintf(&cmd, " --tpm-pcr-private-key %s/tpm2-pcr-private.pem", ukiKeysMountPath)
	fmt.Fprintf(&cmd, " --sb-key %s/db.key", ukiKeysMountPath)
	fmt.Fprintf(&cmd, " --sb-cert %s/db.pem", ukiKeysMountPath)
	// Note: auroraboot build-uki does not support --arch; arch is not passed.
	if artifact.Spec.Artifacts != nil && (artifact.Spec.Artifacts.CloudConfigRef != nil || artifact.Spec.Artifacts.GRUBConfig != "") {
		fmt.Fprintf(&cmd, " --cloud-config %s", "/cloud-config.yaml")
	}
	fmt.Fprintf(&cmd, " %s", "dir:/rootfs")
	return cmd.String()
}

func makeBuildUKIContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, outputType string, mounts []corev1.VolumeMount) corev1.Container {
	name := "build-uki-" + outputType
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            name,
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{buildUKICommand(artifact, outputType)},
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

// isoBasenameForNetboot returns the ISO basename (without .iso) so netboot finds the right file: use UKI name when the ISO was built by build-uki.
func isoBasenameForNetboot(artifact *buildv1alpha2.OSArtifact, artifacts *buildv1alpha2.ArtifactSpec) string {
	if artifacts != nil && artifacts.UKI != nil && artifacts.UKI.ISO {
		return ukiArtifactName(artifact.Name)
	}
	return artifact.Name
}

func buildNetbootCmd(isoBasename string) string {
	var c strings.Builder
	c.WriteString("auroraboot --debug netboot")
	fmt.Fprintf(&c, " /artifacts/%s.iso", isoBasename)
	c.WriteString(" /artifacts")
	fmt.Fprintf(&c, " %s", isoBasename)
	return c.String()
}

func netbootURL(artifacts *buildv1alpha2.ArtifactSpec) string {
	if artifacts != nil {
		return artifacts.NetbootURL
	}
	return ""
}

func makeNetbootContainer(toolImage string, artifact *buildv1alpha2.OSArtifact, mounts []corev1.VolumeMount, artifacts *buildv1alpha2.ArtifactSpec) corev1.Container {
	isoBasename := isoBasenameForNetboot(artifact, artifacts)
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
		Name:            "build-netboot",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Env:             []corev1.EnvVar{{Name: "URL", Value: netbootURL(artifacts)}},
		Args:            []string{buildNetbootCmd(isoBasename)},
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

// kanikoBuildArgs returns kaniko --build-arg KEY=VALUE strings from BuildOptions (default OCI build definition ARGs).
// Only used when artifact.Spec.Image.BuildOptions is set.
func kanikoBuildArgs(artifact *buildv1alpha2.OSArtifact) []string {
	if artifact.Spec.Image.BuildOptions == nil {
		return nil
	}
	opts := artifact.Spec.Image.BuildOptions
	var args []string
	addArg := func(key, value string) {
		if value != "" {
			args = append(args, "--build-arg", key+"="+value)
		}
	}
	addArg("VERSION", opts.Version)
	addArg("BASE_IMAGE", opts.BaseImage)
	addArg("MODEL", opts.Model)
	addArg("KUBERNETES_DISTRO", opts.KubernetesDistro)
	addArg("KUBERNETES_VERSION", opts.KubernetesVersion)
	if opts.TrustedBoot {
		addArg("TRUSTED_BOOT", "true")
	}
	if opts.FIPS {
		addArg("FIPS", "fips")
	}
	return args
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

const imageCredentialsVolumeName = "image-credentials"

// builderImagePullSecrets returns ImagePullSecrets for the builder pod: spec.imagePullSecrets plus the image credentials secret when set (used for pulling image.ref and tool images from private registries).
func builderImagePullSecrets(artifact *buildv1alpha2.OSArtifact) []corev1.LocalObjectReference {
	secrets := append([]corev1.LocalObjectReference{}, artifact.Spec.ImagePullSecrets...)
	if artifact.Spec.Image.ImageCredentialsSecretRef != nil {
		secrets = append(secrets, corev1.LocalObjectReference{Name: artifact.Spec.Image.ImageCredentialsSecretRef.Name})
	}
	return secrets
}

func appendImageCredentialsVolume(volumes []corev1.Volume, artifact *buildv1alpha2.OSArtifact) []corev1.Volume {
	if artifact.Spec.Image.ImageCredentialsSecretRef == nil {
		return volumes
	}
	ref := artifact.Spec.Image.ImageCredentialsSecretRef
	key := ref.Key
	if key == "" {
		key = corev1.DockerConfigJsonKey
	}
	volumes = append(volumes, corev1.Volume{
		Name: imageCredentialsVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: ref.Name,
				Items:      []corev1.KeyToPath{{Key: key, Path: "config.json"}},
			},
		},
	})
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

// imageExtractorContainer unpacks the OCI tarball from /artifacts/<name>.tar (written by Kaniko) to /rootfs using AuroraBoot.
func imageExtractorContainer(toolImage, arch string, artifactName string) corev1.Container {
	cmd := aurorabootUnpackCmd
	if arch != "" {
		cmd = fmt.Sprintf("%s --arch %s", cmd, arch)
	}
	cmd = fmt.Sprintf("%s ocifile:/artifacts/%s.tar /rootfs", cmd, artifactName)
	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "image-extractor",
		Image:           toolImage,
		Command:         []string{"/bin/bash", "-cxe"},
		Args:            []string{cmd},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "rootfs", MountPath: "/rootfs"},
			{Name: "artifacts", MountPath: "/artifacts", ReadOnly: true},
		},
	}
}

// kanikoBuildContainer returns the Kaniko build container. The image tarball is always written to /artifacts/<name>.tar.
func kanikoBuildContainer(artifact *buildv1alpha2.OSArtifact, buildContextVolume string, arch string) corev1.Container {
	kanikoVolumeMounts := []corev1.VolumeMount{
		{Name: "rootfs", MountPath: "/rootfs"},
		{Name: "ocispec", MountPath: "/workspace/ocispec"},
		{Name: "artifacts", MountPath: "/artifacts"},
	}
	if buildContextVolume != "" {
		kanikoVolumeMounts = append(kanikoVolumeMounts, corev1.VolumeMount{
			Name: buildContextVolume, MountPath: "/workspace",
		})
	}
	if artifact.Spec.Image.CACertificatesVolume != "" {
		kanikoVolumeMounts = append(kanikoVolumeMounts, corev1.VolumeMount{
			Name:      artifact.Spec.Image.CACertificatesVolume,
			MountPath: "/kaniko/ssl/certs",
			ReadOnly:  true,
		})
	}
	tarPath := "/artifacts/" + artifact.Name + ".tar"

	doPush := artifact.Spec.Image.Push && artifact.Spec.Image.BuildImage != nil
	destination := "whatever"
	if artifact.Spec.Image.BuildImage != nil && artifact.Spec.Image.BuildImage.ImageRef() != "" {
		destination = artifact.Spec.Image.BuildImage.ImageRef()
	}

	// Use absolute path so Kaniko resolves the file correctly when both
	// build-context (at /workspace) and ocispec (at /workspace/ocispec) are mounted.
	kanikoArgs := []string{
		"--dockerfile", "/workspace/ocispec/" + OCISpecSecretKey,
		"--context", "dir:///workspace",
		"--destination", destination,
		"--tar-path", tarPath,
		"--ignore-path=/product_uuid", // Mounted by kubelet, can't be deleted between stages
	}
	if !doPush {
		kanikoArgs = append(kanikoArgs, "--no-push")
	}
	kanikoArgs = append(kanikoArgs, kanikoBuildArgs(artifact)...)
	if arch != "" {
		kanikoArgs = append(kanikoArgs, "--customPlatform=linux/"+arch)
	}

	// When image credentials are set, mount the secret so Kaniko can pull the base image and optionally push the built image.
	kanikoEnv := []corev1.EnvVar{}
	if artifact.Spec.Image.ImageCredentialsSecretRef != nil {
		kanikoVolumeMounts = append(kanikoVolumeMounts, corev1.VolumeMount{
			Name:      imageCredentialsVolumeName,
			MountPath: "/kaniko/.docker",
			ReadOnly:  true,
		})
		kanikoEnv = append(kanikoEnv, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/kaniko/.docker"})
	}
	if len(artifact.Spec.Image.BuildEnv) > 0 {
		kanikoEnv = append(kanikoEnv, artifact.Spec.Image.BuildEnv...)
	}

	return corev1.Container{
		ImagePullPolicy: corev1.PullAlways,
		Name:            "kaniko-build",
		Image:           "gcr.io/kaniko-project/executor:latest",
		Args:            kanikoArgs,
		Env:             kanikoEnv,
		VolumeMounts:    kanikoVolumeMounts,
		SecurityContext: &corev1.SecurityContext{Privileged: ptr(false)},
	}
}
