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

package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	"github.com/kairos-io/kairos-operator/internal/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// NodeOpReconciler reconciles a NodeOp object
type NodeOpReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get;update;patch

// getNodeOp fetches the NodeOp resource and handles not found errors
func (r *NodeOpReconciler) getNodeOp(ctx context.Context, req ctrl.Request) (*kairosiov1alpha1.NodeOp, error) {
	log := logf.FromContext(ctx)
	nodeOp := &kairosiov1alpha1.NodeOp{}

	err := r.Get(ctx, req.NamespacedName, nodeOp)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get NodeOp")
			return nil, err
		}
		log.Info("NodeOp resource not found. Ignoring since object must be deleted")
		return nil, nil
	}

	// Set TypeMeta fields since they are not stored in etcd
	nodeOp.TypeMeta = metav1.TypeMeta{
		APIVersion: "kairos.io/v1alpha1",
		Kind:       "NodeOp",
	}

	return nodeOp, nil
}

// hasExistingJobs checks if there are any Jobs associated with the NodeOp
func (r *NodeOpReconciler) hasExistingJobs(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) (bool, error) {
	log := logf.FromContext(ctx)

	// Get the operator namespace
	namespace := r.getOperatorNamespace()

	jobList := &batchv1.JobList{}
	err := r.List(ctx, jobList, client.InNamespace(namespace))
	if err != nil {
		log.Error(err, "Failed to list Jobs")
		return false, err
	}

	for _, job := range jobList.Items {
		for _, ownerRef := range job.OwnerReferences {
			if ownerRef.Kind == "NodeOp" && ownerRef.Name == nodeOp.Name {
				log.Info("Found existing Job for NodeOp",
					"nodeOp", nodeOp.Name,
					"job", job.Name)
				return true, nil
			}
		}
	}

	return false, nil
}

// getClusterNodes returns a list of all nodes in the cluster
func (r *NodeOpReconciler) getClusterNodes(ctx context.Context) ([]corev1.Node, error) {
	log := logf.FromContext(ctx)

	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		log.Error(err, "Failed to list cluster nodes")
		return nil, err
	}

	return nodeList.Items, nil
}

// createNodeJob creates a Job for a specific node
func (r *NodeOpReconciler) createNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	log := logf.FromContext(ctx)

	// Create a full name combining all information
	fullName := fmt.Sprintf("%s-%s", nodeOp.Name, node.Name)
	jobName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit)

	// Get the operator namespace
	namespace := r.getOperatorNamespace()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"nodeop": nodeOp.Name,
				"node":   node.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: nodeOp.APIVersion,
					Kind:       nodeOp.Kind,
					Name:       nodeOp.Name,
					UID:        nodeOp.UID,
				},
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": node.Name,
					},
					Containers: []corev1.Container{
						{
							Name:    "nodeop",
							Image:   nodeOp.Spec.Image,
							Command: nodeOp.Spec.Command,
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}

	if err := r.Create(ctx, job); err != nil {
		log.Error(err, "Failed to create Job for node",
			"nodeOp", nodeOp.Name,
			"node", node.Name)
		return err
	}

	// Initialize node status
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}
	nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
		Phase:       "Pending",
		JobName:     jobName,
		Message:     "Job created",
		LastUpdated: metav1.Now(),
	}

	// Update NodeOp status
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOp status after Job creation")
		return err
	}

	log.Info("Created Job for node",
		"nodeOp", nodeOp.Name,
		"node", node.Name,
		"job", jobName)

	return nil
}

// createJobs creates Jobs for all nodes in the cluster
func (r *NodeOpReconciler) createJobs(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)

	// Get all nodes in the cluster
	nodes, err := r.getClusterNodes(ctx)
	if err != nil {
		return err
	}

	// Create a Job for each node
	for _, node := range nodes {
		if err := r.createNodeJob(ctx, nodeOp, node); err != nil {
			// Log the error but continue with other nodes
			log.Error(err, "Failed to create Job for node, continuing with other nodes",
				"node", node.Name)
			continue
		}
	}

	return nil
}

// updateNodeOpStatus updates the status of the NodeOp based on Job statuses
func (r *NodeOpReconciler) updateNodeOpStatus(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)

	// Initialize status if needed
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	// Check all node statuses to determine overall phase
	allCompleted := true
	anyFailed := false
	for nodeName, status := range nodeOp.Status.NodeStatuses {
		// Get the Job status
		job := &batchv1.Job{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: nodeOp.Namespace,
			Name:      status.JobName,
		}, job)
		if err != nil {
			if client.IgnoreNotFound(err) != nil {
				log.Error(err, "Failed to get Job status",
					"node", nodeName,
					"job", status.JobName)
				return err
			}
			// Job not found, mark as failed
			status.Phase = "Failed"
			status.Message = "Job not found"
			status.LastUpdated = metav1.Now()
			nodeOp.Status.NodeStatuses[nodeName] = status
			anyFailed = true
			continue
		}

		// Update node status based on Job status
		if job.Status.Failed > 0 {
			status.Phase = "Failed"
			status.Message = "Job failed"
			anyFailed = true
		} else if job.Status.Succeeded > 0 {
			status.Phase = "Completed"
			status.Message = "Job completed successfully"
		} else if job.Status.Active > 0 {
			status.Phase = "Running"
			status.Message = "Job is running"
			allCompleted = false
		} else {
			status.Phase = "Pending"
			status.Message = "Job is pending"
			allCompleted = false
		}
		status.LastUpdated = metav1.Now()
		nodeOp.Status.NodeStatuses[nodeName] = status
	}

	// Update overall phase
	if anyFailed {
		nodeOp.Status.Phase = "Failed"
	} else if allCompleted {
		nodeOp.Status.Phase = "Completed"
	} else {
		nodeOp.Status.Phase = "Running"
	}
	nodeOp.Status.LastUpdated = metav1.Now()

	// Update the NodeOp status
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOp status")
		return err
	}

	return nil
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *NodeOpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Get the NodeOp resource
	nodeOp, err := r.getNodeOp(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}
	if nodeOp == nil {
		// NodeOp was deleted, nothing to do
		return ctrl.Result{}, nil
	}

	// Check if Jobs already exist
	hasJobs, err := r.hasExistingJobs(ctx, nodeOp)
	if err != nil {
		return ctrl.Result{}, err
	}

	if hasJobs {
		// Update status of existing Jobs
		if err := r.updateNodeOpStatus(ctx, nodeOp); err != nil {
			log.Error(err, "Failed to update NodeOp status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// If we get here, no Jobs exist yet
	// Create the Jobs
	if err := r.createJobs(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to create Jobs")
		return ctrl.Result{}, err
	}

	// Update status after creating Jobs
	if err := r.updateNodeOpStatus(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOp status after Job creation")
		return ctrl.Result{}, err
	}

	// Return with requeue to ensure we pick up Job status changes quickly
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeOpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kairosiov1alpha1.NodeOp{}).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeOpsForJob),
		).
		Named("nodeop").
		Complete(r)
}

// findNodeOpsForJob finds NodeOps that own the given Job
func (r *NodeOpReconciler) findNodeOpsForJob(ctx context.Context, obj client.Object) []reconcile.Request {
	job := obj.(*batchv1.Job)
	log := logf.FromContext(ctx)

	// Get the NodeOp that owns this Job
	for _, ownerRef := range job.OwnerReferences {
		if ownerRef.Kind == "NodeOp" {
			log.Info("Job status changed, triggering NodeOp reconciliation",
				"job", job.Name,
				"nodeOp", ownerRef.Name,
				"namespace", job.Namespace)
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      ownerRef.Name,
						Namespace: job.Namespace,
					},
				},
			}
		}
	}

	log.Info("No NodeOp owner found for Job",
		"job", job.Name,
		"namespace", job.Namespace)
	return nil
}

func (r *NodeOpReconciler) getOperatorNamespace() string {
	// Get namespace from environment variable
	namespace := os.Getenv("OPERATOR_NAMESPACE")
	if namespace == "" {
		// Fallback to "system" if not set
		return "system"
	}
	return namespace
}
