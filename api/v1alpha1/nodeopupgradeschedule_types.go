package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NodeOpUpgradeScheduleSpec defines the desired state of NodeOpUpgradeSchedule.
type NodeOpUpgradeScheduleSpec struct {
	// Schedule specifies when to check for an upgrade, as a five-field cron
	// expression: minute, hour, day of month, month, and day of week.
	// +required
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// TimeZone is the time zone used to interpret Schedule, such as
	// "America/Denver". Defaults to "UTC" when omitted.
	// +optional
	// +kubebuilder:default="UTC"
	// +kubebuilder:validation:MinLength=1
	TimeZone string `json:"timeZone,omitempty"`

	// Suspend pauses future checks and upgrades. It does not stop an upgrade
	// that has already started.
	// +optional
	// +kubebuilder:default=false
	Suspend bool `json:"suspend,omitempty"`

	// ImagePolicy selects an image from a registry. When omitted, upgrades
	// use UpgradeTemplate.Image. When set, the selected image replaces the
	// template image in each generated NodeOpUpgrade.
	// +optional
	ImagePolicy *NodeOpUpgradeScheduleImagePolicy `json:"imagePolicy,omitempty"`

	// UpgradeTemplate defines the spec of each generated NodeOpUpgrade.
	// Its image is required and provides the starting image when ImagePolicy
	// is configured. All other upgrade settings are passed through.
	// +required
	UpgradeTemplate NodeOpUpgradeSpec `json:"upgradeTemplate"`
}

// NodeOpUpgradeScheduleImagePolicy defines how to select an upgrade image.
type NodeOpUpgradeScheduleImagePolicy struct {
	// Repository is the container image repository to check, without a tag,
	// digest, or URL scheme. For example, "ghcr.io/example/kairos".
	// The validation pattern is the anchored NameRegexp from
	// github.com/distribution/reference v0.6.0 (regexp.go).
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^(?:(?:(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])(?:\.(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]))*|\[(?:[a-fA-F0-9:]+)\])(?::[0-9]+)?/)?[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*)*$`
	Repository string `json:"repository"`

	// Semver is the semantic version constraint used to select eligible tags,
	// with the highest matching version selected. Tags must be semantic
	// versions, optionally prefixed with "v".
	// Prereleases are excluded unless the constraint explicitly includes them,
	// for example ">=3.6.2-0 <3.7.0". The selected version must also be newer
	// than the current version; matching the constraint alone is not enough.
	//
	// Examples for a starting version of 3.6.2:
	//   - ">=3.6.2 <3.7.0": allow patch updates within 3.6.
	//   - ">=3.6.2 <4.0.0": allow minor and patch updates within major version 3.
	//   - ">=3.6.2": allow updates across major versions too.
	//   - "=3.6.2": select exactly version 3.6.2.
	//
	// These are fixed bounds, not rules calculated from each node's installed
	// version. Allowing major updates also allows minor and patch updates.
	// The controller validates the constraint syntax.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`\S`
	Semver string `json:"semver"`

	// ImagePullSecrets references registry credentials in the schedule's
	// namespace. These credentials are used for image discovery and are
	// added to the generated upgrade's image pull secrets.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// NodeOpUpgradeScheduleStatus defines the observed state of NodeOpUpgradeSchedule.
type NodeOpUpgradeScheduleStatus struct {
	// ObservedGeneration is the most recent generation processed by the controller.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NextScheduledTime is the next cron execution. It is cleared while
	// suspended, while an upgrade is active, or when the schedule is invalid.
	// After a restart, one overdue execution is processed before advancing to
	// the next future execution.
	// +optional
	NextScheduledTime *metav1.Time `json:"nextScheduledTime,omitempty"`

	// LastCheckTime is the time of the most recent completed image check.
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`

	// AvailableImage is the highest matching newer image found by the most
	// recent successful check, including its original tag and resolved digest.
	// It is compared with LastSuccessfulImage, or UpgradeTemplate.Image before
	// the first successful upgrade. It does not describe installed node versions.
	// It is cleared when no upgrade is available, a check fails, or the spec changes.
	// +optional
	AvailableImage string `json:"availableImage,omitempty"`

	// LastScheduledTime is the schedule time of the most recent processed
	// execution, including executions where no new image was selected.
	// +optional
	LastScheduledTime *metav1.Time `json:"lastScheduledTime,omitempty"`

	// LastAttemptedImage is the image used by the most recently created
	// upgrade. Images selected by ImagePolicy are pinned by digest.
	// +optional
	LastAttemptedImage string `json:"lastAttemptedImage,omitempty"`

	// LastSuccessfulImage is the image used by the most recently completed
	// successful upgrade.
	// +optional
	LastSuccessfulImage string `json:"lastSuccessfulImage,omitempty"`

	// ActiveUpgradeRef references the current NodeOpUpgrade in the schedule's
	// namespace. A failed upgrade remains referenced until it is addressed.
	// If the referenced resource is missing, new executions stay blocked until
	// the reference is cleared after the missing upgrade has been investigated.
	// +optional
	ActiveUpgradeRef *corev1.LocalObjectReference `json:"activeUpgradeRef,omitempty"`

	// Conditions describe the current state of the schedule.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeOpUpgradeSchedule is the Schema for the nodeopupgradeschedules API.
type NodeOpUpgradeSchedule struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of NodeOpUpgradeSchedule
	// +required
	Spec NodeOpUpgradeScheduleSpec `json:"spec"`

	// status defines the observed state of NodeOpUpgradeSchedule
	// +optional
	Status NodeOpUpgradeScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeOpUpgradeScheduleList contains a list of NodeOpUpgradeSchedule.
type NodeOpUpgradeScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeOpUpgradeSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &NodeOpUpgradeSchedule{}, &NodeOpUpgradeScheduleList{})
		return nil
	})
}
