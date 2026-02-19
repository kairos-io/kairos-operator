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

	// Push configures pushing the built image to a registry. Only used when building; ignored when Ref is set.
	// +optional
	Push *ImagePush `json:"push,omitempty"`
}

// BuildOptions holds options for building with the default OCI build definition (Stage 1).
// Maps to build definition ARGs and is passed to kaniko as --build-arg.
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

// ImagePush configures pushing the built image to a registry.
type ImagePush struct {
	// ImageName is the full image reference to push to (e.g. my-registry.example.com/my-ns/image:tag).
	ImageName string `json:"imageName"`

	// CredentialsSecretRef references a Secret with registry auth (e.g. .dockerconfigjson).
	// Only Secrets are supported. When key is empty, the whole Secret is used (e.g. for .dockerconfigjson).
	// +optional
	CredentialsSecretRef *SecretKeySelector `json:"credentialsSecretRef,omitempty"`
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

	// BuildContextVolume names a volume (from spec.volumes) to mount as the OCI build context at /workspace (kaniko).
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
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

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

	if hasRef {
		// Pre-built image; no build. BuildOptions and OCISpec are ignored.
		return nil
	}

	// Building: at least one of BuildOptions or OCISpec must be set.
	if !hasBuildOptions && !hasOCISpec {
		return fmt.Errorf("spec.image: when ref is empty, at least one of buildOptions or ociSpec must be set")
	}

	if hasBuildOptions && img.BuildOptions.Version == "" {
		return fmt.Errorf("spec.image.buildOptions.version is required when using buildOptions")
	}
	if hasOCISpec && img.OCISpec.BuildContextVolume != "" {
		if !volumeNames[img.OCISpec.BuildContextVolume] {
			return fmt.Errorf("spec.image.ociSpec.buildContextVolume references volume %q which is not defined in spec.volumes", img.OCISpec.BuildContextVolume)
		}
	}

	return nil
}

func (s *OSArtifactSpec) validateArtifactSpec(volumeNames map[string]bool) error {
	a := s.Artifacts
	if a.OverlayISOVolume != "" && !volumeNames[a.OverlayISOVolume] {
		return fmt.Errorf("spec.artifacts.overlayISOVolume references volume %q which is not defined in spec.volumes", a.OverlayISOVolume)
	}
	if a.OverlayRootfsVolume != "" && !volumeNames[a.OverlayRootfsVolume] {
		return fmt.Errorf("spec.artifacts.overlayRootfsVolume references volume %q which is not defined in spec.volumes", a.OverlayRootfsVolume)
	}
	return nil
}
