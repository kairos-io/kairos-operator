package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeOpUpgradeSpec defines the desired state of NodeOpUpgrade.
type NodeOpUpgradeSpec struct {
	// NodeSelector specifies a label selector to target specific nodes for the upgrade operation.
	// If empty, the operation will run on all nodes.
	// Examples:
	//   nodeSelector:
	//     matchLabels:
	//       disktype: ssd
	//     matchExpressions:
	//     - key: node.kubernetes.io/instance-type
	//       operator: In
	//       values: ["t3.large", "t3.xlarge"]
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// Image is the container image to use for running the upgrade command.
	// This should contain the Kairos version and dependencies needed for upgrade.
	// +required
	Image string `json:"image"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace
	// to use for pulling any of the images used by this NodeOpUpgrade.
	// If specified, these secrets will be used to authenticate with the container registry.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Concurrency specifies the maximum number of nodes that can run the upgrade operation simultaneously.
	// When set to 0 (default), the operation will run on all target nodes at the same time.
	// When set to a positive number, only that many jobs will run concurrently.
	// As jobs complete, new jobs will be started on remaining nodes until all target nodes are processed.
	// +optional
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	Concurrency int32 `json:"concurrency,omitempty"`

	// StopOnFailure specifies whether to stop creating new jobs when a job fails.
	// When true, if any job fails, no new jobs will be created for remaining nodes.
	// This is useful for canary deployments where you want to stop on the first failure.
	// +optional
	StopOnFailure *bool `json:"stopOnFailure,omitempty"`

	// UpgradeRecovery specifies whether to upgrade the recovery partition.
	// When true, the recovery partition will be upgraded.
	// +optional
	UpgradeRecovery *bool `json:"upgradeRecovery,omitempty"`

	// UpgradeActive specifies whether to upgrade the active partition.
	// When true, the active partition will be upgraded.
	// This is the default behavior for most upgrade scenarios.
	// +optional
	UpgradeActive *bool `json:"upgradeActive,omitempty"`

	// Force specifies whether to perform the upgrade without checking if the current version
	// matches the target version. When true, the upgrade will proceed regardless of version comparison.
	// +optional
	Force *bool `json:"force,omitempty"`

	// Debug specifies whether to run the upgrade with kairos-agent's --debug flag,
	// producing verbose output to help diagnose upgrade failures.
	// +optional
	Debug *bool `json:"debug,omitempty"`

	// UncordonOnFailure specifies whether to uncordon a node if its upgrade fails.
	// When false (the default), a node whose upgrade failed stays cordoned so it can be
	// inspected. Set this to true to have the operator uncordon failed nodes automatically.
	// This value is passed through to the underlying NodeOp.
	// +optional
	UncordonOnFailure *bool `json:"uncordonOnFailure,omitempty"`

	// ExcludePaths is an optional list of additional paths passed to
	// `kairos-agent upgrade` as `--exclude-path` arguments, so those paths on
	// the host are preserved during the upgrade rsync.
	//
	// The operator always excludes /etc/hostname and /etc/hosts (emitted
	// before any paths listed here), because the pod's copies of those files
	// are injected by Kubernetes and would overwrite the node's real ones.
	// To overwrite them intentionally, author a manual NodeOp with an OCI
	// source instead.
	//
	// Requires kairos-agent v3.6.0+ in Spec.Image. Older kairos-agent
	// versions do not recognize --exclude-path and the upgrade Job will fail
	// with "unknown flag". Since Kairos is an atomic-upgrade OS, this only
	// affects users pinning Spec.Image to a pre-3.6.0 kairos image.
	// +optional
	ExcludePaths []string `json:"excludePaths,omitempty"`

	// Resources sets resource requests and limits on the main "nodeop"
	// container of the NodeOp this upgrade generates (the Job container,
	// or its init container in reboot mode). It does not affect the
	// sentinel-creator container of the reboot Job, which is not
	// constrained by this field.
	//   - unset (nil): no resource constraints are set.
	//   - set: requests and limits are used.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// PreflightResources sets resource requests and limits on the
	// preflight Pod of the generated NodeOp. Tri-state:
	//   - unset (nil): the built-in default (200m CPU, 128Mi memory) is
	//     applied to both requests and limits (Guaranteed QoS).
	//   - explicit empty ({}): opt out - no resources are set.
	//   - set: requests and limits are used.
	// +optional
	PreflightResources *corev1.ResourceRequirements `json:"preflightResources,omitempty"`

	// RebootResources sets resource requests and limits on the reboot
	// Pod of the generated NodeOp, with the same tri-state semantics as
	// PreflightResources (built-in default 200m CPU, 128Mi memory).
	// +optional
	RebootResources *corev1.ResourceRequirements `json:"rebootResources,omitempty"`
}

// NodeOpUpgradeStatus defines the observed state of NodeOpUpgrade.
type NodeOpUpgradeStatus struct {
	// Phase represents the current phase of the upgrade operation.
	// Can be "Pending", "Running", "Completed", or "Failed"
	// +optional
	Phase string `json:"phase,omitempty"`

	// NodeOpName is the name of the NodeOp resource created to execute this upgrade.
	// +optional
	NodeOpName string `json:"nodeOpName,omitempty"`

	// NodeStatuses contains the status of the upgrade operation for each target node.
	// This is copied from the underlying NodeOp resource.
	// +optional
	NodeStatuses map[string]NodeStatus `json:"nodeStatuses,omitempty"`

	// Message contains any additional information about the upgrade operation status.
	// +optional
	Message string `json:"message,omitempty"`

	// LastUpdated is the timestamp of the last status update.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeOpUpgrade is the Schema for the nodeopupgrades API.
type NodeOpUpgrade struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeOpUpgradeSpec   `json:"spec,omitempty"`
	Status NodeOpUpgradeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeOpUpgradeList contains a list of NodeOpUpgrade.
type NodeOpUpgradeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeOpUpgrade `json:"items"`
}
