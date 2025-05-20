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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NodeOpSpec defines the desired state of NodeOp.
type NodeOpSpec struct {
	// TargetNodes is a list of node names to run the operation on.
	// If empty, the operation will run on all nodes.
	// +optional
	TargetNodes []string `json:"targetNodes,omitempty"`

	// Command is the command to run on the target nodes.
	// This will be executed in a container with the node's root filesystem mounted.
	Command []string `json:"command"`

	// HostMountPath is the path where the node's root filesystem will be mounted.
	// Defaults to "/host"
	// +optional
	// +kubebuilder:default="/host"
	HostMountPath string `json:"hostMountPath,omitempty"`

	// Image is the container image to use for running the command.
	// Defaults to "busybox:latest"
	// +optional
	// +kubebuilder:default="busybox:latest"
	Image string `json:"image,omitempty"`
}

// NodeOpStatus defines the observed state of NodeOp.
type NodeOpStatus struct {
	// Phase represents the current phase of the operation.
	// Can be "Pending", "Running", "Completed", or "Failed"
	// +optional
	Phase string `json:"phase,omitempty"`

	// NodeStatuses contains the status of the operation for each target node.
	// +optional
	NodeStatuses map[string]NodeStatus `json:"nodeStatuses,omitempty"`

	// LastUpdated is the timestamp of the last status update.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// NodeStatus represents the status of the operation on a specific node.
type NodeStatus struct {
	// Phase represents the current phase of the operation on this node.
	// Can be "Pending", "Running", "Completed", or "Failed"
	Phase string `json:"phase"`

	// JobName is the name of the Job created for this node.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// Message contains any additional information about the operation status.
	// +optional
	Message string `json:"message,omitempty"`

	// LastUpdated is the timestamp of the last status update for this node.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeOp is the Schema for the nodeops API.
type NodeOp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeOpSpec   `json:"spec,omitempty"`
	Status NodeOpStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeOpList contains a list of NodeOp.
type NodeOpList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeOp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeOp{}, &NodeOpList{})
}
