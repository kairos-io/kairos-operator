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

	// Cordon specifies whether to cordon the node before running the operation.
	// When true, the node will be marked as unschedulable before the operation starts
	// and will be uncordoned after the operation completes successfully.
	// +optional
	// +kubebuilder:default=false
	Cordon *bool `json:"cordon,omitempty"`

	// DrainOptions specifies the options for draining the node before running the operation.
	// When enabled, pods will be evicted from the node before the operation starts.
	// This requires the Cordon field to be true.
	// +optional
	DrainOptions *DrainOptions `json:"drainOptions,omitempty"`

	// RebootOnSuccess specifies whether to reboot the node after the operation completes successfully.
	// When true, a privileged pod will be created to trigger a reboot using nsenter.
	// +optional
	// +kubebuilder:default=false
	RebootOnSuccess *bool `json:"rebootOnSuccess,omitempty"`

	// BackoffLimit specifies the number of retries before marking this job failed.
	// This directly maps to the Job spec.backoffLimit field.
	// If not specified, defaults to 6 (Kubernetes default).
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// Concurrency specifies the maximum number of nodes that can run the operation simultaneously.
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
	// +kubebuilder:default=false
	StopOnFailure *bool `json:"stopOnFailure,omitempty"`
}

// DrainOptions defines the options for draining a node.
type DrainOptions struct {
	// Enabled specifies whether to drain the node.
	// When true, pods will be evicted from the node before the operation starts.
	// +optional
	// +kubebuilder:default=false
	Enabled *bool `json:"enabled,omitempty"`

	// Force specifies whether to force the drain operation.
	// When true, pods that do not have a controller will be evicted.
	// +optional
	// +kubebuilder:default=false
	Force *bool `json:"force,omitempty"`

	// GracePeriodSeconds is the time in seconds given to each pod to terminate gracefully.
	// If negative, the default value specified in the pod will be used.
	// +optional
	GracePeriodSeconds *int32 `json:"gracePeriodSeconds,omitempty"`

	// IgnoreDaemonSets specifies whether to ignore DaemonSet-managed pods.
	// When true, DaemonSet-managed pods will be ignored during the drain operation.
	// +optional
	// +kubebuilder:default=true
	IgnoreDaemonSets *bool `json:"ignoreDaemonSets,omitempty"`

	// DeleteEmptyDirData specifies whether to delete local data in emptyDir volumes.
	// When true, data in emptyDir volumes will be deleted during the drain operation.
	// +optional
	// +kubebuilder:default=false
	DeleteEmptyDirData *bool `json:"deleteEmptyDirData,omitempty"`

	// TimeoutSeconds is the length of time to wait before giving up on the drain operation.
	// If not specified, a default timeout will be used.
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
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

	// RebootStatus represents the reboot state of this node.
	// Can be "not-requested" (reboot not requested), "cancelled" (reboot was requested but cancelled due to job failure), "pending" (reboot requested but not completed), or "completed" (reboot finished)
	// +optional
	RebootStatus string `json:"rebootStatus,omitempty"`

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
