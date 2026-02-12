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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OSArtifactSpec defines the desired state of OSArtifact
type OSArtifactSpec struct {
	// There are 3 ways to specify a Kairos image:

	// Points to a prepared kairos image (e.g. a released one)
	ImageName string `json:"imageName,omitempty"`

	// Points to a vanilla (non-Kairos) image. osbuilder will try to convert this to a Kairos image
	BaseImageName string `json:"baseImageName,omitempty"`

	// Points to a Secret that contains a Dockerfile. osbuilder will build the image using that Dockerfile
	// and will try to create a Kairos image from it.
	// The Dockerfile may contain Go template syntax (e.g. {{ .MyVar }}) which will be rendered
	// before building. Template values can be provided via DockerfileTemplateValuesFrom (ConfigMap)
	// and/or DockerfileTemplateValues (inline).
	BaseImageDockerfile *SecretKeySelector `json:"baseImageDockerfile,omitempty"`

	// References a Secret whose data entries are used as template values
	// when rendering the Dockerfile template. Each key in the Secret becomes
	// a template variable (e.g. key "BaseImage" is accessed as {{ .BaseImage }}).
	// +optional
	DockerfileTemplateValuesFrom *SecretKeySelector `json:"dockerfileTemplateValuesFrom,omitempty"`

	ISO bool `json:"iso,omitempty"`

	// Disk-only stuff
	DiskSize   string `json:"diskSize,omitempty"`
	CloudImage bool   `json:"cloudImage,omitempty"`
	AzureImage bool   `json:"azureImage,omitempty"`
	GCEImage   bool   `json:"gceImage,omitempty"`

	Netboot    bool   `json:"netboot,omitempty"`
	NetbootURL string `json:"netbootURL,omitempty"`

	// Architecture to use when building ISOs and images (amd64 or arm64).
	// When pulling container images for a different architecture than the host,
	// this must be specified. Defaults to the host arch if not specified.
	// +kubebuilder:validation:Enum=amd64;arm64
	Arch string `json:"arch,omitempty"`

	CloudConfigRef *SecretKeySelector `json:"cloudConfigRef,omitempty"`
	GRUBConfig     string             `json:"grubConfig,omitempty"`

	Bundles       []string `json:"bundles,omitempty"`
	OSRelease     string   `json:"osRelease,omitempty"`
	KairosRelease string   `json:"kairosRelease,omitempty"`

	ImagePullSecrets []corev1.LocalObjectReference     `json:"imagePullSecrets,omitempty"`
	Exporters        []batchv1.JobSpec                 `json:"exporters,omitempty"`
	Volume           *corev1.PersistentVolumeClaimSpec `json:"volume,omitempty"`
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
	arch := s.Arch
	if arch != "amd64" && arch != "arm64" && arch != "" {
		return "", errors.New("arch must be either 'amd64', 'arm64', or empty")
	}
	return arch, nil
}
