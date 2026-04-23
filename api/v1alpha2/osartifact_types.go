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

package v1alpha2

import (
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var reservedVolumeNames = map[string]bool{
	"artifacts":   true,
	"rootfs":      true,
	"config":      true,
	"ocispec":     true,
	"cloudconfig": true,
}

// BuildImage specifies the registry, repository, and tag of the built image. Used when building (Ref empty); makes it easy for tools like updatecli or renovate to bump only the tag.
type BuildImage struct {
	// Registry is the image registry (e.g. myregistry.com).
	Registry string `json:"registry"`

	// Repository is the image repository and path (e.g. my-ns/myimage).
	Repository string `json:"repository"`

	// Tag is the image tag (e.g. v3.6.0).
	Tag string `json:"tag"`
}

// ImageRef returns the full image reference (registry/repository:tag).
func (b *BuildImage) ImageRef() string {
	if b == nil {
		return ""
	}
	return b.Registry + "/" + b.Repository + ":" + b.Tag
}

// ImageSpec describes how to obtain the Stage 1 OCI image (pre-built or built from a definition).
//
// When Ref is set, the image is pre-built (no build). BuildOptions and OCISpec are ignored.
//
// When Ref is empty, we build the image. At least one of BuildOptions or OCISpec must be set; both may be set.
// The final build definition is constructed as follows:
//   - OCISpec only: the user's OCI spec (from Ref) is the full definition and must include the base image (FROM) and any kairos-init logic ("advanced" mode).
//   - BuildOptions only: the operator constructs the definition from BuildOptions (injects FROM baseImage when set, then default kairos-init flow).
//   - OCISpec + BuildOptions: the operator injects "FROM buildOptions.baseImage" at the top when baseImage is set, then the user's spec (dashboard + user parts), then the kairos-init block at the bottom. BuildOptions fields populate args passed to kairos-init. The user/dashboard parts must not add their own FROM when both are set.
type ImageSpec struct {
	// Ref is a pre-built Kairos image reference (no build). When set, BuildOptions and OCISpec are ignored.
	Ref string `json:"ref,omitempty"`

	// BuildOptions: when used alone, the operator builds from its default OCI build definition (version required); baseImage, when set, is used as the injected FROM.
	// When used with OCISpec, the operator injects FROM baseImage at the top (when set), then the user's spec, then the kairos-init block at the bottom; BuildOptions supply args for kairos-init.
	// +optional
	BuildOptions *BuildOptions `json:"buildOptions,omitempty"`

	// OCISpec is a user-supplied OCI build definition (with optional templating). When used alone, it must be the full definition.
	// When used with BuildOptions, the operator injects FROM baseImage at the top (when set), then the user's spec, then kairos-init at the bottom. Ref is required when OCISpec is used to build.
	// +optional
	OCISpec *OCISpec `json:"ociSpec,omitempty"`

	// BuildImage is the registry, repository, and tag of the built image. Only used when building (Ref empty); when set, Ref must be empty. When empty, the built image name defaults to the OSArtifact resource name. Splitting into registry/repository/tag makes it easier for tools like updatecli or renovate to bump only the tag.
	// +optional
	BuildImage *BuildImage `json:"buildImage,omitempty"`

	// Push, when true, pushes the built image to a registry. Only used when building; ignored when Ref is set.
	// +optional
	Push bool `json:"push,omitempty"`

	// ImageCredentialsSecretRef references a Secret with registry auth for pull and push (e.g. .dockerconfigjson). Used when pulling image.ref, when building (the OCI build container pulls the base image), and when pushing the built image. When key is empty, the whole Secret is used.
	// +optional
	ImageCredentialsSecretRef *SecretKeySelector `json:"imageCredentialsSecretRef,omitempty"`

	// BuildEnv are environment variables for the OCI build container. Use for proxy settings (e.g. HTTP_PROXY, HTTPS_PROXY, NO_PROXY) or any other build-time env.
	// Only used when building (Ref empty); ignored when using a pre-built image.
	// +optional
	BuildEnv []corev1.EnvVar `json:"buildEnv,omitempty"`

	// CACertificatesVolume names a volume (from spec.volumes) to mount for the OCI build container. Use for custom CA certificates when pulling or pushing images (e.g. private registries). Only used when building (Ref empty).
	// +optional
	CACertificatesVolume string `json:"caCertificatesVolume,omitempty"`

	// PullInsecureRegistry disables TLS verification when pulling the base image during the OCI build step (buildah bud --tls-verify=false). Use for HTTP-only or self-signed-cert base image registries. Only used when building (Ref empty).
	// +optional
	PullInsecureRegistry bool `json:"pullInsecureRegistry,omitempty"`

	// PushInsecureRegistry disables TLS verification when pushing the built image to the registry (buildah push --tls-verify=false). Use for HTTP-only or self-signed-cert destination registries. Only used when Push is true.
	// +optional
	PushInsecureRegistry bool `json:"pushInsecureRegistry,omitempty"`
}

// BuildOptions holds options for building with the default OCI build definition (Stage 1).
// Maps to build definition ARGs and is passed as --build-arg to the OCI build step.
type BuildOptions struct {
	// Version is the Kairos version (e.g. v3.6.0). Required when using buildOptions.
	Version string `json:"version"`

	// BaseImage is the base image for the OCI build (e.g. ubuntu:24.04). When BuildOptions is set, the operator injects "FROM BaseImage" at the top of the final Dockerfile (BuildOptions-only or OCISpec+BuildOptions). Optional.
	BaseImage string `json:"baseImage,omitempty"`

	// Model is the Kairos model (e.g. generic).
	Model string `json:"model,omitempty"`

	// TrustedBoot enables trusted boot (UKI). Phase 2.
	TrustedBoot bool `json:"trustedBoot,omitempty"`

	// KubernetesDistro is the Kubernetes distribution (e.g. k3s, k0s).
	KubernetesDistro string `json:"kubernetesDistro,omitempty"`

	// KubernetesVersion is the Kubernetes version (e.g. v1.28.0).
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// FIPS enables FIPS mode.
	FIPS bool `json:"fips,omitempty"`
}

// OCISpec describes building with a custom OCI build definition (Stage 1).
type OCISpec struct {
	// Ref points to a Secret that contains the OCI build definition (e.g. a Dockerfile-format file).
	Ref *SecretKeySelector `json:"ref,omitempty"`

	// TemplateValuesFrom references a Secret whose data entries are used as template values when rendering the build definition.
	// Only Secrets are supported; ConfigMaps are not. When key is empty, all Secret data keys are used.
	// +optional
	TemplateValuesFrom *SecretKeySelector `json:"templateValuesFrom,omitempty"`

	// TemplateValues are inline template values for rendering the build definition. Takes precedence over TemplateValuesFrom on key conflict.
	// +optional
	TemplateValues map[string]string `json:"templateValues,omitempty"`

	// BuildContextVolume names a volume (from spec.volumes) to mount as the OCI build context at /workspace for the OCI build step.
	// +optional
	BuildContextVolume string `json:"buildContextVolume,omitempty"`
}

// ArtifactSpec describes which artifacts to produce from the Stage 1 image (Stage 2) and related options.
type ArtifactSpec struct {
	// +kubebuilder:validation:Enum=amd64;arm64
	Arch string `json:"arch,omitempty"`

	ISO        bool   `json:"iso,omitempty"`
	DiskSize   string `json:"diskSize,omitempty"`
	CloudImage bool   `json:"cloudImage,omitempty"`
	AzureImage bool   `json:"azureImage,omitempty"`
	GCEImage   bool   `json:"gceImage,omitempty"`

	Netboot    bool   `json:"netboot,omitempty"`
	NetbootURL string `json:"netbootURL,omitempty"`

	CloudConfigRef *SecretKeySelector `json:"cloudConfigRef,omitempty"`
	GRUBConfig     string             `json:"grubConfig,omitempty"`

	Bundles       []string `json:"bundles,omitempty"`
	OSRelease     string   `json:"osRelease,omitempty"`
	KairosRelease string   `json:"kairosRelease,omitempty"`

	// OverlayISOVolume names a volume (from spec.volumes) for --overlay-iso (auroraboot build-iso).
	// +optional
	OverlayISOVolume string `json:"overlayISOVolume,omitempty"`

	// OverlayRootfsVolume names a volume (from spec.volumes) for --overlay-rootfs (auroraboot build-iso).
	// +optional
	OverlayRootfsVolume string `json:"overlayRootfsVolume,omitempty"`

	// UKI specifies signed (UKI) artifact outputs and the volume holding signing keys.
	// When any of iso, container, or efi is true, keysVolume is required and must reference spec.volumes.
	// +optional
	UKI *UKISpec `json:"uki,omitempty"`

	// Volume names a volume (from spec.volumes) to use for build outputs (ISO, cloud images, etc.) instead of the operator-created PVC. When set, the operator does not create a PVC; the builder pod and exporter jobs use this volume (mounted at /artifacts). When empty, the operator creates a PVC. Useful for mounting a host directory (e.g. hostPath) so artifacts land directly on the node. Only relevant when at least one artifact type is enabled.
	// +optional
	Volume string `json:"volume,omitempty"`
}

// UKISpec groups UKI (signed/trusted boot) artifact options. Keys are read from the volume named by KeysVolume.
//
// +kubebuilder:validation:XValidation:rule="!( (self.iso || self.container || self.efi) && (!has(self.keysVolume) || self.keysVolume == \"\") )",message="keysVolume is required when at least one of iso, container, or efi is true"
type UKISpec struct {
	// ISO requests a signed UKI ISO (auroraboot build-uki --output-type iso).
	// +optional
	ISO bool `json:"iso,omitempty"`

	// Container requests a signed UKI OCI image (auroraboot build-uki --output-type container).
	// +optional
	Container bool `json:"container,omitempty"`

	// EFI requests a raw directory of signed .efi files (auroraboot build-uki --output-type uki).
	// +optional
	EFI bool `json:"efi,omitempty"`

	// KeysVolume names a volume (from spec.volumes) that holds signing keys (PK.auth, KEK.auth, db.auth, db.key, db.pem, tpm2-pcr-private.pem). Required whenever the uki block is used (all UKI outputs need signing keys).
	KeysVolume string `json:"keysVolume"`
}

// OSArtifactSpec defines the desired state of OSArtifact
type OSArtifactSpec struct {
	// Image describes how to obtain the Stage 1 OCI image. Required.
	// When image.ref is set, the image is pre-built. When empty, at least one of image.buildOptions or image.ociSpec must be set (both allowed; when both are set, operator injects FROM + kairos-init).
	Image ImageSpec `json:"image"`

	// Artifacts describes which artifacts to produce from the Stage 1 image (Stage 2).
	// When nil or no artifact type enabled, only Stage 1 runs (OCI build + optional push).
	// +optional
	Artifacts *ArtifactSpec `json:"artifacts,omitempty"`

	ImagePullSecrets []corev1.LocalObjectReference     `json:"imagePullSecrets,omitempty"`
	Exporters        []batchv1.JobSpec                 `json:"exporters,omitempty"`
	Volume           *corev1.PersistentVolumeClaimSpec `json:"volume,omitempty"`

	// Volumes defines additional volumes available to importers and the build pod.
	// Volume names must not collide with internal names: "artifacts", "rootfs", "config", "ocispec", "cloudconfig".
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// Importers are init containers that run before the build phase on the builder Pod.
	// +optional
	Importers []corev1.Container `json:"importers,omitempty"`
}

type SecretKeySelector struct {
	Name string `json:"name"`
	// +optional
	Key string `json:"key,omitempty"`
}

type ArtifactPhase string

const (
	Pending   = "Pending"
	Building  = "Building"
	Exporting = "Exporting"
	Ready     = "Ready"
	Error     = "Error"
)

// OSArtifactStatus defines the observed state of OSArtifact
type OSArtifactStatus struct {
	// +kubebuilder:default=Pending
	Phase ArtifactPhase `json:"phase,omitempty"`
	// Message reports success or failure details (e.g. export job failure reason).
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="Age"

// OSArtifact is the Schema for the osartifacts API
type OSArtifact struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OSArtifactSpec   `json:"spec,omitempty"`
	Status OSArtifactStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OSArtifactList contains a list of OSArtifact
type OSArtifactList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OSArtifact `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OSArtifact{}, &OSArtifactList{})
}

func (s *OSArtifactSpec) ArchSanitized() (string, error) {
	arch := ""
	if s.Artifacts != nil {
		arch = s.Artifacts.Arch
	}
	if arch != "amd64" && arch != "arm64" && arch != "" {
		return "", errors.New("arch must be either 'amd64', 'arm64', or empty")
	}
	return arch, nil
}

// Validate checks that the OSArtifactSpec is internally consistent:
// - spec.image is required; when ref empty, at least one of buildOptions or ociSpec; volume refs must exist in spec.volumes
// - spec.artifacts overlay volume refs must exist in spec.volumes
func (s *OSArtifactSpec) Validate() error {
	volumeNames := make(map[string]bool, len(s.Volumes))
	for i, v := range s.Volumes {
		if v.Name == "" {
			return fmt.Errorf("spec.volumes[%d].name must not be empty", i)
		}
		if reservedVolumeNames[v.Name] {
			return fmt.Errorf("volume name %q is reserved for internal use", v.Name)
		}
		if volumeNames[v.Name] {
			return fmt.Errorf("duplicate volume name %q in spec.volumes", v.Name)
		}
		volumeNames[v.Name] = true
	}

	if err := validateImageSpec(&s.Image, volumeNames); err != nil {
		return err
	}
	// When using a pre-built image (image.ref), artifacts are required; otherwise the OSArtifact would be a no-op.
	if s.Image.Ref != "" && s.Artifacts == nil {
		return fmt.Errorf("spec.artifacts is required when spec.image.ref is set (pre-built image); specify which artifacts to produce (e.g. ISO, cloud image)")
	}
	if s.Artifacts != nil {
		if err := s.validateArtifactSpec(volumeNames); err != nil {
			return err
		}
	}

	return nil
}

func validateImageSpec(img *ImageSpec, volumeNames map[string]bool) error {
	hasRef := img.Ref != ""
	hasBuildOptions := img.BuildOptions != nil
	hasOCISpec := img.OCISpec != nil && img.OCISpec.Ref != nil

	// BuildImage is only used when building; mutually exclusive with Ref.
	if img.BuildImage != nil && img.BuildImage.ImageRef() != "" && hasRef {
		return fmt.Errorf("spec.image.buildImage is only used when building; ref must be empty when buildImage is set")
	}
	if img.BuildImage != nil && (img.BuildImage.Registry != "" || img.BuildImage.Repository != "" || img.BuildImage.Tag != "") {
		if img.BuildImage.Registry == "" || img.BuildImage.Repository == "" || img.BuildImage.Tag == "" {
			return fmt.Errorf("spec.image.buildImage: when set, registry, repository, and tag are all required")
		}
	}

	if hasRef {
		// Pre-built image; no build. BuildOptions and OCISpec are ignored.
		return nil
	}

	// Building: at least one of BuildOptions or OCISpec must be set.
	if !hasBuildOptions && !hasOCISpec {
		return fmt.Errorf("spec.image: when ref is empty, at least one of buildOptions or ociSpec must be set")
	}

	// When pushing, buildImage (registry, repository, tag) is required so we have a valid destination.
	if img.Push {
		if img.BuildImage == nil || img.BuildImage.Registry == "" || img.BuildImage.Repository == "" || img.BuildImage.Tag == "" {
			return fmt.Errorf("spec.image.push is true but buildImage is missing or incomplete; when pushing, registry, repository, and tag are required")
		}
	}

	if hasBuildOptions {
		if img.BuildOptions.Version == "" {
			return fmt.Errorf("spec.image.buildOptions.version is required when using buildOptions")
		}
		if img.BuildOptions.BaseImage == "" {
			return fmt.Errorf("spec.image.buildOptions.baseImage is required when using buildOptions")
		}
	}
	if hasOCISpec && img.OCISpec.BuildContextVolume != "" {
		if !volumeNames[img.OCISpec.BuildContextVolume] {
			return fmt.Errorf("spec.image.ociSpec.buildContextVolume references volume %q which is not defined in spec.volumes", img.OCISpec.BuildContextVolume)
		}
	}
	if img.CACertificatesVolume != "" {
		if !volumeNames[img.CACertificatesVolume] {
			return fmt.Errorf("spec.image.caCertificatesVolume references volume %q which is not defined in spec.volumes", img.CACertificatesVolume)
		}
	}

	return nil
}

func (s *OSArtifactSpec) validateArtifactSpec(volumeNames map[string]bool) error {
	a := s.Artifacts
	hasStandard := a.ISO || a.CloudImage || a.AzureImage || a.GCEImage || a.Netboot
	hasUKI := a.UKI != nil && (a.UKI.ISO || a.UKI.Container || a.UKI.EFI)
	if !hasStandard && !hasUKI {
		return fmt.Errorf("spec.artifacts: at least one artifact type must be enabled (iso, cloudImage, azureImage, gceImage, netboot, or uki.iso/uki.container/uki.efi)")
	}
	if a.OverlayISOVolume != "" && !volumeNames[a.OverlayISOVolume] {
		return fmt.Errorf("spec.artifacts.overlayISOVolume references volume %q which is not defined in spec.volumes", a.OverlayISOVolume)
	}
	if a.OverlayRootfsVolume != "" && !volumeNames[a.OverlayRootfsVolume] {
		return fmt.Errorf("spec.artifacts.overlayRootfsVolume references volume %q which is not defined in spec.volumes", a.OverlayRootfsVolume)
	}
	if a.Volume != "" && !volumeNames[a.Volume] {
		return fmt.Errorf("spec.artifacts.volume references volume %q which is not defined in spec.volumes", a.Volume)
	}
	if hasUKI {
		if a.UKI.KeysVolume == "" {
			return fmt.Errorf("spec.artifacts.uki.keysVolume is required when any of uki.iso, uki.container, or uki.efi is true")
		}
		if !volumeNames[a.UKI.KeysVolume] {
			return fmt.Errorf("spec.artifacts.uki.keysVolume references volume %q which is not defined in spec.volumes", a.UKI.KeysVolume)
		}
	}
	return nil
}
