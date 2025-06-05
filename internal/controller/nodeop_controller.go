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
	"sort"
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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	kindNodeOp     = "NodeOp"
	phasePending   = "Pending"
	phaseFailed    = "Failed"
	phaseRunning   = "Running"
	phaseCompleted = "Completed"
	// Environment variables for controller pod identification
	controllerPodNameEnv      = "CONTROLLER_POD_NAME"
	controllerPodNamespaceEnv = "CONTROLLER_POD_NAMESPACE"
	// Finalizer for cleaning up ClusterRoleBinding
	clusterRoleBindingFinalizer = "nodeop-reboot.kairos.io/clusterrolebinding"
	// Reboot status constants
	rebootStatusNotRequested = "not-requested"
	rebootStatusCancelled    = "cancelled"
	rebootStatusPending      = "pending"
	rebootStatusCompleted    = "completed"
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
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/reconcile
func (r *NodeOpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling NodeOp", "request", req)

	// Get the NodeOp resource
	nodeOp, err := r.getNodeOp(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}
	if nodeOp == nil {
		// NodeOp was deleted, nothing to do
		return ctrl.Result{}, nil
	}

	// Handle finalization
	deleted, err := r.handleDeletion(ctx, nodeOp)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deleted {
		return ctrl.Result{}, nil
	}

	// Update status based on existing jobs
	if err := r.updateNodeOpStatus(ctx, nodeOp); err != nil {
		if apierrors.IsConflict(err) {
			log.Info("NodeOp was modified, requeuing reconciliation")
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "Failed to update NodeOp status")
		return ctrl.Result{}, err
	}

	// Check if we should create more jobs
	if err := r.manageJobCreation(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to manage job creation")
		return ctrl.Result{}, err
	}

	// Return with requeue to ensure we still sync even if we miss some event
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeOpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Ensure cluster-wide RBAC resources are created when the controller starts
	if err := r.ensureClusterRBAC(context.Background()); err != nil {
		log := logf.Log.WithName("setup")
		log.Error(err, "Failed to ensure cluster RBAC")
		os.Exit(1)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kairosiov1alpha1.NodeOp{}).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeOpsForJob),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeOpsForRebootPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// Only watch pods with our reboot label
				pod := obj.(*corev1.Pod)
				_, hasRebootLabel := pod.Labels["kairos.io/reboot"]
				return hasRebootLabel
			})),
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
		APIVersion: "operator.kairos.io/v1alpha1",
		Kind:       "NodeOp",
	}

	return nodeOp, nil
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

		// Skip reboot pods - they need to stay alive to reboot the node after jobs complete
		if _, hasRebootLabel := pod.Labels["kairos.io/reboot"]; hasRebootLabel {
			log.Info("Skipping reboot pod during drain",
				"pod", pod.Name,
				"namespace", pod.Namespace)
			continue
		}

		// Skip DaemonSet pods if IgnoreDaemonSets is true
		if getBool(drainOptions.IgnoreDaemonSets, DrainIgnoreDaemonSetsDefault) {
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
		if !getBool(drainOptions.Force, DrainForceDefault) {
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

// createRebootJobSpec creates a JobSpec for nodes that will reboot after job completion
func (r *NodeOpReconciler) createRebootJobSpec(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, backoffLimit int32) batchv1.JobSpec {
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName: node.Name,
				InitContainers: []corev1.Container{
					{
						Name:    "nodeop",
						Image:   nodeOp.Spec.Image,
						Command: nodeOp.Spec.Command,
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "host-root",
								MountPath: "/host",
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "sentinel-creator",
						Image: "busybox:latest",
						Command: []string{
							"/bin/sh",
							"-c",
							"echo 'Job completed at $(date)' | tee /sentinel/$(JOB_NAME)-$(date +%s)",
						},
						Env: []corev1.EnvVar{
							{
								Name: "JOB_NAME",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "metadata.labels['batch.kubernetes.io/job-name']",
									},
								},
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "sentinel-volume",
								MountPath: "/sentinel",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "host-root",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/",
							},
						},
					},
					{
						Name: "sentinel-volume",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/usr/local/.kairos",
								Type: &[]corev1.HostPathType{corev1.HostPathDirectoryOrCreate}[0],
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		},
	}
}

// createStandardJobSpec creates a JobSpec for standard node operations without reboot
func (r *NodeOpReconciler) createStandardJobSpec(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, backoffLimit int32) batchv1.JobSpec {
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName: node.Name,
				Containers: []corev1.Container{
					{
						Name:    "nodeop",
						Image:   nodeOp.Spec.Image,
						Command: nodeOp.Spec.Command,
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "host-root",
								MountPath: "/host",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "host-root",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/",
							},
						},
					},
				},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		},
	}
}

// createNodeJob creates a Job for a specific node
func (r *NodeOpReconciler) createNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	log := logf.FromContext(ctx)

	// If cordoning is requested, cordon the node first
	if getBool(nodeOp.Spec.Cordon, CordonDefault) {
		if err := r.cordonNode(ctx, &node); err != nil {
			return err
		}

		// If draining is also requested, drain the node
		if nodeOp.Spec.DrainOptions != nil && getBool(nodeOp.Spec.DrainOptions.Enabled, DrainEnabledDefault) {
			if err := r.drainNode(ctx, &node, nodeOp.Spec.DrainOptions); err != nil {
				log.Error(err, "Failed to drain node", "node", node.Name)
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
	jobBaseName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit-6) // Leave room for suffix

	// Determine backoff limit - use NodeOp setting or default to Kubernetes default (6)
	backoffLimit := int32(6) // Kubernetes default
	if nodeOp.Spec.BackoffLimit != nil {
		backoffLimit = *nodeOp.Spec.BackoffLimit
	}

	// Create the appropriate JobSpec based on whether reboot is required
	var jobSpec batchv1.JobSpec
	if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		jobSpec = r.createRebootJobSpec(nodeOp, node, backoffLimit)
	} else {
		jobSpec = r.createStandardJobSpec(nodeOp, node, backoffLimit)
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: jobBaseName + "-",
			Namespace:    nodeOp.Namespace,
			Labels: map[string]string{
				"kairos.io/nodeop": nodeOp.Name,
				"kairos.io/node":   node.Name,
			},
		},
		Spec: jobSpec,
	}

	if err := controllerutil.SetControllerReference(nodeOp, job, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for Job")
		return err
	}

	if err := r.Create(ctx, job); err != nil {
		log.Error(err, "Failed to create Job for node",
			"nodeOp", nodeOp.Name,
			"node", node.Name)
		return err
	}

	// Get the actual job name that Kubernetes assigned
	actualJobName := job.Name

	// Initialize node status
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	// Determine initial reboot status
	rebootStatus := rebootStatusNotRequested
	if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		rebootStatus = rebootStatusPending
	}

	nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
		Phase:        phasePending,
		JobName:      actualJobName,
		Message:      "Job created",
		RebootStatus: rebootStatus,
		LastUpdated:  metav1.Now(),
	}

	// Update NodeOp status
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOp status after Job creation")
		return err
	}

	log.Info("Created Job for node",
		"nodeOp", nodeOp.Name,
		"node", node.Name,
		"job", actualJobName)

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
	anyFailed := false
	completedNodes := 0
	for nodeName, status := range nodeOp.Status.NodeStatuses {
		// Skip processing if the node status is already in a terminal state
		if status.Phase == phaseCompleted || status.Phase == phaseFailed {
			log.V(1).Info("Node status already in terminal state, skipping processing",
				"node", nodeName,
				"currentPhase", status.Phase)
			continue
		}

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
		if job.Status.Succeeded > 0 {
			var err error
			log.Info("Job succeeded", "node", nodeName, "job", job.Name)
			status, err = r.handleJobSuccess(ctx, nodeOp, nodeName, status)
			if err != nil {
				return err
			}
		} else if job.Status.Active > 0 {
			status.Phase = "Running"
			status.Message = "Job is running"
			// Keep existing reboot status during running phase
			if status.RebootStatus == "" {
				if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
					status.RebootStatus = rebootStatusPending
				} else {
					status.RebootStatus = rebootStatusNotRequested
				}
			}
		} else if job.Status.Failed > 0 {
			status.Phase = phaseFailed
			status.Message = "Job failed"
			anyFailed = true

			// Handle reboot-related cleanup and status based on whether reboot was originally requested
			if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
				status.RebootStatus = rebootStatusCancelled // Reboot was requested but cancelled due to job failure

				// Clean up reboot pod for failed job
				if err := r.cleanupRebootPodForNode(ctx, nodeOp, nodeName); err != nil {
					log.Error(err, "Failed to cleanup reboot pod for failed job", "node", nodeName)
					// Don't return error, just log it and continue
				}
			} else {
				status.RebootStatus = rebootStatusNotRequested // Reboot was never requested
			}
		} else {
			status.Phase = phasePending
			status.Message = "Job is pending"
			// Keep existing reboot status during pending phase
			if status.RebootStatus == "" {
				if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
					status.RebootStatus = rebootStatusPending
				} else {
					status.RebootStatus = rebootStatusNotRequested
				}
			}
		}
		status.LastUpdated = metav1.Now()
		nodeOp.Status.NodeStatuses[nodeName] = status

		// If RebootOnSuccess is true, we need to check both job and reboot pod status
		if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
			// Node is only completed when both job and reboot pod are completed
			if status.Phase == "Completed" && status.RebootStatus == "completed" {
				completedNodes++
			}
		} else {
			// If RebootOnSuccess is false, we only check job status
			if status.Phase == "Completed" {
				completedNodes++
			}
		}
	}

	// Update overall phase
	if anyFailed {
		nodeOp.Status.Phase = phaseFailed
	} else if len(nodeOp.Status.NodeStatuses) > 0 && completedNodes == len(nodeOp.Status.NodeStatuses) {
		nodeOp.Status.Phase = phaseCompleted
	} else {
		nodeOp.Status.Phase = phaseRunning
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

// findNodeOpsForRebootPod finds NodeOps that are associated with the given reboot pod
func (r *NodeOpReconciler) findNodeOpsForRebootPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod := obj.(*corev1.Pod)
	log := logf.FromContext(ctx)

	// Check if this is a reboot pod
	if nodeOpName, ok := pod.Labels["kairos.io/nodeop"]; ok {
		log.Info("Reboot pod status changed, triggering NodeOp reconciliation",
			"pod", pod.Name,
			"nodeOp", nodeOpName,
			"namespace", pod.Namespace)
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpName,
					Namespace: pod.Namespace,
				},
			},
		}
	}

	return nil
}

// ensureClusterRBAC creates the cluster-wide RBAC resources for the reboot pod
func (r *NodeOpReconciler) ensureClusterRBAC(ctx context.Context) error {
	log := logf.FromContext(ctx)

	// Create cluster role
	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeop-reboot",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "patch"},
			},
		},
	}
	if err := r.Create(ctx, clusterRole); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create cluster role")
			return err
		}
	}

	return nil
}

// ensureNodeOpServiceAccount creates the service account for a specific NodeOp
func (r *NodeOpReconciler) ensureNodeOpServiceAccount(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)

	// Create service account for this NodeOp
	saName := fmt.Sprintf("%s-reboot", nodeOp.Name)
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      saName,
			Namespace: nodeOp.Namespace,
		},
	}
	if err := controllerutil.SetControllerReference(nodeOp, sa, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for service account")
		return err
	}
	if err := r.Create(ctx, sa); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create service account")
			return err
		}
	}

	// Create cluster role binding for this NodeOp's service account
	crbName := fmt.Sprintf("nodeop-reboot-%s", nodeOp.Name)
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: crbName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: nodeOp.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "nodeop-reboot",
		},
	}
	if err := r.Create(ctx, clusterRoleBinding); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "Failed to create cluster role binding")
			return err
		}
	}

	// Add finalizer to NodeOp if not present
	if !controllerutil.ContainsFinalizer(nodeOp, clusterRoleBindingFinalizer) {
		controllerutil.AddFinalizer(nodeOp, clusterRoleBindingFinalizer)
		if err := r.Update(ctx, nodeOp); err != nil {
			log.Error(err, "Failed to add finalizer to NodeOp")
			return err
		}
	}

	return nil
}

// createRebootPod creates a reboot pod that waits for a sentinel file before rebooting
func (r *NodeOpReconciler) createRebootPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName, jobBaseName string) error {
	log := logf.FromContext(ctx)

	// Ensure service account for reboot pod
	if err := r.ensureNodeOpServiceAccount(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to ensure service account for reboot pod")
		return err
	}

	// Get the operator image from environment variable
	operatorImage := os.Getenv("OPERATOR_IMAGE")
	if operatorImage == "" {
		operatorImage = "quay.io/kairos/kairos-operator:latest"
	}

	rebootPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-reboot-", nodeOp.Name),
			Namespace:    nodeOp.Namespace,
			Labels: map[string]string{
				"kairos.io/nodeop": nodeOp.Name,
				"kairos.io/reboot": "true",
				"kairos.io/node":   nodeName,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			HostPID:  true,
			Containers: []corev1.Container{
				{
					Name:  "reboot",
					Image: operatorImage,
					Command: []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf(`
							echo "=== Checking for existing reboot annotation ==="
							EXISTING_ANNOTATION=$(kubectl get pod $POD_NAME --namespace $POD_NAMESPACE -o jsonpath='{.metadata.annotations.kairos\.io/reboot-state}' 2>/dev/null || echo "")
							if [ "$EXISTING_ANNOTATION" = "%s" ]; then
								echo "Reboot annotation already exists, reboot was already performed. Exiting successfully."
								exit 0
							fi
							echo "No reboot annotation found, proceeding with reboot process..."
							ls -la /sentinel/ || echo "Cannot list /sentinel directory"
							while true; do
								SENTINEL_FILE=$(find /sentinel -name "%s-*" -type f 2>/dev/null | head -1)
								if [ -n "$SENTINEL_FILE" ]; then
									echo "Found sentinel file: $SENTINEL_FILE"
									echo "Deleting sentinel file before reboot..."
									rm -f "$SENTINEL_FILE"
									echo "Attempting to patch pod..."
									kubectl patch pod $POD_NAME -p '{"metadata":{"annotations":{"kairos.io/reboot-state":"%s"}}}' --namespace $POD_NAMESPACE || echo "kubectl patch failed"
									echo "Giving 5 seconds to the Job Pod to exit gracefully..."
									sleep 5
									echo "Attempting reboot with nsenter..."
									nsenter -i -m -t 1 -- reboot || echo "nsenter reboot failed"
									break
								fi
								echo "No matching sentinel file found, sleeping..."
								sleep 10
							done
						`, rebootStatusCompleted, jobBaseName, rebootStatusCompleted),
					},
					SecurityContext: &corev1.SecurityContext{
						Privileged: asBool(true),
						RunAsUser:  &[]int64{0}[0],
					},
					Env: []corev1.EnvVar{
						{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.name",
								},
							},
						},
						{
							Name: "POD_NAMESPACE",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
								},
							},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "sentinel-volume",
							MountPath: "/sentinel",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "sentinel-volume",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/usr/local/.kairos",
							Type: &[]corev1.HostPathType{corev1.HostPathDirectory}[0],
						},
					},
				},
			},
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			ServiceAccountName: fmt.Sprintf("%s-reboot", nodeOp.Name),
		},
	}

	if err := controllerutil.SetControllerReference(nodeOp, rebootPod, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for reboot pod")
		return err
	}

	if err := r.Create(ctx, rebootPod); err != nil {
		log.Error(err, "Failed to create reboot pod", "node", nodeName)
		return err
	}

	log.Info("Created reboot pod", "node", nodeName, "pod", rebootPod.Name)
	return nil
}

// cleanupRebootPodForNode removes the reboot pod for a failed job
func (r *NodeOpReconciler) cleanupRebootPodForNode(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) error {
	log := logf.FromContext(ctx)

	// Get the reboot pod for the failed job
	podList := &corev1.PodList{}
	err := r.List(ctx, podList,
		client.InNamespace(nodeOp.Namespace),
		client.MatchingLabels(map[string]string{
			"kairos.io/nodeop": nodeOp.Name,
			"kairos.io/reboot": "true",
			"kairos.io/node":   nodeName,
		}),
	)
	if err != nil {
		log.Error(err, "Failed to list reboot pods", "node", nodeName)
		return err
	}

	if len(podList.Items) > 0 {
		pod := podList.Items[0]
		log.Info("Deleting reboot pod for failed job", "node", nodeName, "pod", pod.Name)
		if err := r.Delete(ctx, &pod); err != nil {
			log.Error(err, "Failed to delete reboot pod for failed job", "node", nodeName)
			return err
		}
	}

	return nil
}

// isRebootPodCompleted checks if the reboot pod for a node is completed
func (r *NodeOpReconciler) isRebootPodCompleted(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) (bool, error) {
	log := logf.FromContext(ctx)

	// Get the reboot pod for the node
	podList := &corev1.PodList{}
	err := r.List(ctx, podList,
		client.InNamespace(nodeOp.Namespace),
		client.MatchingLabels(map[string]string{
			"kairos.io/nodeop": nodeOp.Name,
			"kairos.io/reboot": "true",
			"kairos.io/node":   nodeName,
		}),
	)
	if err != nil {
		log.Error(err, "Failed to list reboot pods", "node", nodeName)
		return false, err
	}

	if len(podList.Items) > 0 {
		pod := podList.Items[0]
		// Check if pod has succeeded AND has the reboot completion annotation
		if pod.Status.Phase == corev1.PodSucceeded {
			if rebootState, exists := pod.Annotations["kairos.io/reboot-state"]; exists && rebootState == rebootStatusCompleted {
				return true, nil
			}
		}
	}

	return false, nil
}

// handleDeletion handles the finalization process when a NodeOp is being deleted
func (r *NodeOpReconciler) handleDeletion(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) (bool, error) {
	if nodeOp.DeletionTimestamp.IsZero() {
		return false, nil
	}

	log := logf.FromContext(ctx)

	if controllerutil.ContainsFinalizer(nodeOp, clusterRoleBindingFinalizer) {
		// Delete the ClusterRoleBinding
		crbName := fmt.Sprintf("nodeop-reboot-%s", nodeOp.Name)
		clusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: crbName,
			},
		}
		if err := r.Delete(ctx, clusterRoleBinding); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to delete ClusterRoleBinding")
				return false, err
			}
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(nodeOp, clusterRoleBindingFinalizer)
		if err := r.Update(ctx, nodeOp); err != nil {
			log.Error(err, "Failed to remove finalizer from NodeOp")
			return false, err
		}
	}
	return true, nil
}

// handleJobSuccess handles the logic when a job completes successfully
func (r *NodeOpReconciler) handleJobSuccess(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string, status kairosiov1alpha1.NodeStatus) (kairosiov1alpha1.NodeStatus, error) {
	log := logf.FromContext(ctx)

	status.Phase = phaseCompleted
	status.Message = "Job completed successfully"

	// Set reboot status based on whether reboot was requested
	if !getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		status.RebootStatus = rebootStatusNotRequested
	} else {
		// If reboot is requested, check if reboot pod is completed
		rebootCompleted, err := r.isRebootPodCompleted(ctx, nodeOp, nodeName)
		if err != nil {
			log.Error(err, "Failed to check reboot pod status", "node", nodeName)
			return status, err
		}
		if rebootCompleted {
			status.RebootStatus = rebootStatusCompleted
		} else {
			status.RebootStatus = rebootStatusPending
		}
	}

	// If the node was cordoned, check if we should uncordon it
	if getBool(nodeOp.Spec.Cordon, CordonDefault) {
		shouldUncordon := false

		if !getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
			// If reboot is not requested, uncordon immediately when job completes
			shouldUncordon = true
		} else {
			// If reboot is requested, only uncordon when both job and reboot pod are completed
			if status.RebootStatus == rebootStatusCompleted {
				shouldUncordon = true
				status.Message = "Job and reboot completed successfully"
			} else {
				status.Message = "Job completed successfully, waiting for reboot"
			}
		}

		if shouldUncordon {
			node := &corev1.Node{}
			if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
				log.Error(err, "Failed to get node for uncordoning", "node", nodeName)
				return status, err
			}
			if err := r.uncordonNode(ctx, node); err != nil {
				log.Error(err, "Failed to uncordon node after completion", "node", nodeName)
				return status, err
			}
		}
	}

	return status, nil
}

// isMasterNode returns true if the node has a master or control-plane label
func isMasterNode(n corev1.Node) bool {
	_, master := n.Labels["node-role.kubernetes.io/master"]
	_, controlPlane := n.Labels["node-role.kubernetes.io/control-plane"]
	return master || controlPlane
}

// getTargetNodes returns the list of nodes that should run the operation
func (r *NodeOpReconciler) getTargetNodes(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) ([]corev1.Node, error) {
	log := logf.FromContext(ctx)

	// Get all nodes in the cluster
	allNodes, err := r.getClusterNodes(ctx)
	if err != nil {
		return nil, err
	}

	// If no node selector specified, return all nodes
	if nodeOp.Spec.NodeSelector == nil {
		log.V(1).Info("No node selector specified, targeting all nodes", "nodeCount", len(allNodes))
		return allNodes, nil
	}

	// Convert label selector to a selector
	selector, err := metav1.LabelSelectorAsSelector(nodeOp.Spec.NodeSelector)
	if err != nil {
		log.Error(err, "Failed to convert NodeSelector to selector")
		return nil, err
	}

	// Filter nodes based on label selector
	var targetNodes []corev1.Node
	for _, node := range allNodes {
		if selector.Matches(labels.Set(node.Labels)) {
			targetNodes = append(targetNodes, node)
		}
	}

	// Sort nodes: masters first (by label 'node-role.kubernetes.io/master' or 'node-role.kubernetes.io/control-plane')
	sortedNodes := make([]corev1.Node, len(targetNodes))
	copy(sortedNodes, targetNodes)
	sort.SliceStable(sortedNodes, func(i, j int) bool {
		return isMasterNode(sortedNodes[i]) && !isMasterNode(sortedNodes[j])
	})

	log.Info("Found target nodes using selector",
		"selector", selector.String(),
		"targetNodeCount", len(sortedNodes),
		"totalNodeCount", len(allNodes))

	return sortedNodes, nil
}

// manageJobCreation manages the creation of jobs based on concurrency and stop-on-failure settings
func (r *NodeOpReconciler) manageJobCreation(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)

	// Get target nodes
	targetNodes, err := r.getTargetNodes(ctx, nodeOp)
	if err != nil {
		return err
	}

	// Initialize status if needed
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	// Check if we should stop creating new jobs due to failures
	if getBool(nodeOp.Spec.StopOnFailure, StopOnFailureDefault) && r.hasFailedJobs(nodeOp) {
		log.Info("Stopping job creation due to failure and StopOnFailure=true")
		return nil
	}

	// Count currently running jobs
	runningCount := r.countRunningJobs(nodeOp)

	// Determine how many new jobs we can start
	var maxConcurrency int32
	if nodeOp.Spec.Concurrency == 0 {
		// 0 means unlimited concurrency
		maxConcurrency = int32(len(targetNodes))
	} else {
		maxConcurrency = nodeOp.Spec.Concurrency
	}

	availableSlots := maxConcurrency - runningCount
	if availableSlots <= 0 {
		log.V(1).Info("No available slots for new jobs",
			"nodeOp", nodeOp.Name,
			"running", runningCount,
			"maxConcurrency", maxConcurrency)
		return nil
	}

	// Find nodes that don't have jobs yet
	var nodesNeedingJobs []corev1.Node
	for _, node := range targetNodes {
		if _, exists := nodeOp.Status.NodeStatuses[node.Name]; !exists {
			nodesNeedingJobs = append(nodesNeedingJobs, node)
		}
	}

	if len(nodesNeedingJobs) == 0 {
		log.V(1).Info("All target nodes already have jobs", "nodeOp", nodeOp.Name)
		return nil
	}

	// Create jobs up to the available slots
	jobsToCreate := int(availableSlots)
	if jobsToCreate > len(nodesNeedingJobs) {
		jobsToCreate = len(nodesNeedingJobs)
	}

	log.Info("Creating jobs for nodes",
		"nodeOp", nodeOp.Name,
		"jobsToCreate", jobsToCreate,
		"availableSlots", availableSlots,
		"nodesNeedingJobs", len(nodesNeedingJobs))

	// Create reboot pods first if needed
	if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		for i := 0; i < jobsToCreate; i++ {
			node := nodesNeedingJobs[i]
			fullName := fmt.Sprintf("%s-%s", nodeOp.Name, node.Name)
			jobBaseName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit-6)

			if err := r.createRebootPod(ctx, nodeOp, node.Name, jobBaseName); err != nil {
				log.Error(err, "Failed to create reboot pod for node", "node", node.Name)
				// Continue with other nodes
			}
		}
	}

	// Create jobs for the selected nodes
	for i := 0; i < jobsToCreate; i++ {
		node := nodesNeedingJobs[i]
		if err := r.createNodeJob(ctx, nodeOp, node); err != nil {
			log.Error(err, "Failed to create Job for node, continuing with other nodes", "node", node.Name)
			continue
		}
	}

	return nil
}

// hasFailedJobs checks if any jobs have failed
func (r *NodeOpReconciler) hasFailedJobs(nodeOp *kairosiov1alpha1.NodeOp) bool {
	for _, status := range nodeOp.Status.NodeStatuses {
		if status.Phase == phaseFailed {
			return true
		}
	}
	return false
}

// countRunningJobs counts the number of currently running or pending jobs
func (r *NodeOpReconciler) countRunningJobs(nodeOp *kairosiov1alpha1.NodeOp) int32 {
	var count int32
	for _, status := range nodeOp.Status.NodeStatuses {
		// Conditions where we consider a job "running" or "busy":
		// 1. It's in Pending or Running phase, OR
		// 2. It's Completed but reboot is still pending (node is busy rebooting)
		if status.Phase == phasePending || status.Phase == phaseRunning ||
			(status.Phase == phaseCompleted && status.RebootStatus == rebootStatusPending) {
			count++
		}
	}
	return count
}
