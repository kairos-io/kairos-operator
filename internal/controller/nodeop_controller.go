package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	"github.com/kairos-io/kairos-operator/internal/bootid"
	"github.com/kairos-io/kairos-operator/internal/rebootwatcher"
	"github.com/kairos-io/kairos-operator/internal/utils"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
	// clusterRoleBindingFinalizer is on NodeOps created by released operator
	// versions, which gave the reboot Pod its permissions through a
	// cluster-wide ClusterRoleBinding. When such a NodeOp is deleted,
	// handleDeletion deletes that binding and then removes this finalizer.
	// This operator never adds it.
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
	// Status messages of a node whose upgrade Job completed, before and after
	// its reboot is confirmed.
	jobCompletedMessage          = "Job completed successfully"
	jobAndRebootCompletedMessage = "Job and reboot completed successfully"
	// Label keys used on Jobs and Pods created by the NodeOp controller
	labelKeyNodeOp    = "kairos.io/nodeop"
	labelKeyNode      = "kairos.io/node"
	labelKeyReboot    = "kairos.io/reboot"
	labelKeyPreflight = "kairos.io/preflight"
	// Phase set on a node while its preflight Pod is in flight.
	phasePreflight = "Preflight"
	// preflightContainerName is the single container name inside every preflight Pod.
	preflightContainerName = "preflight"
	// rebootContainerName is the name of the only container in a reboot Pod.
	rebootContainerName = "reboot"
	// preflightDefaultActiveDeadlineSeconds bounds total preflight Pod lifetime
	// (used as a controller-side fallback when Spec.Preflight.ActiveDeadlineSeconds
	// is nil; the CRD's kubebuilder:default applies the same value at admission).
	preflightDefaultActiveDeadlineSeconds = int64(120)
	// Volume constants
	hostRootVolumeName = "host-root"
	hostDirEnv         = "HOST_DIR"
	// RBAC verbs used in rules the controllers create
	verbGet  = "get"
	verbList = "list"
	// RBAC resource names used in those rules
	resourceJobs = "jobs"
	resourcePods = "pods"
)

// bootIDReporterResources are the fixed resources of the upgrade Job's
// boot-id-reporter container, used for both requests and limits.
var bootIDReporterResources = corev1.ResourceList{
	corev1.ResourceCPU:    resource.MustParse("10m"),
	corev1.ResourceMemory: resource.MustParse("32Mi"),
}

// NodeOpReconciler reconciles a NodeOp object
type NodeOpReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader reads directly from the API server instead of the
	// controller's local copy, which can lag behind. It is used where acting
	// on an out-of-date read would repeat something, such as draining a node
	// again for an upgrade Job that already exists. When nil, Client is used.
	APIReader client.Reader
}

// apiReader returns APIReader, or Client when APIReader is not set.
func (r *NodeOpReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;create;update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=delete

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
		// A conflict is routine, so it is not logged here. It is still
		// returned: it can come from a Node write, and Nodes are not watched,
		// so only the workqueue's retry with backoff brings the NodeOp back.
		if !apierrors.IsConflict(err) {
			log.Error(err, "Failed to update NodeOp status")
		}
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
func (r *NodeOpReconciler) uncordonNode(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) error {
	log := logf.FromContext(ctx)

	// This helper always re-reads the latest node state, so callers only need to
	// supply the node name rather than a fully-populated Node object.
	latestNode := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, latestNode); err != nil {
		log.Error(err, "Failed to get latest node state", "node", nodeName)
		return err
	}

	if !latestNode.Spec.Unschedulable {
		// Clean up a stale annotation if the node was uncordoned out-of-band.
		// Conflicts on this Update are handled the same way as below: the error
		// propagates to Reconcile, which requeues.
		if _, hasAnn := latestNode.Annotations[cordonedByAnnotation]; hasAnn {
			delete(latestNode.Annotations, cordonedByAnnotation)
			if err := r.Update(ctx, latestNode); err != nil {
				log.Error(err, "Failed to clear stale cordoned-by annotation", "node", nodeName)
				return err
			}
		}
		return nil
	}

	owner, hasAnn := latestNode.Annotations[cordonedByAnnotation]
	if !hasAnn {
		log.Info("Node is cordoned but not by this operator; leaving it alone", "node", nodeName)
		return nil
	}
	if owner != nodeOpOwnerRef(nodeOp) {
		log.Info("Node was cordoned by a different NodeOp; leaving it alone",
			"node", nodeName, "cordonedBy", owner, "thisNodeOp", nodeOpOwnerRef(nodeOp))
		return nil
	}

	latestNode.Spec.Unschedulable = false
	delete(latestNode.Annotations, cordonedByAnnotation)
	// Same optimistic-concurrency pattern as cordonNode: a Conflict here means
	// the node was modified between Get and Update, and the error propagates up
	// so Reconcile can requeue and re-evaluate.
	if err := r.Update(ctx, latestNode); err != nil {
		log.Error(err, "Failed to uncordon node", "node", nodeName)
		return err
	}

	log.Info("Successfully uncordoned node", "node", nodeName, "nodeOp", nodeOpOwnerRef(nodeOp))
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

	// The grace period is a property of the DELETE request, so it travels as a
	// client.DeleteOption. A negative value means "use the grace period the Pod
	// declares", which is what the API server applies to a request that carries
	// no grace period.
	var deleteOpts []client.DeleteOption
	if drainOptions.GracePeriodSeconds != nil && *drainOptions.GracePeriodSeconds >= 0 {
		deleteOpts = append(deleteOpts, client.GracePeriodSeconds(int64(*drainOptions.GracePeriodSeconds)))
	}

	// Filter pods that are on this node. Nothing is evicted inside this loop:
	// the emptyDir guard below has to be able to refuse the whole drain without
	// having already taken pods down, the way `kubectl drain` does.
	var podsToEvict []corev1.Pod
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

		podsToEvict = append(podsToEvict, pod)
	}

	// Refuse the drain when it would destroy emptyDir data the operator has not
	// agreed to lose. This runs after the skip rules above, so a pod the drain
	// was never going to touch does not block it, and before any eviction, so a
	// refused drain leaves the node exactly as it found it.
	if !getBool(drainOptions.DeleteEmptyDirData, DrainDeleteEmptyDirDataDefault) {
		if withData := podsWithEmptyDirData(podsToEvict); len(withData) > 0 {
			err := fmt.Errorf("refusing to drain node %s: %w: %s",
				node.Name, errEmptyDirDataWouldBeLost, strings.Join(withData, ", "))
			log.Error(err, "Refusing to drain node", "node", node.Name)
			return err
		}
	}

	for _, pod := range podsToEvict {
		// Delete the pod
		if err := r.Delete(ctx, &pod, deleteOpts...); err != nil {
			log.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
			return err
		}
	}

	log.Info("Successfully drained node", "node", node.Name)
	return nil
}

// errEmptyDirDataWouldBeLost is the reason a drain is refused when a pod it
// would evict has an emptyDir volume and DrainOptions.DeleteEmptyDirData is not
// set. It mirrors `kubectl drain`, which reports "pods with local storage (use
// --delete-emptydir-data to override)" and aborts rather than evicting them.
var errEmptyDirDataWouldBeLost = errors.New(
	"pods with local storage would lose it (set drainOptions.deleteEmptyDirData to true to allow this)")

// podsWithEmptyDirData returns the namespace/name of every pod carrying an
// emptyDir volume. An emptyDir lives and dies with its pod, so evicting the pod
// destroys whatever it holds. A memory-backed emptyDir holds nothing across the
// eviction either way, but kubectl makes no exception for it and neither does
// this, so the two agree on which pods block a drain.
func podsWithEmptyDirData(pods []corev1.Pod) []string {
	var names []string
	for _, pod := range pods {
		for _, volume := range pod.Spec.Volumes {
			if volume.EmptyDir != nil {
				names = append(names, pod.Namespace+"/"+pod.Name)
				break
			}
		}
	}
	return names
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

// bootIDReporterScript returns the shell script of the upgrade Job's
// boot-id-reporter container, which runs after the upgrade has succeeded. It
// writes the node's boot ID to the container's termination message. The
// operator stores that boot ID in the NodeOp status, the reboot Pod reads it
// too, and both compare it with the node's boot ID after the reboot.
func bootIDReporterScript() string {
	return "read -r boot_id < /proc/sys/kernel/random/boot_id" +
		" && printf '%s' \"$boot_id\" > /dev/termination-log"
}

// bootIDReporterImage returns the image of the upgrade Job's boot-id-reporter
// container. Priority: the operator's SENTINEL_IMAGE environment variable (the
// sentinelImage Helm value), then NodeOp Spec.Image, then "busybox:latest".
func bootIDReporterImage(nodeOp *kairosiov1alpha1.NodeOp) string {
	if img := os.Getenv("SENTINEL_IMAGE"); img != "" {
		return img
	}
	if nodeOp.Spec.Image != "" {
		return nodeOp.Spec.Image
	}
	return "busybox:latest"
}

// getHostMountPath returns the host mount path for node operation.
// Defaults to /host if spec.HostMountPath is not set
func getHostMountPath(nodeOp *kairosiov1alpha1.NodeOp) string {
	if nodeOp.Spec.HostMountPath != "" {
		return nodeOp.Spec.HostMountPath
	}
	return defaultHostMountPath
}

// createRebootJobSpec creates a JobSpec for nodes that will reboot after job completion
func (r *NodeOpReconciler) createRebootJobSpec(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, backoffLimit int32) batchv1.JobSpec {
	hostMount := getHostMountPath(nodeOp)
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName:         node.Name,
				ImagePullSecrets: nodeOp.Spec.ImagePullSecrets,
				InitContainers: []corev1.Container{
					{
						Name:      "nodeop",
						Image:     getNodeOpImage(nodeOp),
						Command:   nodeOp.Spec.Command,
						Resources: nodeOp.ResourcesOrDefault(),
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						Env: []corev1.EnvVar{
							{
								Name:  hostDirEnv,
								Value: hostMount,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      hostRootVolumeName,
								MountPath: hostMount,
							},
						},
					},
				},
				Containers: []corev1.Container{
					{
						Name:  bootid.ReporterContainerName,
						Image: bootIDReporterImage(nodeOp),
						Resources: corev1.ResourceRequirements{
							Requests: bootIDReporterResources.DeepCopy(),
							Limits:   bootIDReporterResources.DeepCopy(),
						},
						Command: []string{
							"/bin/sh", //nolint:goconst // path literal; not worth a constant
							"-c",
							bootIDReporterScript(),
						},
						// The operator and the reboot Pod read the boot ID
						// from this container's termination message.
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
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

// createStandardJobSpec creates a JobSpec for standard node operations without reboot
func (r *NodeOpReconciler) createStandardJobSpec(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, backoffLimit int32) batchv1.JobSpec {
	hostMount := getHostMountPath(nodeOp)
	return batchv1.JobSpec{
		BackoffLimit: &backoffLimit,
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName:         node.Name,
				ImagePullSecrets: nodeOp.Spec.ImagePullSecrets,
				Containers: []corev1.Container{
					{
						Name:      "nodeop",
						Image:     getNodeOpImage(nodeOp),
						Command:   nodeOp.Spec.Command,
						Resources: nodeOp.ResourcesOrDefault(),
						SecurityContext: &corev1.SecurityContext{
							Privileged: asBool(true),
						},
						Env: []corev1.EnvVar{
							{
								Name:  hostDirEnv,
								Value: hostMount,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      hostRootVolumeName,
								MountPath: hostMount,
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

// createNodeJob cordons and drains nodeName, creates its upgrade Job named
// jobName and records that upgrade Job in the NodeOp status. If an upgrade Job
// with that name already exists and belongs to nodeOp, an earlier reconcile
// created it but failed to record it. It is then only recorded: cordoning and
// draining again would delete the upgrade Job's running Pod.
func (r *NodeOpReconciler) createNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node, jobName string) error {
	log := logf.FromContext(ctx)

	existing := &batchv1.Job{}
	err := r.apiReader().Get(ctx, types.NamespacedName{Namespace: nodeOp.Namespace, Name: jobName}, existing)
	switch {
	case err == nil:
		if !metav1.IsControlledBy(existing, nodeOp) {
			return fmt.Errorf("job %s exists and is not controlled by NodeOp %s", jobName, nodeOp.Name)
		}
		log.Info("Job already exists; recording it", "node", node.Name, "job", jobName)
		return r.recordNodeJob(ctx, nodeOp, node.Name, jobName)
	case !apierrors.IsNotFound(err):
		return err
	}

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
				if uncordonErr := r.uncordonNode(ctx, nodeOp, node.Name); uncordonErr != nil {
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
		adopted, adoptErr := r.adoptExistingJob(ctx, nodeOp, jobName, err)
		if !adopted {
			log.Error(adoptErr, "Failed to create Job for node",
				"nodeOp", nodeOp.Name,
				"node", node.Name)
			return adoptErr
		}
		log.Info("Job already exists; recording it", "node", node.Name, "job", jobName)
	}

	if err := r.recordNodeJob(ctx, nodeOp, node.Name, job.Name); err != nil {
		return err
	}
	log.Info("Created Job for node",
		"nodeOp", nodeOp.Name,
		"node", node.Name,
		"job", job.Name)
	return nil
}

// recordNodeJob records jobName as the upgrade Job of nodeName in the NodeOp
// status.
func (r *NodeOpReconciler) recordNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName, jobName string) error {
	// Initialize node status
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	// Determine initial reboot status
	rebootStatus := rebootStatusNotRequested
	if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		rebootStatus = rebootStatusPending
	}

	nodeOp.Status.NodeStatuses[nodeName] = kairosiov1alpha1.NodeStatus{
		Phase:        phasePending,
		JobName:      jobName,
		Message:      "Job created",
		RebootStatus: rebootStatus,
		LastUpdated:  metav1.Now(),
	}

	if err := r.Status().Update(ctx, nodeOp); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to update NodeOp status after Job creation")
		return err
	}
	return nil
}

// adoptExistingJob is called when creating the upgrade Job named jobName failed
// with createErr. It reports whether the error says that upgrade Job already
// exists and the existing one belongs to nodeOp. That happens when this
// operator created the same upgrade Job between the check at the top of
// createNodeJob and the create call, so recording it is safe. Any other error
// is returned unchanged.
func (r *NodeOpReconciler) adoptExistingJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, jobName string, createErr error) (bool, error) {
	if !apierrors.IsAlreadyExists(createErr) {
		return false, createErr
	}
	existing := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: nodeOp.Namespace, Name: jobName}, existing); err != nil {
		return false, err
	}
	if !metav1.IsControlledBy(existing, nodeOp) {
		return false, fmt.Errorf("job %s exists and is not controlled by NodeOp %s: %w", jobName, nodeOp.Name, createErr)
	}
	return true, nil
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
		// A node waiting for its reboot Pod has no upgrade Job yet either;
		// manageJobCreation creates it once the reboot Pod is ready.
		if awaitingRebootPod(status) {
			continue
		}
		// Skipped-by-preflight nodes are terminal: Phase=Completed, no Job,
		// no reboot. Don't run them through the Job/reboot processors.
		if status.Phase == phaseCompleted && status.JobName == "" {
			completedNodes++
			continue
		}
		// Failed-by-preflight nodes are terminal as well, and they have no Job
		// either. processJobStatus would Get a Job named "" and take that for a
		// deleted Job, replacing the preflight reason with "Job not found".
		if status.Phase == phaseFailed && status.JobName == "" {
			anyFailed = true
			if err := r.uncordonAfterFailure(ctx, nodeOp, nodeName); err != nil {
				return err
			}
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
				if err := r.uncordonNode(ctx, nodeOp, nodeName); err != nil {
					log.Error(err, "Failed to uncordon node after reboot completion", "node", nodeName)
					return err
				}
			}
		}

		// Count failed jobs
		if status.Phase == phaseFailed {
			anyFailed = true

			if err := r.uncordonAfterFailure(ctx, nodeOp, nodeName); err != nil {
				return err
			}
		}

		// Count completed nodes based on whether reboot is required
		if getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
			// The node is completed only when its upgrade Job completed and the node rebooted
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

// uncordonAfterFailure uncordons a node whose operation failed, when the
// NodeOp opted into it. The uncordonNode helper only acts on nodes this NodeOp
// cordoned, so out-of-band cordons are left alone. A failed node does not
// reboot, so it is safe to uncordon now.
func (r *NodeOpReconciler) uncordonAfterFailure(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) error {
	if !getBool(nodeOp.Spec.Cordon, CordonDefault) ||
		!getBool(nodeOp.Spec.UncordonOnFailure, UncordonOnFailureDefault) {
		return nil
	}
	if err := r.uncordonNode(ctx, nodeOp, nodeName); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to uncordon node after failure", "node", nodeName)
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
				status.Message = jobCompletedMessage
				// processRebootStatus sets the reboot message only on the
				// pass that confirms the reboot. Every later pass comes
				// through here first, so it keeps that message.
				if status.RebootStatus == rebootStatusCompleted {
					status.Message = jobAndRebootCompletedMessage
				}
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
		rebootCompleted, err := r.isRebootCompleted(ctx, nodeOp, nodeName, &status)
		if err != nil {
			log.Error(err, "Failed to check reboot completion", "node", nodeName)
			return status, err
		}

		if rebootCompleted {
			status.RebootStatus = rebootStatusCompleted
			status.Message = jobAndRebootCompletedMessage
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

// rebootRBACName is the name of the ServiceAccount, Role and RoleBinding that
// nodeOp's reboot Pods run under.
func rebootRBACName(nodeOp *kairosiov1alpha1.NodeOp) string {
	return nodeOp.Name + "-reboot"
}

// rebootRoleRules returns the permissions a reboot Pod needs: getting its
// upgrade Job, to see when Kubernetes marks it Complete, and listing Pods, to
// read the boot ID from that upgrade Job's succeeded Pod.
func rebootRoleRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{APIGroups: []string{batchv1.GroupName}, Resources: []string{resourceJobs}, Verbs: []string{verbGet}},
		{APIGroups: []string{corev1.GroupName}, Resources: []string{resourcePods}, Verbs: []string{verbList}},
	}
}

// ensureRebootRBAC creates, in the NodeOp's namespace, what a reboot Pod needs
// to read its upgrade Job: a ServiceAccount, a Role with the permissions from
// rebootRoleRules, and a RoleBinding that grants the Role to the
// ServiceAccount. All three belong to the NodeOp, so Kubernetes deletes them
// together with it.
//
// The ServiceAccount and the RoleBinding are only created when they are
// missing. An existing one is left as it is: an earlier reconcile may have
// created it, and a released operator version may have created a
// ServiceAccount with the same name for the same NodeOp. The Role is also
// corrected when it exists with other permissions (see ensureRebootRole).
func (r *NodeOpReconciler) ensureRebootRBAC(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	name := rebootRBACName(nodeOp)

	if err := r.createRebootRBACObject(ctx, nodeOp, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nodeOp.Namespace},
	}); err != nil {
		return err
	}

	// The Role is set up before the RoleBinding, so the reboot Pod's
	// permissions are complete as soon as the RoleBinding exists.
	if err := r.ensureRebootRole(ctx, nodeOp); err != nil {
		return err
	}

	return r.createRebootRBACObject(ctx, nodeOp, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nodeOp.Namespace},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      name,
			Namespace: nodeOp.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     name,
		},
	})
}

// createRebootRBACObject makes obj belong to nodeOp and creates it. An object
// that already exists is not an error.
func (r *NodeOpReconciler) createRebootRBACObject(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, obj client.Object) error {
	name := rebootRBACName(nodeOp)
	if err := controllerutil.SetControllerReference(nodeOp, obj, r.Scheme); err != nil {
		return fmt.Errorf("setting controller reference on reboot %T %s: %w", obj, name, err)
	}
	if err := r.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating reboot %T %s: %w", obj, name, err)
	}
	return nil
}

// ensureRebootRole creates the reboot Pod's Role, or updates it when it exists
// with other permissions than rebootRoleRules. That happens when an operator
// version that granted other permissions created it, or when someone changed
// it by hand. Without the update, the reboot Pod could miss a permission it
// needs: it would then never see its upgrade Job finish, and the node would
// stay cordoned without being rebooted.
//
// The Role is read directly from the API server, so the operator does not
// need to keep a copy of every Role in the cluster. When creating or updating
// the Role fails, for example because the Role was created or changed at the
// same moment, the error is returned and the operator tries again on its next
// pass, which reads the Role again.
func (r *NodeOpReconciler) ensureRebootRole(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	name := rebootRBACName(nodeOp)
	role := &rbacv1.Role{}
	err := r.apiReader().Get(ctx, types.NamespacedName{Name: name, Namespace: nodeOp.Namespace}, role)
	if apierrors.IsNotFound(err) {
		role = &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: nodeOp.Namespace},
			Rules:      rebootRoleRules(),
		}
		if err := controllerutil.SetControllerReference(nodeOp, role, r.Scheme); err != nil {
			return fmt.Errorf("setting controller reference on reboot Role %s: %w", name, err)
		}
		if err := r.Create(ctx, role); err != nil {
			return fmt.Errorf("creating reboot Role %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting reboot Role %s: %w", name, err)
	}

	if equality.Semantic.DeepEqual(role.Rules, rebootRoleRules()) {
		return nil
	}
	logf.FromContext(ctx).Info("Updating the reboot Role to the permissions this operator version needs",
		"role", name, "namespace", nodeOp.Namespace)
	role.Rules = rebootRoleRules()
	if err := r.Update(ctx, role); err != nil {
		return fmt.Errorf("updating reboot Role %s: %w", name, err)
	}
	return nil
}

// createRebootPod creates the reboot Pod on nodeName for the upgrade Job named
// jobName. The reboot Pod is created before its upgrade Job, so it is given
// the upgrade Job's name up front (see startMainJob). It waits for the upgrade
// Job to complete and then reboots the node, using the read-only permissions
// from ensureRebootRBAC.
func (r *NodeOpReconciler) createRebootPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName, jobName string) (*corev1.Pod, error) {
	log := logf.FromContext(ctx)

	if err := r.ensureRebootRBAC(ctx, nodeOp); err != nil {
		log.Error(err, "Failed to ensure reboot RBAC", "node", nodeName)
		return nil, err
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
					Name:      rebootContainerName,
					Image:     operatorImage,
					Resources: nodeOp.RebootResourcesOrDefault(),
					// The reboot Pod runs the manager image's reboot-watcher
					// program. It reads its upgrade Job through the API,
					// compares the boot ID the upgrade Job reported with the
					// node's current one, and reboots the node while they are
					// equal.
					Command: rebootwatcher.Command(),
					SecurityContext: &corev1.SecurityContext{
						Privileged: asBool(true),
						RunAsUser:  &[]int64{0}[0],
					},
					Env: []corev1.EnvVar{
						{
							Name:  rebootwatcher.JobNameEnv,
							Value: jobName,
						},
						{
							Name: rebootwatcher.NamespaceEnv,
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
							},
						},
					},
				},
			},
			RestartPolicy:      corev1.RestartPolicyOnFailure,
			ServiceAccountName: rebootRBACName(nodeOp),
			// The reboot pod must survive the NotReady/Unreachable window caused
			// by the very reboot it triggers. Without infinite NoExecute
			// tolerations, the taint-eviction controller deletes the pod after
			// the defaulted tolerationSeconds (300s stock), so it cannot run
			// again after the node comes back, see the new boot ID and exit.
			//
			// TolerationSeconds is deliberately omitted: a NoExecute toleration
			// without it tolerates the taint indefinitely, which is what makes
			// these tolerations "infinite".
			Tolerations: []corev1.Toleration{
				{
					Key:      corev1.TaintNodeNotReady,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
				{
					Key:      corev1.TaintNodeUnreachable,
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(nodeOp, rebootPod, r.Scheme); err != nil {
		log.Error(err, "Failed to set controller reference for reboot pod")
		return nil, err
	}

	if err := r.Create(ctx, rebootPod); err != nil {
		log.Error(err, "Failed to create reboot pod", "node", nodeName)
		return nil, err
	}

	log.Info("Created reboot pod", "node", nodeName, "pod", rebootPod.Name)
	return rebootPod, nil
}

// keepOneRebootPod leaves nodeName with at most one usable reboot Pod for
// nodeOp. It deletes the node's other reboot Pods and returns the one it
// kept, or nil when there is none and the caller has to create one.
//
// It is only called for a node whose upgrade Job does not exist yet, so none
// of the node's reboot Pods has rebooted it in the current run. It sorts the
// node's reboot Pods into three groups:
//
//   - Reboot Pods that are already being deleted are left alone.
//   - Reboot Pods that have stopped (phase Succeeded or Failed) can no longer
//     reboot the node, so they are deleted, and the caller creates a new one.
//   - Reboot Pods that are running or starting can be used. If there is more
//     than one, the oldest is kept and the others are deleted, so the node has
//     a single reboot Pod and a single upgrade Job name.
//
// The reboot Pods are read directly from the API server. The controller's
// local copy can lag behind, and missing a reboot Pod that the previous
// reconcile created would make the caller create a second one.
func (r *NodeOpReconciler) keepOneRebootPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) (*corev1.Pod, error) {
	pods, err := r.listRebootPods(ctx, r.apiReader(), nodeOp, nodeName)
	if err != nil {
		return nil, err
	}
	var live, finished []corev1.Pod
	for _, pod := range pods {
		switch {
		case !pod.DeletionTimestamp.IsZero():
			// Already being deleted: not used, and not deleted a second time.
		case pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed:
			finished = append(finished, pod)
		default:
			live = append(live, pod)
		}
	}
	if err := r.deleteRebootPods(ctx, finished); err != nil {
		return nil, err
	}
	if len(live) == 0 {
		return nil, nil
	}
	sort.Slice(live, func(i, j int) bool {
		ti, tj := live[i].CreationTimestamp, live[j].CreationTimestamp
		if !ti.Equal(&tj) {
			return ti.Before(&tj)
		}
		return live[i].Name < live[j].Name
	})
	if err := r.deleteRebootPods(ctx, live[1:]); err != nil {
		return nil, err
	}
	return &live[0], nil
}

// listRebootPods returns all of nodeOp's reboot Pods on nodeName, read
// through reader.
func (r *NodeOpReconciler) listRebootPods(ctx context.Context, reader client.Reader, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := reader.List(
		ctx, podList,
		client.InNamespace(nodeOp.Namespace),
		client.MatchingLabels{
			labelKeyNodeOp: nodeOp.Name,
			labelKeyReboot: "true", //nolint:goconst // common label value; not worth a constant
			labelKeyNode:   nodeName,
		},
	); err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// deleteRebootPods deletes the given reboot Pods. Reboot Pods that are already
// being deleted are skipped, and reboot Pods that are already gone are not an
// error.
func (r *NodeOpReconciler) deleteRebootPods(ctx context.Context, pods []corev1.Pod) error {
	log := logf.FromContext(ctx)
	for i := range pods {
		pod := &pods[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		log.Info("Deleting reboot pod", "node", pod.Spec.NodeName, "pod", pod.Name, "phase", pod.Status.Phase)
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to delete reboot pod", "node", pod.Spec.NodeName, "pod", pod.Name)
			return err
		}
	}
	return nil
}

// rebootPodJobName returns the name of the upgrade Job that the reboot Pod
// waits for, or "" when the reboot Pod does not name one.
func rebootPodJobName(pod *corev1.Pod) string {
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			if env.Name == rebootwatcher.JobNameEnv {
				return env.Value
			}
		}
	}
	return ""
}

// cleanupRebootPodForNode deletes all of nodeOp's reboot Pods on nodeName.
func (r *NodeOpReconciler) cleanupRebootPodForNode(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) error {
	pods, err := r.listRebootPods(ctx, r.Client, nodeOp, nodeName)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list reboot pods", "node", nodeName)
		return err
	}
	return r.deleteRebootPods(ctx, pods)
}

// isRebootCompleted reports whether nodeName has rebooted since its upgrade
// Job finished.
//
// The comparison point is the boot ID that the upgrade Job's boot-id-reporter
// container wrote to its termination message. Kubernetes stores that message
// in the same status update that marks the upgrade Job's Pod as finished, so
// it is in the cluster as soon as the upgrade has finished, whether or not
// the operator was running at that moment. The first call that finds it
// copies it to status.PreRebootBootID, which is saved with the rest of the
// NodeOp status, so it is kept even if the upgrade Job's Pod is deleted later.
//
// The reboot Pod is not involved: the Node's own boot ID is the proof. This
// still works when the reboot Pod never runs again after the reboot, for
// example when the upgraded kubelet cannot start containers yet. Without a
// valid boot ID from the upgrade Job no reboot can be proven, so the node
// stays cordoned and its reboot stays pending.
func (r *NodeOpReconciler) isRebootCompleted(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string, status *kairosiov1alpha1.NodeStatus) (bool, error) {
	log := logf.FromContext(ctx)

	if status.PreRebootBootID == "" {
		bootID, err := r.jobPodBootID(ctx, nodeOp.Namespace, status.JobName)
		if err != nil {
			return false, err
		}
		if !bootid.Valid(bootID) {
			log.V(1).Info("No Succeeded Job Pod with a valid boot ID is available yet; leaving the reboot pending",
				"node", nodeName,
				"job", status.JobName)
			return false, nil
		}
		status.PreRebootBootID = bootID
		status.LastUpdated = metav1.Now()
		log.Info("Recorded pre-reboot boot ID", "node", nodeName, "bootID", bootID)
	}

	return r.nodeRebootedSince(ctx, nodeName, status.PreRebootBootID)
}

// jobPodBootID returns the boot ID reported by the upgrade Job named jobName,
// read from that upgrade Job's succeeded Pod, or "" when no Pod reports a
// valid one.
func (r *NodeOpReconciler) jobPodBootID(ctx context.Context, namespace, jobName string) (string, error) {
	if jobName == "" {
		return "", nil
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{batchv1.JobNameLabel: jobName},
	); err != nil {
		return "", err
	}

	return bootid.FromJobPods(podList.Items), nil
}

// nodeRebootedSince reports whether nodeName is back up on a different boot
// than preRebootBootID. The kubelet refreshes status.nodeInfo.bootID on every
// boot, so this is a level-triggered fact: unlike a Ready=True -> Ready=False
// -> Ready=True transition it cannot be missed between two reconciles. The node
// must also be Ready, so that a node observed mid-reboot is not called done.
//
// An empty preRebootBootID means there is nothing to compare with, so no
// reboot can be proven and the answer is false.
func (r *NodeOpReconciler) nodeRebootedSince(ctx context.Context, nodeName, preRebootBootID string) (bool, error) {
	if preRebootBootID == "" {
		return false, nil
	}

	node := &corev1.Node{}
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if node.Status.NodeInfo.BootID == "" || node.Status.NodeInfo.BootID == preRebootBootID {
		return false, nil
	}

	return isNodeReady(node), nil
}

// isNodeReady reports whether the node's Ready condition is currently True.
func isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// handleDeletion handles the finalization process when a NodeOp is being deleted
func (r *NodeOpReconciler) handleDeletion(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) (bool, error) {
	if nodeOp.DeletionTimestamp.IsZero() {
		return false, nil
	}

	log := logf.FromContext(ctx)

	// NodeOps created by released operator versions carry
	// clusterRoleBindingFinalizer and a cluster-wide ClusterRoleBinding named
	// nodeop-reboot-<name>, which gave their reboot Pod its permissions.
	// Deleting such a NodeOp deletes that binding, if it still exists, and
	// then removes the finalizer so the NodeOp can go away. The ServiceAccount
	// the binding points to belongs to the NodeOp, so Kubernetes deletes it
	// with the NodeOp.
	if controllerutil.ContainsFinalizer(nodeOp, clusterRoleBindingFinalizer) {
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

// manageJobCreation moves each node through its states. It advances
// preflight Pods that already exist, creates the upgrade Job for nodes whose
// reboot Pod is ready, starts the preflight Pod or the upgrade for nodes that
// have no state yet (respecting Concurrency and StopOnFailure), and starts the
// upgrade when a preflight Pod has reported "proceed".
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
		return r.releaseWaitingNodes(ctx, nodeOp)
	}

	// First pass: advance nodes that are in Preflight or waiting for their
	// reboot Pod. A preflight Pod that says skip, or that failed, frees the
	// node's slot. A preflight Pod that says proceed, and a node still waiting
	// for its reboot Pod, go through startMainJob right away, and the node
	// keeps its slot.
	for _, node := range targetNodes {
		status, exists := nodeOp.Status.NodeStatuses[node.Name]
		if !exists {
			continue
		}
		switch {
		case status.Phase == phasePreflight:
			if err := r.advancePreflight(ctx, nodeOp, node); err != nil {
				log.Error(err, "Failed to advance preflight for node", "node", node.Name)
				return err
			}
		case awaitingRebootPod(status):
			if err := r.startMainJob(ctx, nodeOp, node); err != nil {
				log.Error(err, "Failed to start main Job for node", "node", node.Name)
				return err
			}
		}
	}

	// StopOnFailure may have been tripped by a preflight failure recorded above.
	if getBool(nodeOp.Spec.StopOnFailure, StopOnFailureDefault) && r.hasFailedJobs(nodeOp) {
		log.Info("Stopping new work after preflight failure (StopOnFailure=true)")
		return r.releaseWaitingNodes(ctx, nodeOp)
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

// startMainJob starts the upgrade of a node. Without RebootOnSuccess it
// creates the upgrade Job right away. With RebootOnSuccess it first makes sure
// the node has a reboot Pod, and creates the upgrade Job only once that reboot
// Pod is ready (see rebootPodReady): an upgrade without a working reboot Pod
// would leave the node upgraded and never rebooted. Until then the node is
// recorded as waiting. It is called for nodes without preflight, for nodes
// whose preflight Pod reported "proceed", and again for nodes that are
// waiting for their reboot Pod.
//
// The upgrade Job's name is chosen here, before the upgrade Job exists: a
// fixed prefix plus a short random suffix. Kubernetes' generateName is not
// used because it only reveals the name after the upgrade Job is created, and
// the reboot Pod, which is created first so that the drain leaves it alone,
// needs the name up front. While the node waits, the name is stored only in
// the reboot Pod, and later calls read it back from there. Every run gets a
// new name, so an upgrade Job left over from an earlier run is never mistaken
// for the current one.
func (r *NodeOpReconciler) startMainJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) error {
	if !getBool(nodeOp.Spec.RebootOnSuccess, RebootOnSuccessDefault) {
		return r.createNodeJob(ctx, nodeOp, node, newJobName(nodeOp, node))
	}

	rebootPod, err := r.keepOneRebootPod(ctx, nodeOp, node.Name)
	if err != nil {
		return err
	}
	if rebootPod == nil {
		rebootPod, err = r.createRebootPod(ctx, nodeOp, node.Name, newJobName(nodeOp, node))
		if err != nil {
			return err
		}
	}
	jobName := rebootPodJobName(rebootPod)
	if jobName == "" {
		return fmt.Errorf("reboot Pod %s does not name its upgrade Job in %s", rebootPod.Name, rebootwatcher.JobNameEnv)
	}

	if !rebootPodReady(rebootPod) {
		return r.awaitRebootPod(ctx, nodeOp, node.Name, rebootPod)
	}
	return r.createNodeJob(ctx, nodeOp, node, jobName)
}

// rebootPodReady reports whether the reboot Pod's program is running: the
// Pod's Ready condition is True and its container is running. The Pod's
// Running phase alone is not enough, because a container that keeps crashing
// leaves the Pod in the Running phase.
func rebootPodReady(pod *corev1.Pod) bool {
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			ready = condition.Status == corev1.ConditionTrue
		}
	}
	if !ready {
		return false
	}
	status := rebootContainerStatus(pod)
	return status != nil && status.State.Running != nil
}

// rebootContainerStatus returns the status of the reboot Pod's container, or
// nil when Kubernetes has not reported it yet.
func rebootContainerStatus(pod *corev1.Pod) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == rebootContainerName {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// newJobName returns a new, unique name for an upgrade Job of nodeOp on node.
func newJobName(nodeOp *kairosiov1alpha1.NodeOp, node corev1.Node) string {
	fullName := fmt.Sprintf("%s-%s", nodeOp.Name, node.Name)
	jobBaseName := utils.TruncateNameWithHash(fullName, utils.KubernetesNameLengthLimit-6)
	return jobBaseName + "-" + rand.String(5)
}

// awaitingRebootPod reports whether status belongs to a node that is waiting
// for its reboot Pod to be ready before its upgrade Job is created.
func awaitingRebootPod(status kairosiov1alpha1.NodeStatus) bool {
	return status.Phase == phasePending && status.JobName == ""
}

// awaitRebootPod records in the NodeOp status that nodeName is waiting for
// rebootPod to be ready before its upgrade Job is created. The node is
// Pending, so it takes up one of the concurrency slots. The operator watches
// reboot Pods, so it looks at the node again when rebootPod changes. The
// status message names the reboot Pod's phase once it is past Pending (for
// example Unknown, when its node stops reporting) and the reason its
// container is waiting (for example ImagePullBackOff or CrashLoopBackOff).
func (r *NodeOpReconciler) awaitRebootPod(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string, rebootPod *corev1.Pod) error {
	var details []string
	if phase := rebootPod.Status.Phase; phase != "" && phase != corev1.PodPending {
		details = append(details, fmt.Sprintf("reboot Pod phase: %s", phase))
	}
	if status := rebootContainerStatus(rebootPod); status != nil && status.State.Waiting != nil && status.State.Waiting.Reason != "" {
		details = append(details, fmt.Sprintf("container: %s", status.State.Waiting.Reason))
	}
	message := "Waiting for the reboot Pod to be ready"
	if len(details) > 0 {
		message = fmt.Sprintf("%s (%s)", message, strings.Join(details, ", "))
	}
	if status, ok := nodeOp.Status.NodeStatuses[nodeName]; ok && awaitingRebootPod(status) && status.Message == message {
		return nil
	}
	if nodeOp.Status.NodeStatuses == nil {
		nodeOp.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}
	nodeOp.Status.NodeStatuses[nodeName] = kairosiov1alpha1.NodeStatus{
		Phase:        phasePending,
		Message:      message,
		RebootStatus: rebootStatusPending,
		LastUpdated:  metav1.Now(),
	}
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Waiting for the reboot Pod before creating the upgrade Job",
		"node", nodeName, "pod", rebootPod.Name, "phase", rebootPod.Status.Phase)
	return nil
}

// releaseWaitingNodes runs when StopOnFailure halts the operation after a
// failure. It handles the nodes that are still waiting for their reboot Pod
// to be ready. Their upgrade has not started: no upgrade Job exists and the
// node is not cordoned. Left alone, such a node would wait forever, because
// the halted operation never starts its upgrade. So the node is dropped from
// the operation: its reboot Pods are deleted and the node is removed from the
// NodeOp status, as if it had never been picked.
//
// There is one exception. The operator may have created a node's upgrade Job
// and then failed to save that in the NodeOp status, so the node still looks
// like it is waiting while its upgrade is already running. For such a node
// the upgrade Job is saved in the NodeOp status instead, and the reboot Pod
// is kept, so the node is still rebooted and uncordoned when the upgrade
// finishes.
//
// The reboot Pods are deleted before the node is removed from the NodeOp
// status. If saving the status fails, the node is still waiting, and the
// operator tries again the next time it processes the NodeOp.
func (r *NodeOpReconciler) releaseWaitingNodes(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp) error {
	log := logf.FromContext(ctx)
	var released []string
	for nodeName, status := range nodeOp.Status.NodeStatuses {
		if !awaitingRebootPod(status) {
			continue
		}
		jobName, err := r.unrecordedNodeJob(ctx, nodeOp, nodeName)
		if err != nil {
			return err
		}
		if jobName != "" {
			log.Info("Job already exists; recording it", "node", nodeName, "job", jobName)
			if err := r.recordNodeJob(ctx, nodeOp, nodeName, jobName); err != nil {
				return err
			}
			continue
		}
		if err := r.cleanupRebootPodForNode(ctx, nodeOp, nodeName); err != nil {
			return err
		}
		released = append(released, nodeName)
	}
	if len(released) == 0 {
		return nil
	}
	for _, nodeName := range released {
		delete(nodeOp.Status.NodeStatuses, nodeName)
	}
	if err := r.Status().Update(ctx, nodeOp); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Released nodes waiting for their reboot Pod (StopOnFailure=true)", "nodes", released)
	return nil
}

// unrecordedNodeJob returns the name of the upgrade Job that one of nodeOp's
// reboot Pods on nodeName names and that belongs to nodeOp, or "" when there
// is none. It reads directly from the API server: an upgrade Job created
// moments ago may be missing from the controller's local copy, and releasing
// its node would leave that upgrade Job running with nothing tracking it.
func (r *NodeOpReconciler) unrecordedNodeJob(ctx context.Context, nodeOp *kairosiov1alpha1.NodeOp, nodeName string) (string, error) {
	pods, err := r.listRebootPods(ctx, r.apiReader(), nodeOp, nodeName)
	if err != nil {
		return "", err
	}
	for i := range pods {
		jobName := rebootPodJobName(&pods[i])
		if jobName == "" {
			continue
		}
		job := &batchv1.Job{}
		err := r.apiReader().Get(ctx, types.NamespacedName{Namespace: nodeOp.Namespace, Name: jobName}, job)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return "", err
		}
		if metav1.IsControlledBy(job, nodeOp) {
			return jobName, nil
		}
	}
	return "", nil
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
	if err := r.List(
		ctx, podList,
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

	hostMount := getHostMountPath(nodeOp)

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
				Resources:                nodeOp.PreflightResourcesOrDefault(),
				TerminationMessagePolicy: corev1.TerminationMessageReadFile,
				Env: []corev1.EnvVar{
					{
						Name:  hostDirEnv,
						Value: hostMount,
					},
				},
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
