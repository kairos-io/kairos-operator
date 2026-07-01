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
	"k8s.io/apimachinery/pkg/util/rand"
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
	// Annotation marking a node as cordoned by a specific NodeOp.
	// Value is "<namespace>/<name>@<uid>" of the NodeOp that flipped the node to
	// unschedulable; including the UID prevents a recreated NodeOp with the same
	// name from matching a stale annotation. uncordonNode only acts when this
	// annotation matches; nodes cordoned by a human or other tooling are left alone.
	//
	// The "operator.kairos.io/" prefix mirrors the NodeOp CRD's API group, since
	// this annotation is controller-internal ownership state (not a Kairos-wide
	// semantic label like "kairos.io/managed"). Other Kairos components are not
	// expected to read it.
	cordonedByAnnotation = "operator.kairos.io/cordoned-by"
	// Reboot status constants
	rebootStatusNotRequested = "not-requested"
	rebootStatusCancelled    = "cancelled"
	rebootStatusPending      = "pending"
	rebootStatusCompleted    = "completed"
	// Label keys used on Jobs and Pods created by the NodeOp controller
	labelKeyNodeOp    = "kairos.io/nodeop"
	labelKeyNode      = "kairos.io/node"
	labelKeyReboot    = "kairos.io/reboot"
	labelKeyPreflight = "kairos.io/preflight"
	// Phase set on a node while its preflight Pod is in flight.
	phasePreflight = "Preflight"
	// preflightContainerName is the single container name inside every preflight Pod.
	preflightContainerName = "preflight"
	// preflightDefaultActiveDeadlineSeconds bounds total preflight Pod lifetime
	// (used as a controller-side fallback when Spec.Preflight.ActiveDeadlineSeconds
	// is nil; the CRD's kubebuilder:default applies the same value at admission).
	preflightDefaultActiveDeadlineSeconds = int64(120)
	// Volume constants
	hostRootVolumeName = "host-root"
	sentinelVolumeName = "sentinel-volume"
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
				_, hasRebootLabel := pod.Labels[labelKeyReboot]
				return hasRebootLabel
			})),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeOpsForPreflightPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// Only watch Pods we created as part of a preflight check.
				pod := obj.(*corev1.Pod)
				_, hasPreflightLabel := pod.Labels[labelKeyPreflight]
				return hasPreflightLabel
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
		Kind:       kindNodeOp,
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

// nodeOpOwnerRef returns the annotation value used to mark a node as cordoned
// by the given NodeOp. The UID is included so that a NodeOp deleted and
// recreated with the same namespace/name does not match a stale annotation
// left behind by the previous instance.
func nodeOpOwnerRef(nodeOp *kairosiov1alpha1.NodeOp) string {
	return fmt.Sprintf("%s/%s@%s", nodeOp.Namespace, nodeOp.Name, nodeOp.UID)
}

// cordonNode marks a node as unschedulable and records that this NodeOp owns
// the cordon (via the cordonedByAnnotation). If the node is already cordoned
// by something else (a human, another controller), the annotation is NOT set,
// so a later uncordonNode call will correctly leave the node alone.
func (r *NodeOpReconciler) cordonNode(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node *corev1.Node) error {
	log := logf.FromContext(ctx)

	latestNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, latestNode); err != nil {
		log.Error(err, "Failed to get latest node state", "node", node.Name)
		return err
	}

	if latestNode.Spec.Unschedulable {
		owner, hasAnn := latestNode.Annotations[cordonedByAnnotation]
		switch {
		case !hasAnn:
			log.Info("Node is already cordoned by something outside this operator; not claiming ownership",
				"node", node.Name)
		case owner == nodeOpOwnerRef(nodeOp):
			log.Info("Node is already cordoned by this NodeOp", "node", node.Name, "nodeOp", owner)
		default:
			log.Info("Node is already cordoned by a different NodeOp; not claiming ownership",
				"node", node.Name, "cordonedBy", owner, "thisNodeOp", nodeOpOwnerRef(nodeOp))
		}
		return nil
	}

	latestNode.Spec.Unschedulable = true
	if latestNode.Annotations == nil {
		latestNode.Annotations = map[string]string{}
	}
	latestNode.Annotations[cordonedByAnnotation] = nodeOpOwnerRef(nodeOp)

	// Optimistic-concurrency Update: if the node changed between Get and Update
	// (e.g. another reconciler patched a label, or a human ran kubectl cordon),
	// this returns a Conflict error which propagates up to Reconcile and triggers
	// a requeue. The next reconcile re-reads the latest node state and retries.
	if err := r.Update(ctx, latestNode); err != nil {
		log.Error(err, "Failed to cordon node", "node", node.Name)
		return err
	}

	log.Info("Successfully cordoned node", "node", node.Name, "nodeOp", nodeOpOwnerRef(nodeOp))
	return nil
}

// uncordonNode marks a node as schedulable, but only if this NodeOp is the one
// that cordoned it (as recorded by cordonedByAnnotation). Nodes that the
// operator did not cordon — including nodes that were already cordoned when
// the NodeOp started, and nodes manually re-cordoned after the operator
// uncordoned them once — are left alone.
func (r *NodeOpReconciler) uncordonNode(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node *corev1.Node) error {
	log := logf.FromContext(ctx)

	latestNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name}, latestNode); err != nil {
		log.Error(err, "Failed to get latest node state", "node", node.Name)
		return err
	}

	if !latestNode.Spec.Unschedulable {
		// Clean up a stale annotation if the node was uncordoned out-of-band.
		// Conflicts on this Update are handled the same way as below: the error
		// propagates to Reconcile, which requeues.
		if _, hasAnn := latestNode.Annotations[cordonedByAnnotation]; hasAnn {
			delete(latestNode.Annotations, cordonedByAnnotation)
			if err := r.Update(ctx, latestNode); err != nil {
				log.Error(err, "Failed to clear stale cordoned-by annotation", "node", node.Name)
				return err
			}
		}
		return nil
	}

	owner, hasAnn := latestNode.Annotations[cordonedByAnnotation]
	if !hasAnn {
		log.Info("Node is cordoned but not by this operator; leaving it alone", "node", node.Name)
		return nil
	}
	if owner != nodeOpOwnerRef(nodeOp) {
		log.Info("Node was cordoned by a different NodeOp; leaving it alone",
			"node", node.Name, "cordonedBy", owner, "thisNodeOp", nodeOpOwnerRef(nodeOp))
		return nil
	}

	latestNode.Spec.Unschedulable = false
	delete(latestNode.Annotations, cordonedByAnnotation)
	// Same optimistic-concurrency pattern as cordonNode: a Conflict here means
	// the node was modified between Get and Update, and the error propagates up
	// so Reconcile can requeue and re-evaluate.
	if err := r.Update(ctx, latestNode); err != nil {
		log.Error(err, "Failed to uncordon node", "node", node.Name)
		return err
	}

	log.Info("Successfully uncordoned node", "node", node.Name, "nodeOp", nodeOpOwnerRef(nodeOp))
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
		if _, hasRebootLabel := pod.Labels[labelKeyReboot]; hasRebootLabel {
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

// getNodeOpImage returns the image to use for NodeOp containers.
// Priority: NodeOp Spec.Image > NODEOP_DEFAULT_IMAGE env var > "busybox:latest".
func getNodeOpImage(nodeOp *kairosiov1alpha1.NodeOp) string {
	if nodeOp.Spec.Image != "" {
		return nodeOp.Spec.Image
	}
	if img := os.Getenv("NODEOP_DEFAULT_IMAGE"); img != "" {
		return img
	}
	return "busybox:latest"
}

// getSentinelImage returns the image for the sentinel creator container.
// Priority: SENTINEL_IMAGE env var > NodeOp Spec.Image > "busybox:latest".
func getSentinelImage(nodeOp *kairosiov1alpha1.NodeOp) string {
	if img := os.Getenv("SENTINEL_IMAGE"); img != "" {
		return img
	}
	if nodeOp.Spec.Image != "" {
		return nodeOp.Spec.Image
	}
	return "busybox:latest"
}

// createRebootJobSpec creates a JobSpec for nodes that will reboot after job completion
func (r *NodeOpReconciler) createRebootJobSpec(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, backoffLimit int32) batchv1.JobSpec {
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName:         node.Name,
				ImagePullSecrets: nodeOp.Spec.ImagePullSecrets,
				InitContainers: []corev1.Container{
					{
						Name:    "nodeop",
						Image:   getNodeOpImage(nodeOp),
						Command: nodeOp.Spec.Command,
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      hostRootVolumeName,
								MountPath: "/host", //nolint:goconst // FHS default mount; not worth a constant
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  "sentinel-creator",
						Image: getSentinelImage(nodeOp),
						Command: []string{
							"/bin/sh", //nolint:goconst // path literal; not worth a constant
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
								Name:      sentinelVolumeName,
								MountPath: "/sentinel",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: hostRootVolumeName,
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/",
							},
						},
					},
					{
						Name: sentinelVolumeName,
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
				NodeName:         node.Name,
				ImagePullSecrets: nodeOp.Spec.ImagePullSecrets,
				Containers: []corev1.Container{
					{
						Name:    "nodeop",
						Image:   getNodeOpImage(nodeOp),
						Command: nodeOp.Spec.Command,
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      hostRootVolumeName,
								MountPath: "/host", //nolint:goconst // FHS default mount; not worth a constant
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: hostRootVolumeName,
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
func (r *NodeOpReconciler) createNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, jobName string) error {
	log := logf.FromContext(ctx)

	// If cordoning is requested, cordon the node first
	if getBool(nodeOp.Spec.Cordon, CordonDefault) {
		if err := r.cordonNode(ctx, nodeOp, &node); err != nil {
			return err
		}

		// If draining is also requested, drain the node
		if nodeOp.Spec.DrainOptions != nil && getBool(nodeOp.Spec.DrainOptions.Enabled, DrainEnabledDefault) {
			if err := r.drainNode(ctx, &node, nodeOp.Spec.DrainOptions); err != nil {
				log.Error(err, "Failed to drain node", "node", node.Name)
				// If draining fails, uncordon the node
				if uncordonErr := r.uncordonNode(ctx, nodeOp, &node); uncordonErr != nil {
					log.Error(uncordonErr, "Failed to uncordon node after drain failure", "node", node.Name)
				}
				return err
			}
		}
	}

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
			Name:      jobName,
			Namespace: nodeOp.Namespace,
			Labels: map[string]string{
				labelKeyNodeOp: nodeOp.Name,
				labelKeyNode:   node.Name,
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
	var err error
	for nodeName, status := range nodeOp.Status.NodeStatuses {
		// Nodes still in the Preflight phase have no Job yet; their state is
		// driven by manageJobCreation via the preflight Pod.
		if status.Phase == phasePreflight {
			continue
		}
		// Skipped-by-preflight nodes are terminal: Phase=Completed, no Job,
		// no reboot. Don't run them through the Job/reboot processors.
		if status.Phase == phaseCompleted && status.JobName == "" {
			completedNodes++
			continue
		}

		status, err = r.processJobStatus(ctx, nodeOp, nodeName, status)
		if err != nil {
			return err
		}

		// Process reboot status
		status, err = r.processRebootStatus(ctx, nodeOp, nodeName, status)
		if err != nil {
			return err
		}

		// Update the status map with any changes
		nodeOp.Status.NodeStatuses[nodeName] = status

		// Handle uncordoning for completed nodes
		if status.Phase == phaseCompleted && getBool(nodeOp.Spec.Cordon, CordonDefault) {
			rebootOnSuccess := getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault)

			if (rebootOnSuccess && status.RebootStatus == rebootStatusCompleted) || !rebootOnSuccess {
				node := &corev1.Node{}
				if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
					log.Error(err, "Failed to get node for uncordoning after reboot completion", "node", nodeName)
					return err
				}
				if err := r.uncordonNode(ctx, nodeOp, node); err != nil {
					log.Error(err, "Failed to uncordon node after reboot completion", "node", nodeName)
					return err
				}
			}
		}

		// Count failed jobs
		if status.Phase == phaseFailed {
			anyFailed = true

			// Uncordon the node when the operation failed, if opted in. uncordonNode
			// only acts on nodes this NodeOp cordoned, so out-of-band cordons are
			// left alone. Failed jobs do not reboot, so it is safe to uncordon now.
			if getBool(nodeOp.Spec.Cordon, CordonDefault) &&
				getBool(nodeOp.Spec.UncordonOnFailure, UncordonOnFailureDefault) {
				node := &corev1.Node{}
				if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
					log.Error(err, "Failed to get node for uncordoning after job failure", "node", nodeName)
					return err
				}
				if err := r.uncordonNode(ctx, nodeOp, node); err != nil {
					log.Error(err, "Failed to uncordon node after job failure", "node", nodeName)
					return err
				}
			}
		}

		// Count completed nodes based on whether reboot is required
		if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
			// Node is only completed when both job and reboot pod are completed
			if status.Phase == phaseCompleted && status.RebootStatus == rebootStatusCompleted {
				completedNodes++
			}
		} else {
			// If RebootOnSuccess is false, we only check job status
			if status.Phase == phaseCompleted {
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

// processJobStatus processes the job status for a specific node
func (r *NodeOpReconciler) processJobStatus(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string, status kairosiov1alpha1.NodeStatus) (kairosiov1alpha1.NodeStatus, error) {
	log := logf.FromContext(ctx)

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
			return status, err
		}
		// Job not found, mark as failed
		status.Phase = phaseFailed
		status.Message = "Job not found"
		status.LastUpdated = metav1.Now()
		return status, nil
	}

	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			switch condition.Type {
			case batchv1.JobFailed, batchv1.JobFailureTarget:
				status.Phase = phaseFailed
				status.Message = "Job failed"
				status.LastUpdated = metav1.Now()
				return status, nil
			case batchv1.JobSuccessCriteriaMet, batchv1.JobComplete:
				status.Phase = phaseCompleted
				status.Message = "Job completed successfully"
				status.LastUpdated = metav1.Now()
				return status, nil
			}
		}
	}

	status.Phase = phaseRunning
	status.Message = "Job is running"
	status.LastUpdated = metav1.Now()

	return status, nil
}

// processRebootStatus processes the reboot status for a specific node
func (r *NodeOpReconciler) processRebootStatus(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string, status kairosiov1alpha1.NodeStatus) (kairosiov1alpha1.NodeStatus, error) {
	log := logf.FromContext(ctx)

	// Initialize reboot status if empty based on NodeOp configuration
	if status.RebootStatus == "" {
		if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
			status.RebootStatus = rebootStatusPending
		} else {
			status.RebootStatus = rebootStatusNotRequested
		}
		status.LastUpdated = metav1.Now()
		return status, nil
	}

	// Handle reboot cleanup for failed or missing jobs
	if status.Phase == phaseFailed && getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		if status.RebootStatus != rebootStatusCancelled {
			status.RebootStatus = rebootStatusCancelled
			status.LastUpdated = metav1.Now()

			// Clean up reboot pod for failed job
			if err := r.cleanupRebootPodForNode(ctx, nodeOp, nodeName); err != nil {
				log.Error(err, "Failed to cleanup reboot pod for failed job", "node", nodeName)
				// Don't return error, just log it and continue
			}
		}
		return status, nil
	}

	// Process reboot completion for successfully completed jobs
	if status.Phase == phaseCompleted && status.RebootStatus == rebootStatusPending {
		// Check if reboot pod is completed
		rebootCompleted, err := r.isRebootPodCompleted(ctx, nodeOp, nodeName)
		if err != nil {
			log.Error(err, "Failed to check reboot pod status", "node", nodeName)
			return status, err
		}

		if rebootCompleted {
			status.RebootStatus = rebootStatusCompleted
			status.Message = "Job and reboot completed successfully"
			status.LastUpdated = metav1.Now()
		}
	}

	return status, nil
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

// findNodeOpsForPreflightPod enqueues the owning NodeOp when a preflight Pod's
// status changes. Without this, the NodeOp would only re-reconcile on its
// 5-minute fallback requeue, leaving a node stuck in Phase=Preflight long
// after its preflight Pod has terminated.
func (r *NodeOpReconciler) findNodeOpsForPreflightPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod := obj.(*corev1.Pod)
	log := logf.FromContext(ctx)

	nodeOpName, ok := pod.Labels[labelKeyNodeOp]
	if !ok {
		return nil
	}
	log.Info("Preflight pod status changed, triggering NodeOp reconciliation",
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

// findNodeOpsForRebootPod finds NodeOps that are associated with the given reboot pod
func (r *NodeOpReconciler) findNodeOpsForRebootPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod := obj.(*corev1.Pod)
	log := logf.FromContext(ctx)

	// Check if this is a reboot pod
	if nodeOpName, ok := pod.Labels[labelKeyNodeOp]; ok {
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
// createRebootPod schedules a long-lived Pod on nodeName whose only job is to
// watch /usr/local/.kairos for a sentinel file written by the upgrade Job's
// sentinel-creator container, then reboot the host. We pass the upgrade Job's
// full name (not just the jobBaseName prefix) so the watch pattern
// "<jobName>-*" matches exactly one Job's sentinel — see startMainJob for the
// rationale.
func (r *NodeOpReconciler) createRebootPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName, jobName string) error {
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

	// Truncate so that GenerateName + the 5-char suffix Kubernetes appends fits
	// within the 63-char Pod-name limit even when nodeOp.Name is near the limit.
	rebootPrefix := utils.TruncateNameWithHash(nodeOp.Name+"-reboot", utils.KubernetesNameLengthLimit-6) + "-"

	rebootPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: rebootPrefix,
			Namespace:    nodeOp.Namespace,
			Labels: map[string]string{
				labelKeyNodeOp: nodeOp.Name,
				labelKeyReboot: "true", //nolint:goconst // common label value; not worth a constant
				labelKeyNode:   nodeName,
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
						"/bin/sh", //nolint:goconst // path literal; not worth a constant
						"-c",
						`echo "=== Checking for existing reboot annotation ==="
EXISTING_ANNOTATION=$(kubectl get pod $POD_NAME --namespace $POD_NAMESPACE -o jsonpath='{.metadata.annotations.kairos\.io/reboot-state}' 2>/dev/null || echo "")
if [ "$EXISTING_ANNOTATION" = "` + rebootStatusCompleted + `" ]; then
	echo "Reboot annotation already exists, reboot was already performed. Exiting successfully."
	exit 0
fi
echo "No reboot annotation found, proceeding with reboot process..."

ls -la /sentinel/ || echo "Cannot list /sentinel directory"

# Watch only for THIS Job's sentinel (` + jobName + `-*). Stale sentinels left by
# previous runs use different Job names and can't match.
while true; do
	SENTINEL_FILE=$(find /sentinel -name "` + jobName + `-*" -type f 2>/dev/null | head -1)
	if [ -n "$SENTINEL_FILE" ]; then
		echo "Found sentinel file: $SENTINEL_FILE"
		echo "Deleting sentinel file before reboot..."
		rm -f "$SENTINEL_FILE"
		echo "Attempting to patch pod..."
		kubectl patch pod $POD_NAME -p '{"metadata":{"annotations":{"kairos.io/reboot-state":"` + rebootStatusCompleted + `"}}}' --namespace $POD_NAMESPACE || echo "kubectl patch failed"
		echo "Giving 5 seconds to the Job Pod to exit gracefully..."
		sleep 5
		echo "Attempting reboot with nsenter..."
		nsenter -i -m -t 1 -- reboot || echo "nsenter reboot failed"
		break
	fi
	echo "No matching sentinel file found, sleeping..."
	sleep 10
done`,
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
							Name:      sentinelVolumeName,
							MountPath: "/sentinel",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: sentinelVolumeName,
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
			labelKeyNodeOp: nodeOp.Name,
			labelKeyReboot: "true", //nolint:goconst // common label value; not worth a constant
			labelKeyNode:   nodeName,
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
			labelKeyNodeOp: nodeOp.Name,
			labelKeyReboot: "true", //nolint:goconst // common label value; not worth a constant
			labelKeyNode:   nodeName,
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

	// If no node selector specified, target all nodes; otherwise filter.
	var targetNodes []corev1.Node
	var selectorStr string
	if nodeOp.Spec.NodeSelector == nil {
		log.V(1).Info("No node selector specified, targeting all nodes", "nodeCount", len(allNodes))
		targetNodes = allNodes
	} else {
		selector, err := metav1.LabelSelectorAsSelector(nodeOp.Spec.NodeSelector)
		if err != nil {
			log.Error(err, "Failed to convert NodeSelector to selector")
			return nil, err
		}
		selectorStr = selector.String()
		for _, node := range allNodes {
			if selector.Matches(labels.Set(node.Labels)) {
				targetNodes = append(targetNodes, node)
			}
		}
	}

	// Sort nodes: masters first (by label 'node-role.kubernetes.io/master' or 'node-role.kubernetes.io/control-plane')
	sortedNodes := make([]corev1.Node, len(targetNodes))
	copy(sortedNodes, targetNodes)
	sort.SliceStable(sortedNodes, func(i, j int) bool {
		return isMasterNode(sortedNodes[i]) && !isMasterNode(sortedNodes[j])
	})

	log.Info("Found target nodes",
		"selector", selectorStr,
		"targetNodeCount", len(sortedNodes),
		"totalNodeCount", len(allNodes))

	return sortedNodes, nil
}

// manageJobCreation drives per-node state transitions: it advances preflight
// Pods that already exist, starts preflight or main work for nodes that don't
// have any state yet (respecting Concurrency and StopOnFailure), and creates
// the main Job when a preflight has reported "proceed".
func (r *NodeOpReconciler) manageJobCreation(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)

	targetNodes, err := r.getTargetNodes(ctx, nodeOp)
	if err != nil {
		return err
	}

	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	if getBool(nodeOp.Spec.StopOnFailure, StopOnFailureDefault) && r.hasFailedJobs(nodeOp) {
		log.Info("Stopping job creation due to failure and StopOnFailure=true")
		return nil
	}

	// First pass: advance any nodes currently in Preflight. Skipped/Failed
	// outcomes free their slot; proceed outcomes transition into the normal
	// Job-creation path inline (slot stays held by the same node).
	for _, node := range targetNodes {
		status, exists := nodeOp.Status.NodeStatuses[node.Name]
		if !exists || status.Phase != phasePreflight {
			continue
		}
		if err := r.advancePreflight(ctx, nodeOp, node); err != nil {
			log.Error(err, "Failed to advance preflight for node", "node", node.Name)
			return err
		}
	}

	// StopOnFailure may have been tripped by a preflight failure recorded above.
	if getBool(nodeOp.Spec.StopOnFailure, StopOnFailureDefault) && r.hasFailedJobs(nodeOp) {
		log.Info("Stopping new work after preflight failure (StopOnFailure=true)")
		return nil
	}

	// Second pass: start work for nodes that don't have a status entry yet,
	// up to the concurrency budget.
	runningCount := r.countRunningJobs(nodeOp)
	var maxConcurrency int32
	if nodeOp.Spec.Concurrency == 0 {
		maxConcurrency = int32(len(targetNodes))
	} else {
		maxConcurrency = nodeOp.Spec.Concurrency
	}
	availableSlots := maxConcurrency - runningCount
	if availableSlots <= 0 {
		log.V(1).Info("No available slots for new work",
			"nodeOp", nodeOp.Name,
			"running", runningCount,
			"maxConcurrency", maxConcurrency)
		return nil
	}

	var freshNodes []corev1.Node
	for _, node := range targetNodes {
		if _, exists := nodeOp.Status.NodeStatuses[node.Name]; !exists {
			freshNodes = append(freshNodes, node)
		}
	}
	if len(freshNodes) == 0 {
		return nil
	}

	toStart := int(availableSlots)
	if toStart > len(freshNodes) {
		toStart = len(freshNodes)
	}

	log.Info("Starting new work for nodes",
		"nodeOp", nodeOp.Name,
		"toStart", toStart,
		"availableSlots", availableSlots,
		"freshNodes", len(freshNodes))

	for i := 0; i < toStart; i++ {
		node := freshNodes[i]
		if nodeOp.Spec.Preflight != nil {
			if err := r.startPreflight(ctx, nodeOp, node); err != nil {
				log.Error(err, "Failed to start preflight for node", "node", node.Name)
				continue
			}
			continue
		}
		if err := r.startMainJob(ctx, nodeOp, node); err != nil {
			log.Error(err, "Failed to start main Job for node", "node", node.Name)
			continue
		}
	}

	return nil
}

// startMainJob creates the reboot Pod (if RebootOnSuccess) and then the main
// upgrade Job for a node. Shared by the no-preflight path and the
// preflight-proceeded path.
//
// We mint a short per-run ID up-front and use it as the suffix of the Job's
// explicit Name (instead of relying on Kubernetes' GenerateName, which only
// reveals the assigned name after Create). The reboot Pod, which is created
// BEFORE the Job to survive drain, can then be told exactly which Job's
// sentinel file to watch for ("<jobName>-*"). Without this, the reboot Pod
// could only watch on "<jobBaseName>-*" — a pattern that matches both the
// active Job's sentinel and any stale sentinels left behind by a previous
// interrupted run, which used to require a startup "cleanup" pass that raced
// the active Job's sentinel-creator. Unique per-run names eliminate that
// ambiguity (so the cleanup pass is gone).
func (r *NodeOpReconciler) startMainJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	fullName := fmt.Sprintf("%s-%s", nodeOp.Name, node.Name)
	jobBaseName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit-6)
	jobName := jobBaseName + "-" + rand.String(5)

	if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		if err := r.createRebootPod(ctx, nodeOp, node.Name, jobName); err != nil {
			return err
		}
	}
	return r.createNodeJob(ctx, nodeOp, node, jobName)
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
		// Conditions where we consider a node "in flight" against the concurrency budget:
		// 1. It's running a preflight check, OR
		// 2. It's in Pending or Running phase, OR
		// 3. It's Completed but reboot is still pending (node is busy rebooting)
		if status.Phase == phasePreflight || status.Phase == phasePending || status.Phase == phaseRunning ||
			(status.Phase == phaseCompleted && status.RebootStatus == rebootStatusPending) {
			count++
		}
	}
	return count
}

// startPreflight ensures a preflight Pod exists for the given node and records
// the node as Phase=Preflight in the NodeOp status. The Pod runs the
// user-supplied preflight command with the host root mounted read-only at
// Spec.HostMountPath.
//
// If a labeled preflight Pod for this node already exists (e.g. because a
// previous reconcile created the Pod but failed before writing NodeStatus, so
// manageJobCreation re-routes the node back through startPreflight), we reuse
// it instead of creating a duplicate that advancePreflight would later have to
// pick from arbitrarily.
func (r *NodeOpReconciler) startPreflight(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	log := logf.FromContext(ctx)

	existing, err := r.findPreflightPod(ctx, nodeOp, node.Name)
	if err != nil {
		return err
	}

	var podName string
	if existing != nil {
		podName = existing.Name
		log.Info("Reusing existing preflight Pod", "nodeOp", nodeOp.Name, "node", node.Name, "pod", podName)
	} else {
		pod := r.buildPreflightPod(nodeOp, node)
		if err := controllerutil.SetControllerReference(nodeOp, pod, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference on preflight Pod: %w", err)
		}
		if err := r.Create(ctx, pod); err != nil {
			return fmt.Errorf("failed to create preflight Pod for %s: %w", node.Name, err)
		}
		podName = pod.Name
		log.Info("Created preflight Pod", "nodeOp", nodeOp.Name, "node", node.Name, "pod", podName)
	}

	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}
	nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
		Phase:        phasePreflight,
		Message:      "Preflight in progress",
		RebootStatus: rebootStatusNotRequested,
		LastUpdated:  metav1.Now(),
	}
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOp status after starting preflight")
		return err
	}
	return nil
}

// advancePreflight inspects the preflight Pod for a node currently in
// Phase=Preflight and either: marks the node Completed (with the termination
// message as the skip reason), marks it Failed, or proceeds to create the main
// Job (which transitions it to Phase=Pending). Nodes whose preflight Pod has
// not yet terminated are left untouched.
func (r *NodeOpReconciler) advancePreflight(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	log := logf.FromContext(ctx)

	pod, err := r.findPreflightPod(ctx, nodeOp, node.Name)
	if err != nil {
		return err
	}
	if pod == nil {
		// Preflight Pod disappeared. Treat as Failed so the user notices.
		nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
			Phase:        phaseFailed,
			Message:      "Preflight Pod not found",
			RebootStatus: rebootStatusNotRequested,
			LastUpdated:  metav1.Now(),
		}
		return r.Status().Update(ctx, nodeOp)
	}

	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		msg := preflightTerminationMessage(pod)
		if msg == "" {
			// Proceed: hand the slot off to the normal upgrade flow.
			log.Info("Preflight reported proceed; starting main Job", "node", node.Name)
			if err := r.startMainJob(ctx, nodeOp, node); err != nil {
				return err
			}
			r.deletePreflightPod(ctx, pod)
			return nil
		}
		nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
			Phase:        phaseCompleted,
			Message:      "Skipped by preflight: " + msg,
			RebootStatus: rebootStatusNotRequested,
			LastUpdated:  metav1.Now(),
		}
		log.Info("Preflight reported skip", "node", node.Name, "reason", msg)
		if err := r.Status().Update(ctx, nodeOp); err != nil {
			return err
		}
		r.deletePreflightPod(ctx, pod)
		return nil
	case corev1.PodFailed:
		nodeOp.Status.NodeStatuses[node.Name] = kairosiov1alpha1.NodeStatus{
			Phase:        phaseFailed,
			Message:      "Preflight failed: " + preflightFailureReason(pod),
			RebootStatus: rebootStatusNotRequested,
			LastUpdated:  metav1.Now(),
		}
		log.Info("Preflight Pod ended in PodFailed; marking node Failed", "node", node.Name)
		if err := r.Status().Update(ctx, nodeOp); err != nil {
			return err
		}
		r.deletePreflightPod(ctx, pod)
		return nil
	default:
		// Still Pending/Running/etc. — nothing to advance yet.
		return nil
	}
}

// deletePreflightPod removes a preflight Pod once its verdict has been
// recorded. Best-effort: failures are logged but don't bubble up so the
// already-acted-upon verdict isn't undone by a transient delete error.
func (r *NodeOpReconciler) deletePreflightPod(ctx context.Context, pod *corev1.Pod) {
	log := logf.FromContext(ctx)
	grace := int64(0)
	if err := r.Delete(ctx, pod, &client.DeleteOptions{GracePeriodSeconds: &grace}); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Failed to delete preflight Pod", "pod", pod.Name)
		}
	}
}

// preflightTerminationMessage returns the termination message written by the
// preflight container (if any). Empty string means "proceed".
func preflightTerminationMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != preflightContainerName {
			continue
		}
		if cs.State.Terminated != nil {
			return cs.State.Terminated.Message
		}
	}
	return ""
}

// preflightFailureReason returns a short human-readable reason for a failed
// preflight Pod, falling back to the Pod's own Reason/Message fields.
func preflightFailureReason(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != preflightContainerName {
			continue
		}
		if cs.State.Terminated != nil {
			reason := cs.State.Terminated.Reason
			if reason == "" {
				reason = fmt.Sprintf("exit %d", cs.State.Terminated.ExitCode)
			}
			return reason
		}
	}
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	return "container did not report a terminated state"
}

// findPreflightPod returns the preflight Pod owned by this NodeOp for the
// given node, or nil if none exists.
func (r *NodeOpReconciler) findPreflightPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(nodeOp.Namespace),
		client.MatchingLabels{
			labelKeyNodeOp:    nodeOp.Name,
			labelKeyNode:      nodeName,
			labelKeyPreflight: "true", //nolint:goconst // common label value; not worth a constant
		},
	); err != nil {
		return nil, err
	}
	if len(podList.Items) == 0 {
		return nil, nil
	}
	return &podList.Items[0], nil
}

// buildPreflightPod returns the PodSpec for a single preflight Pod targeting
// the given node. The host root is mounted read-only at Spec.HostMountPath
// (defaulting to "/host"). `restartPolicy: OnFailure` plus
// `ActiveDeadlineSeconds` absorb transient failures while bounding total
// lifetime. `terminationMessagePolicy: File` (the default) ensures the
// container only communicates a skip reason via explicit writes to
// /dev/termination-log.
func (r *NodeOpReconciler) buildPreflightPod(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) *corev1.Pod {
	image := nodeOp.Spec.Preflight.Image
	if image == "" {
		image = getNodeOpImage(nodeOp)
	}

	deadline := preflightDefaultActiveDeadlineSeconds
	if nodeOp.Spec.Preflight.ActiveDeadlineSeconds != nil {
		deadline = int64(*nodeOp.Spec.Preflight.ActiveDeadlineSeconds)
	}

	hostMount := nodeOp.Spec.HostMountPath
	if hostMount == "" {
		hostMount = "/host" //nolint:goconst // default fallback; not worth a constant
	}

	// Truncate so that GenerateName + the 5-char suffix Kubernetes appends fits
	// within the 63-char Pod-name limit even when nodeOp.Name is near the limit.
	prefix := utils.TruncateNameWithHash(nodeOp.Name+"-preflight", utils.KubernetesNameLengthLimit-6) + "-"

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: prefix,
			Namespace:    nodeOp.Namespace,
			Labels: map[string]string{
				labelKeyNodeOp:    nodeOp.Name,
				labelKeyNode:      node.Name,
				labelKeyPreflight: "true", //nolint:goconst // common label value; not worth a constant
			},
		},
		Spec: corev1.PodSpec{
			NodeName:              node.Name,
			RestartPolicy:         corev1.RestartPolicyOnFailure,
			ActiveDeadlineSeconds: &deadline,
			ImagePullSecrets:      nodeOp.Spec.ImagePullSecrets,
			Containers: []corev1.Container{{
				Name:                     preflightContainerName,
				Image:                    image,
				Command:                  nodeOp.Spec.Preflight.Command,
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				VolumeMounts: []corev1.VolumeMount{{
					Name:      hostRootVolumeName,
					MountPath: hostMount,
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: hostRootVolumeName,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/",
					},
				},
			}},
		},
	}
}
