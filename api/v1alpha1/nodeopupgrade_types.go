/*
Copyright 2025.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeOpUpgradeSpec defines the desired state of NodeOpUpgrade.
type NodeOpUpgradeSpec struct {
	// TargetNodes is a list of node names to run the upgrade operation on.
	// If empty, the operation will run on all nodes.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// Image is the container image to use for running the upgrade command.
	// This should contain the Kairos version and dependencies needed for upgrade.
	// +required
	Image string `json:"image"`

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
	StopOnFailure bool `json:"stopOnFailure,omitempty"`

	// UpgradeRecovery specifies whether to upgrade the recovery partition.
	// When true, the recovery partition will be upgraded.
	// +optional
	UpgradeRecovery bool `json:"upgradeRecovery,omitempty"`

	// UpgradeActive specifies whether to upgrade the active partition.
	// When true, the active partition will be upgraded.
	// This is the default behavior for most upgrade scenarios.
	// +optional
	UpgradeActive bool `json:"upgradeActive,omitempty"`

	// Force specifies whether to perform the upgrade without checking if the current version
	// matches the target version. When true, the upgrade will proceed regardless of version comparison.
	// +optional
	Force bool `json:"force,omitempty"`
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

func init() {
	SchemeBuilder.Register(&NodeOpUpgrade{}, &NodeOpUpgradeList{})
}
