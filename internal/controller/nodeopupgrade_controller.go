package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	kindNodeOpUpgrade     = "NodeOpUpgrade"
	phaseInitializing     = "Initializing"
	hostMountPath         = "/host"
	labelKeyNodeOpUpgrade = "nodeopupgrade.kairos.io/name"
	// nodeImageRepoAnnotation is the node-labeler annotation derived from
	// KAIROS_IMAGE_REPO in /etc/kairos-release. The NodeOpUpgrade controller
	// compares it against Spec.Image to skip nodes already at the target image.
	nodeImageRepoAnnotation = "kairos.io/image-repo"
	// hostnameLabel is the standard Kubernetes label every node carries; we
	// use it as the key for the NotIn matchExpression that excludes skipped
	// nodes from the created NodeOp's NodeSelector.
	hostnameLabel = "kubernetes.io/hostname"
)

// NodeOpUpgradeReconciler reconciles a NodeOpUpgrade object
type NodeOpUpgradeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgrades,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgrades/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgrades/finalizers,verbs=update
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *NodeOpUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Get the NodeOpUpgrade resource
	nodeOpUpgrade := &kairosiov1alpha1.NodeOpUpgrade{}
	err := r.Get(ctx, req.NamespacedName, nodeOpUpgrade)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "Failed to get NodeOpUpgrade")
			return ctrl.Result{}, err
		}
		log.Info("NodeOpUpgrade resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	// Set TypeMeta fields since they are not stored in etcd
	nodeOpUpgrade.TypeMeta = metav1.TypeMeta{
		APIVersion: "operator.kairos.io/v1alpha1",
		Kind:       kindNodeOpUpgrade,
	}

	// Check if a NodeOp already exists for this NodeOpUpgrade
	nodeOp := &kairosiov1alpha1.NodeOp{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      nodeOpUpgrade.Name,
		Namespace: nodeOpUpgrade.Namespace,
	}, nodeOp)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Compute which nodes are already at the target image so the NodeOp
			// (if any) is created targeting only the ones that actually need work.
			skippedNames, matchingCount, err := r.classifySkippedNodes(ctx, nodeOpUpgrade)
			if err != nil {
				log.Error(err, "Failed to classify nodes for image-repo skip")
				return ctrl.Result{}, err
			}

			// Record skipped nodes directly on the NodeOpUpgrade status. The
			// underlying NodeOp will not target them, so this is the only place
			// these entries will appear.
			r.recordSkippedNodes(nodeOpUpgrade, skippedNames)

			// Short-circuit when every matching node is already at the target image.
			// matchingCount > 0 avoids a no-op selector that legitimately matches nothing.
			if matchingCount > 0 && len(skippedNames) == matchingCount {
				log.Info("All targeted nodes are already at the target image; skipping NodeOp creation",
					"nodeOpUpgrade", nodeOpUpgrade.Name, "skipped", len(skippedNames))
				nodeOpUpgrade.Status.Phase = phaseCompleted
				nodeOpUpgrade.Status.Message = "All targeted nodes are already at the target image"
				nodeOpUpgrade.Status.LastUpdated = metav1.Now()
				if err := r.Status().Update(ctx, nodeOpUpgrade); err != nil {
					log.Error(err, "Failed to update NodeOpUpgrade status (all-skipped)")
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, nil
			}

			// NodeOp doesn't exist, create it
			log.Info("Creating NodeOp for NodeOpUpgrade", "nodeOp", nodeOpUpgrade.Name)
			if err := r.createNodeOp(ctx, nodeOpUpgrade, skippedNames); err != nil {
				log.Error(err, "Failed to create NodeOp")
				return ctrl.Result{}, err
			}

			// Update status to indicate NodeOp was created
			nodeOpUpgrade.Status.Phase = phaseInitializing
			nodeOpUpgrade.Status.NodeOpName = nodeOpUpgrade.Name
			nodeOpUpgrade.Status.Message = "NodeOp created"
			nodeOpUpgrade.Status.LastUpdated = metav1.Now()

			if err := r.Status().Update(ctx, nodeOpUpgrade); err != nil {
				log.Error(err, "Failed to update NodeOpUpgrade status")
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Second * 10}, nil
		}
		log.Error(err, "Failed to get NodeOp")
		return ctrl.Result{}, err
	}

	// NodeOp exists, update NodeOpUpgrade status from NodeOp status
	if err := r.updateStatusFromNodeOp(ctx, nodeOpUpgrade, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOpUpgrade status from NodeOp")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Second * 30}, nil
}

// createNodeOp creates a NodeOp resource based on the NodeOpUpgrade spec.
// skippedNames are node names that have already been recorded on the upgrade's
// status as already-at-image; the NodeOp's NodeSelector is augmented to exclude
// them so no Job is scheduled there.
func (r *NodeOpUpgradeReconciler) createNodeOp(ctx context.Context,
	nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade, skippedNames []string) error {
	// Generate the upgrade command based on the NodeOpUpgrade spec
	upgradeCommand := r.generateUpgradeCommand(nodeOpUpgrade)

	// Determine if we should reboot on success (true if UpgradeActive is true or nil)
	shouldReboot := getBool(nodeOpUpgrade.Spec.UpgradeActive, UpgradeActiveDefault)

	nodeOp := &kairosiov1alpha1.NodeOp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeOpUpgrade.Name,
			Namespace: nodeOpUpgrade.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nodeopupgrade-controller",
				labelKeyNodeOpUpgrade:          nodeOpUpgrade.Name,
			},
		},
		Spec: kairosiov1alpha1.NodeOpSpec{
			NodeSelector:     selectorExcludingNodes(nodeOpUpgrade.Spec.NodeSelector, skippedNames),
			Image:            nodeOpUpgrade.Spec.Image,
			ImagePullSecrets: nodeOpUpgrade.Spec.ImagePullSecrets,
			Concurrency:      nodeOpUpgrade.Spec.Concurrency,
			StopOnFailure:    nodeOpUpgrade.Spec.StopOnFailure,
			Command:          upgradeCommand,
			HostMountPath:    hostMountPath,
			Cordon:           asBool(true), // Always cordon for upgrades
			RebootOnSuccess:  &shouldReboot,
			DrainOptions: &kairosiov1alpha1.DrainOptions{
				Enabled: asBool(true), // Always drain for upgrades
			},
		},
	}

	// Set NodeOpUpgrade as the owner of the NodeOp
	if err := controllerutil.SetControllerReference(nodeOpUpgrade, nodeOp, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	return r.Create(ctx, nodeOp)
}

// generateUpgradeCommand creates the upgrade command based on the NodeOpUpgrade specification
func (r *NodeOpUpgradeReconciler) generateUpgradeCommand(nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) []string {
	script := `set -x -e

`

	// Add version check logic unless force is enabled
	forceUpgrade := getBool(nodeOpUpgrade.Spec.Force, UpgradeForceDefault)
	if !forceUpgrade {
		script += `get_version() {
    local file_path="$1"
    # shellcheck disable=SC1090
    . "$file_path"

    echo "${KAIROS_VERSION}-${KAIROS_SOFTWARE_VERSION_PREFIX}${KAIROS_SOFTWARE_VERSION}"
}

if [ -f "/etc/kairos-release" ]; then
      UPDATE_VERSION=$(get_version "/etc/kairos-release")
    else
      # shellcheck disable=SC1091
      UPDATE_VERSION=$(get_version "/etc/os-release" )
    fi

    if [ -f "` + hostMountPath + `/etc/kairos-release" ]; then
      # shellcheck disable=SC1091
      CURRENT_VERSION=$(get_version "` + hostMountPath + `/etc/kairos-release" )
    else
      # shellcheck disable=SC1091
      CURRENT_VERSION=$(get_version "` + hostMountPath + `/etc/os-release" )
    fi

    if [ "$CURRENT_VERSION" = "$UPDATE_VERSION" ]; then
      echo Up to date
      echo "Current version: ${CURRENT_VERSION}"
      echo "Update version: ${UPDATE_VERSION}"
      exit 0
    fi

`
	}

	// Add mount operations
	script += `mount --rbind ` + hostMountPath + `/dev /dev
mount --rbind ` + hostMountPath + `/run /run

`

	upgradeRecovery := getBool(nodeOpUpgrade.Spec.UpgradeRecovery, UpgradeRecoveryDefault)
	upgradeActive := getBool(nodeOpUpgrade.Spec.UpgradeActive, UpgradeActiveDefault)

	// Add upgrade logic based on spec
	if upgradeRecovery && upgradeActive {
		// Both recovery and active
		script += `# Upgrade recovery partition
kairos-agent upgrade --recovery --source dir:/

# Upgrade active partition
kairos-agent upgrade --source dir:/
exit 0
`
	} else if upgradeRecovery {
		// Recovery only
		script += `# Upgrade recovery partition only
kairos-agent upgrade --recovery --source dir:/
exit 0
`
	} else if upgradeActive {
		// Active only (default behavior)
		script += `# Upgrade active partition
kairos-agent upgrade --source dir:/
exit 0
`
	} else {
		// Neither specified - default to active
		script += `# Upgrade active partition (default)
kairos-agent upgrade --source dir:/
exit 0
`
	}

	// Build the complete command
	command := make([]string, 2, 3)
	command[0] = "/bin/sh" //nolint:goconst // path literal; not worth a constant
	command[1] = "-c"

	command = append(command, script)
	return command
}

// updateStatusFromNodeOp updates the NodeOpUpgrade status based on the NodeOp status
func (r *NodeOpUpgradeReconciler) updateStatusFromNodeOp(ctx context.Context,
	nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade, nodeOp *kairosiov1alpha1.NodeOp) error {
	// Copy status from NodeOp to NodeOpUpgrade
	statusChanged := false

	if nodeOpUpgrade.Status.Phase != nodeOp.Status.Phase {
		nodeOpUpgrade.Status.Phase = nodeOp.Status.Phase
		statusChanged = true
	}

	if nodeOpUpgrade.Status.NodeStatuses == nil {
		nodeOpUpgrade.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}

	// Update node statuses
	for nodeName, nodeStatus := range nodeOp.Status.NodeStatuses {
		if existingStatus, exists := nodeOpUpgrade.Status.NodeStatuses[nodeName]; !exists || existingStatus != nodeStatus {
			nodeOpUpgrade.Status.NodeStatuses[nodeName] = nodeStatus
			statusChanged = true
		}
	}

	// Update message based on phase
	newMessage := ""
	switch nodeOp.Status.Phase {
	case phaseRunning:
		newMessage = "Upgrade operation is running"
	case phaseCompleted:
		newMessage = "Upgrade operation completed successfully"
	case phaseFailed:
		newMessage = "Upgrade operation failed"
	default:
		newMessage = "Upgrade operation is pending"
	}

	if nodeOpUpgrade.Status.Message != newMessage {
		nodeOpUpgrade.Status.Message = newMessage
		statusChanged = true
	}

	if statusChanged {
		nodeOpUpgrade.Status.LastUpdated = metav1.Now()
		return r.Status().Update(ctx, nodeOpUpgrade)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeOpUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kairosiov1alpha1.NodeOpUpgrade{}).
		Watches(
			&kairosiov1alpha1.NodeOp{},
			handler.EnqueueRequestsFromMapFunc(r.findNodeOpUpgradesForNodeOp),
		).
		Named("nodeopupgrade").
		Complete(r)
}

// findNodeOpUpgradesForNodeOp finds NodeOpUpgrade resources that own the given NodeOp
func (r *NodeOpUpgradeReconciler) findNodeOpUpgradesForNodeOp(_ context.Context,
	obj client.Object) []reconcile.Request {
	nodeOp := obj.(*kairosiov1alpha1.NodeOp)

	// Look for the owning NodeOpUpgrade using labels
	if nodeOpUpgradeName, exists := nodeOp.Labels[labelKeyNodeOpUpgrade]; exists {
		return []reconcile.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      nodeOpUpgradeName,
					Namespace: nodeOp.Namespace,
				},
			},
		}
	}

	return []reconcile.Request{}
}

// classifySkippedNodes returns the names of nodes that match the upgrade's
// NodeSelector and whose kairos.io/image-repo annotation already equals
// Spec.Image, alongside the total number of matching nodes. The caller can
// short-circuit NodeOp creation when len(skipped) == matchingCount > 0.
//
// Honors Spec.Force: when true (or Spec.Image is empty) the function returns
// (nil, 0, nil) so the upgrade runs on every targeted node.
func (r *NodeOpUpgradeReconciler) classifySkippedNodes(ctx context.Context,
	nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) (skipped []string, matchingCount int, err error) {
	if getBool(nodeOpUpgrade.Spec.Force, UpgradeForceDefault) || nodeOpUpgrade.Spec.Image == "" {
		return nil, 0, nil
	}

	matching, err := r.listMatchingNodes(ctx, nodeOpUpgrade.Spec.NodeSelector)
	if err != nil {
		return nil, 0, err
	}

	for _, node := range matching {
		if node.Annotations[nodeImageRepoAnnotation] == nodeOpUpgrade.Spec.Image {
			skipped = append(skipped, node.Name)
		}
	}
	return skipped, len(matching), nil
}

// listMatchingNodes returns all cluster nodes that match the given selector.
// A nil selector matches every node, mirroring NodeOp's targeting semantics.
func (r *NodeOpUpgradeReconciler) listMatchingNodes(ctx context.Context,
	selector *metav1.LabelSelector) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := r.List(ctx, nodeList); err != nil {
		return nil, err
	}

	if selector == nil {
		return nodeList.Items, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}

	var out []corev1.Node
	for _, node := range nodeList.Items {
		if sel.Matches(labels.Set(node.Labels)) {
			out = append(out, node)
		}
	}
	return out, nil
}

// recordSkippedNodes pre-populates the NodeOpUpgrade status with a Completed
// entry for each skipped node. Idempotent: an existing entry for the same node
// is left untouched.
func (r *NodeOpUpgradeReconciler) recordSkippedNodes(nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	if nodeOpUpgrade.Status.NodeStatuses == nil {
		nodeOpUpgrade.Status.NodeStatuses = make(map[string]kairosiov1alpha1.NodeStatus)
	}
	for _, name := range skipped {
		if _, exists := nodeOpUpgrade.Status.NodeStatuses[name]; exists {
			continue
		}
		nodeOpUpgrade.Status.NodeStatuses[name] = kairosiov1alpha1.NodeStatus{
			Phase:        phaseCompleted,
			Message:      fmt.Sprintf("Skipped: node already at image %s", nodeOpUpgrade.Spec.Image),
			RebootStatus: rebootStatusNotRequested,
			LastUpdated:  metav1.Now(),
		}
	}
}

// selectorExcludingNodes returns a copy of base augmented with a
// kubernetes.io/hostname NotIn matchExpression that excludes the named nodes.
// If exclude is empty the original selector (or nil) is returned unchanged.
func selectorExcludingNodes(base *metav1.LabelSelector, exclude []string) *metav1.LabelSelector {
	if len(exclude) == 0 {
		return base
	}
	var out *metav1.LabelSelector
	if base != nil {
		out = base.DeepCopy()
	} else {
		out = &metav1.LabelSelector{}
	}
	out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
		Key:      hostnameLabel,
		Operator: metav1.LabelSelectorOpNotIn,
		Values:   append([]string(nil), exclude...),
	})
	return out
}
