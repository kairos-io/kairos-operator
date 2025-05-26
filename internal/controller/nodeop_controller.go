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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	kindNodeOp  = "NodeOp"
	phaseFailed = "Failed"
	// Environment variables for controller pod identification
	controllerPodNameEnv      = "CONTROLLER_POD_NAME"
	controllerPodNamespaceEnv = "CONTROLLER_POD_NAMESPACE"
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
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete

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
		log.Info("Jobs already exist for NodeOp", "nodeOp", nodeOp.Name)
		// Update status of existing Jobs
		if err := r.updateNodeOpStatus(ctx, nodeOp); err != nil {
			if apierrors.IsConflict(err) {
				log.Info("NodeOp was modified, requeuing reconciliation")
				return ctrl.Result{Requeue: true}, nil
			}
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
		if apierrors.IsConflict(err) {
			log.Info("NodeOp was modified, requeuing reconciliation")
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "Failed to update NodeOp status after Job creation")
		return ctrl.Result{}, err
	}

	// Return with requeue to ensure we still sync even if we miss some event
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
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

	jobList := &batchv1.JobList{}
	err := r.List(ctx, jobList, client.InNamespace(nodeOp.Namespace))
	if err != nil {
		log.Error(err, "Failed to list Jobs")
		return false, err
	}

	for _, job := range jobList.Items {
		for _, ownerRef := range job.OwnerReferences {
			if ownerRef.Kind == kindNodeOp && ownerRef.Name == nodeOp.Name {
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

// cordonNode marks a node as unschedulable
func (r *NodeOpReconciler) cordonNode(ctx context.Context, node *corev1.Node) error {
	log := logf.FromContext(ctx)

	// Get the latest version of the node
	latestNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, latestNode); err != nil {
		log.Error(err, "Failed to get latest node state", "node", node.Name)
		return err
	}

	if latestNode.Spec.Unschedulable {
		log.Info("Node is already cordoned", "node", node.Name)
		return nil
	}

	// Update the node with the latest resource version
	latestNode.Spec.Unschedulable = true
	if err := r.Update(ctx, latestNode); err != nil {
		log.Error(err, "Failed to cordon node", "node", node.Name)
		return err
	}

	log.Info("Successfully cordoned node", "node", node.Name)
	return nil
}

// uncordonNode marks a node as schedulable
func (r *NodeOpReconciler) uncordonNode(ctx context.Context, node *corev1.Node) error {
	log := logf.FromContext(ctx)

	// Get the latest version of the node
	latestNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, latestNode); err != nil {
		log.Error(err, "Failed to get latest node state", "node", node.Name)
		return err
	}

	if !latestNode.Spec.Unschedulable {
		log.Info("Node is already uncordoned", "node", node.Name)
		return nil
	}

	// Update the node with the latest resource version
	latestNode.Spec.Unschedulable = false
	if err := r.Update(ctx, latestNode); err != nil {
		log.Error(err, "Failed to uncordon node", "node", node.Name)
		return err
	}

	log.Info("Successfully uncordoned node", "node", node.Name)
	return nil
}

// drainNode evicts all pods from a node
func (r *NodeOpReconciler) drainNode(ctx context.Context, node *corev1.Node, drainOptions *kairosiov1alpha1.DrainOptions) error {
	log := logf.FromContext(ctx)

	// Get controller pod information from environment variables
	controllerPodName := os.Getenv(controllerPodNameEnv)
	controllerPodNamespace := os.Getenv(controllerPodNamespaceEnv)

	// List all pods in all namespaces
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList); err != nil {
		log.Error(err, "Failed to list pods", "node", node.Name)
		return err
	}

	// Filter pods that are on this node
	for _, pod := range podList.Items {
		// Skip pods that are not on this node
		if pod.Spec.NodeName != node.Name {
			continue
		}

		// Skip pods that are already terminating
		if pod.DeletionTimestamp != nil {
			continue
		}

		// Skip pods that are part of the kube-system namespace
		if pod.Namespace == "kube-system" {
			continue
		}

		// Skip the controller's own pod if environment variables are set
		if controllerPodName != "" && controllerPodNamespace != "" {
			if pod.Name == controllerPodName && pod.Namespace == controllerPodNamespace {
				log.Info("Skipping controller pod during drain",
					"pod", pod.Name,
					"namespace", pod.Namespace)
				continue
			}
		}

		// Skip DaemonSet pods if IgnoreDaemonSets is true
		if drainOptions.IgnoreDaemonSets {
			isDaemonSet := false
			for _, ownerRef := range pod.OwnerReferences {
				if ownerRef.Kind == "DaemonSet" {
					isDaemonSet = true
					break
				}
			}
			if isDaemonSet {
				continue
			}
		}

		// Skip pods without a controller if Force is false
		if !drainOptions.Force {
			hasController := false
			for _, ownerRef := range pod.OwnerReferences {
				if ownerRef.Controller != nil && *ownerRef.Controller {
					hasController = true
					break
				}
			}
			if !hasController {
				continue
			}
		}

		// Set deletion grace period if specified
		if drainOptions.GracePeriodSeconds != nil {
			gracePeriod := int64(*drainOptions.GracePeriodSeconds)
			if gracePeriod >= 0 {
				pod.DeletionGracePeriodSeconds = &gracePeriod
			}
		}

		// Delete the pod
		if err := r.Delete(ctx, &pod); err != nil {
			log.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
			return err
		}
	}

	log.Info("Successfully drained node", "node", node.Name)
	return nil
}

// createNodeJob creates a Job for a specific node
func (r *NodeOpReconciler) createNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	log := logf.FromContext(ctx)

	// If cordoning is requested, cordon the node first
	if nodeOp.Spec.Cordon {
		if err := r.cordonNode(ctx, &node); err != nil {
			return err
		}

		// If draining is also requested, drain the node
		if nodeOp.Spec.DrainOptions != nil && nodeOp.Spec.DrainOptions.Enabled {
			if err := r.drainNode(ctx, &node, nodeOp.Spec.DrainOptions); err != nil {
				// If draining fails, uncordon the node
				if uncordonErr := r.uncordonNode(ctx, &node); uncordonErr != nil {
					log.Error(uncordonErr, "Failed to uncordon node after drain failure", "node", node.Name)
				}
				return err
			}
		}
	}

	// Create a full name combining all information
	fullName := fmt.Sprintf("%s-%s", nodeOp.Name, node.Name)
	jobName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: nodeOp.Namespace,
			Labels: map[string]string{
				"kairos.io/nodeop": nodeOp.Name,
				"kairos.io/node":   node.Name,
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
					NodeName: node.Name,
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
			status.Phase = phaseFailed
			status.Message = "Job not found"
			status.LastUpdated = metav1.Now()
			nodeOp.Status.NodeStatuses[nodeName] = status
			anyFailed = true
			continue
		}

		// Update node status based on Job status
		if job.Status.Failed > 0 {
			status.Phase = phaseFailed
			status.Message = "Job failed"
			anyFailed = true
		} else if job.Status.Succeeded > 0 {
			status.Phase = "Completed"
			status.Message = "Job completed successfully"

			// If the job completed successfully and the node was cordoned, uncordon it
			if nodeOp.Spec.Cordon {
				node := &corev1.Node{}
				if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
					log.Error(err, "Failed to get node for uncordoning", "node", nodeName)
					return err
				}
				if err := r.uncordonNode(ctx, node); err != nil {
					log.Error(err, "Failed to uncordon node after job completion", "node", nodeName)
					return err
				}
			}
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
		nodeOp.Status.Phase = phaseFailed
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

// findNodeOpsForJob finds NodeOps that own the given Job
func (r *NodeOpReconciler) findNodeOpsForJob(ctx context.Context, obj client.Object) []reconcile.Request {
	job := obj.(*batchv1.Job)
	log := logf.FromContext(ctx)

	// Get the NodeOp that owns this Job
	for _, ownerRef := range job.OwnerReferences {
		if ownerRef.Kind == kindNodeOp {
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
