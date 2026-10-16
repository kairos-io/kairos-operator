package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kairosiov1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	kindNodeOpUpgrade     = "NodeOpUpgrade"
	phaseInitializing     = "Initializing"
	labelKeyNodeOpUpgrade = "nodeopupgrade.kairos.io/name"
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

	// Check if a NodeOp already exists for this NodeOpUpgrade. The NodeOp is
	// named after the NodeOpUpgrade, and NodeOp is a user-facing API of its
	// own, so this key can also hold a NodeOp nobody here created.
	nodeOp := &kairosiov1alpha1.NodeOp{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      nodeOpUpgrade.Name,
		Namespace: nodeOpUpgrade.Namespace,
	}, nodeOp)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// NodeOp doesn't exist, create it
			log.Info("Creating NodeOp for NodeOpUpgrade", "nodeOp", nodeOpUpgrade.Name)
			if err := r.createNodeOp(ctx, nodeOpUpgrade); err != nil {
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

	// A NodeOp is ours only when we control it. Adopting a NodeOp that belongs
	// to someone else would report its progress as this upgrade's own and, worse,
	// leave the upgrade itself unscheduled for good, because the key we would
	// create it under is taken.
	if !metav1.IsControlledBy(nodeOp, nodeOpUpgrade) {
		return r.reportNodeOpConflict(ctx, nodeOpUpgrade)
	}

	// NodeOp exists, update NodeOpUpgrade status from NodeOp status
	if err := r.updateStatusFromNodeOp(ctx, nodeOpUpgrade, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOpUpgrade status from NodeOp")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Second * 30}, nil
}

// reportNodeOpConflict records that the NodeOp name this upgrade needs is
// already taken by a NodeOp it does not own, and keeps polling so the upgrade
// starts on its own once that NodeOp is gone.
func (r *NodeOpUpgradeReconciler) reportNodeOpConflict(ctx context.Context,
	nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	message := fmt.Sprintf(
		"NodeOp %s/%s already exists and is not managed by this NodeOpUpgrade; "+
			"delete or rename it for the upgrade to start",
		nodeOpUpgrade.Namespace, nodeOpUpgrade.Name)

	log.Info("Refusing to adopt a NodeOp this NodeOpUpgrade does not own", "nodeOp", nodeOpUpgrade.Name)

	if nodeOpUpgrade.Status.Phase != phaseFailed || nodeOpUpgrade.Status.Message != message {
		nodeOpUpgrade.Status.Phase = phaseFailed
		nodeOpUpgrade.Status.Message = message
		nodeOpUpgrade.Status.LastUpdated = metav1.Now()
		if err := r.Status().Update(ctx, nodeOpUpgrade); err != nil {
			log.Error(err, "Failed to update NodeOpUpgrade status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * 30}, nil
}

// createNodeOp creates a NodeOp resource based on the NodeOpUpgrade spec
func (r *NodeOpUpgradeReconciler) createNodeOp(ctx context.Context,
	nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) error {
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
			NodeSelector:      nodeOpUpgrade.Spec.NodeSelector,
			Image:             nodeOpUpgrade.Spec.Image,
			ImagePullSecrets:  nodeOpUpgrade.Spec.ImagePullSecrets,
			Concurrency:       nodeOpUpgrade.Spec.Concurrency,
			StopOnFailure:     nodeOpUpgrade.Spec.StopOnFailure,
			Command:           upgradeCommand,
			HostMountPath:     defaultHostMountPath,
			Cordon:            asBool(true), // Always cordon for upgrades
			UncordonOnFailure: nodeOpUpgrade.Spec.UncordonOnFailure,
			RebootOnSuccess:   &shouldReboot,
			DrainOptions: &kairosiov1alpha1.DrainOptions{
				Enabled: asBool(true), // Always drain for upgrades
			},
			Preflight:          buildUpgradePreflight(nodeOpUpgrade),
			Resources:          nodeOpUpgrade.Spec.Resources,
			PreflightResources: nodeOpUpgrade.Spec.PreflightResources,
			RebootResources:    nodeOpUpgrade.Spec.RebootResources,
		},
	}

	// Set NodeOpUpgrade as the owner of the NodeOp
	if err := controllerutil.SetControllerReference(nodeOpUpgrade, nodeOp, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	return r.Create(ctx, nodeOp)
}

// buildUpgradePreflight returns the PreflightSpec the NodeOp should run before
// cordoning/draining each target node. Returns nil when Force=true so the
// upgrade runs on every node regardless of the current OS version, and when
// the recovery partition is upgraded: the preflight only knows the host's
// active version, so it would skip nodes whose recovery partition still
// needs the new image.
//
// The preflight script reads /etc/kairos-release inside the upgrade image and
// compares the resulting version triple against the host's /etc/kairos-release
// (mounted read-only at Spec.defaultHostMountPath). When the versions match it writes
// a one-line reason to /dev/termination-log so the NodeOp controller records
// the node as Completed (skipped). When the versions differ it stays silent
// and exits 0, which the controller treats as "proceed".
func buildUpgradePreflight(nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) *kairosiov1alpha1.PreflightSpec {
	if getBool(nodeOpUpgrade.Spec.Force, UpgradeForceDefault) ||
		getBool(nodeOpUpgrade.Spec.UpgradeRecovery, UpgradeRecoveryDefault) {
		return nil
	}
	deadline := int32(120)
	return &kairosiov1alpha1.PreflightSpec{
		Command:               []string{"/bin/sh", "-c", upgradePreflightScript()}, //nolint:goconst // path literal; not worth a constant
		ActiveDeadlineSeconds: &deadline,
	}
}

// versionDetectionSnippet is the shell fragment that resolves the target and
// the host Kairos version triple into TARGET and CURRENT. Both the preflight
// script and the upgrade script itself use it, so the two always agree on
// what "same version" means.
//
// get_version returns an empty string when the file is missing or when
// KAIROS_VERSION is unset (e.g. when falling back to /etc/os-release on a
// non-Kairos image or an older OS that doesn't carry KAIROS_* variables), so
// an unknown version is always distinguishable from a known one. Callers must
// therefore require BOTH CURRENT and TARGET to be non-empty before declaring
// them equal — otherwise two unknowns compare equal and the upgrade is
// wrongly reported as unnecessary. Unknown on either side means "proceed".
//
// File paths are read from environment variables (defaults match what the Pod
// sees in production), so tests can drive either script with synthetic files
// in a temp directory.
func versionDetectionSnippet() string {
	return `: "${TARGET_KAIROS_RELEASE:=/etc/kairos-release}"
: "${TARGET_OS_RELEASE:=/etc/os-release}"
: "${HOST_KAIROS_RELEASE:=` + defaultHostMountPath + `/etc/kairos-release}"
: "${HOST_OS_RELEASE:=` + defaultHostMountPath + `/etc/os-release}"

get_version() {
    local file_path="$1"
    if [ ! -f "$file_path" ]; then
        echo ""
        return 0
    fi
    # shellcheck disable=SC1090
    . "$file_path"
    if [ -z "${KAIROS_VERSION}" ]; then
        echo ""
        return 0
    fi
    echo "${KAIROS_VERSION}-${KAIROS_SOFTWARE_VERSION_PREFIX}${KAIROS_SOFTWARE_VERSION}"
}

TARGET=$(get_version "${TARGET_KAIROS_RELEASE}")
if [ -z "${TARGET}" ]; then
    TARGET=$(get_version "${TARGET_OS_RELEASE}")
fi

CURRENT=$(get_version "${HOST_KAIROS_RELEASE}")
if [ -z "${CURRENT}" ]; then
    CURRENT=$(get_version "${HOST_OS_RELEASE}")
fi

echo "Host: ${CURRENT:-unknown}, Target: ${TARGET:-unknown}"
`
}

// upgradePreflightScript is the shell snippet run inside each preflight Pod
// when this is a NodeOpUpgrade. Output convention: a non-empty
// /dev/termination-log means "skip this node with the given reason"; an empty
// termination log + exit 0 means "proceed".
func upgradePreflightScript() string {
	return `set -e
: "${TERMINATION_LOG:=/dev/termination-log}"

` + versionDetectionSnippet() + `
if [ -n "${CURRENT}" ] && [ -n "${TARGET}" ] && [ "${CURRENT}" = "${TARGET}" ]; then
    echo "node is already at ${TARGET}" > "${TERMINATION_LOG}"
fi
exit 0
`
}

// buildExcludePathFlags returns a shell fragment of `--exclude-path` flags to
// append after `--source dir:/`. The always-excluded paths come first, then
// any user-configured paths. Each path is single-quoted so spaces and shell
// metacharacters stay literal; embedded single quotes are handled with the
// POSIX close-escape-reopen idiom (close the quote, emit an escaped literal
// single quote, reopen the quote), which is the raw-string literal used in
// the ReplaceAll call below.
func buildExcludePathFlags(userPaths []string) string {
	all := make([]string, 0, len(upgradeAlwaysExcludePaths)+len(userPaths))
	all = append(all, upgradeAlwaysExcludePaths...)
	all = append(all, userPaths...)

	var b strings.Builder
	for _, p := range all {
		b.WriteString(" --exclude-path '")
		b.WriteString(strings.ReplaceAll(p, "'", `'\''`))
		b.WriteString("'")
	}
	return b.String()
}

// generateUpgradeCommand creates the upgrade command based on the NodeOpUpgrade specification
func (r *NodeOpUpgradeReconciler) generateUpgradeCommand(nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) []string {
	excludes := buildExcludePathFlags(nodeOpUpgrade.Spec.ExcludePaths)

	script := `set -x -e

`

	// Add version check logic unless force is enabled. This uses the same
	// version detection as the preflight, so the two cannot disagree: an
	// unknown version on either side must not be read as "already up to
	// date".
	//
	// The versions come from the host's active system, so they say nothing
	// about the recovery partition. Skip the check when recovery is part of
	// the upgrade, or it would skip work that still has to happen.
	forceUpgrade := getBool(nodeOpUpgrade.Spec.Force, UpgradeForceDefault)
	upgradeRecovery := getBool(nodeOpUpgrade.Spec.UpgradeRecovery, UpgradeRecoveryDefault)
	upgradeActive := getBool(nodeOpUpgrade.Spec.UpgradeActive, UpgradeActiveDefault)
	if !forceUpgrade && !upgradeRecovery {
		script += versionDetectionSnippet() + `
if [ -n "${CURRENT}" ] && [ -n "${TARGET}" ] && [ "${CURRENT}" = "${TARGET}" ]; then
  echo Up to date
  echo "Current version: ${CURRENT}"
  echo "Update version: ${TARGET}"
  exit 0
fi

`
	}

	// Add mount operations
	script += `mount --rbind ` + defaultHostMountPath + `/dev /dev
mount --rbind ` + defaultHostMountPath + `/run /run

`

	// --debug is a global flag on the kairos-agent CLI, so it must precede the
	// upgrade subcommand.
	agent := "kairos-agent"
	if getBool(nodeOpUpgrade.Spec.Debug, UpgradeDebugDefault) {
		agent += " --debug"
	}

	// Add upgrade logic based on spec
	if upgradeRecovery && upgradeActive {
		// Both recovery and active
		script += `# Upgrade recovery partition
` + agent + ` upgrade --recovery --source dir:/` + excludes + `

# Upgrade active partition
` + agent + ` upgrade --source dir:/` + excludes + `
exit 0
`
	} else if upgradeRecovery {
		// Recovery only
		script += `# Upgrade recovery partition only
` + agent + ` upgrade --recovery --source dir:/` + excludes + `
exit 0
`
	} else if upgradeActive {
		// Active only (default behavior)
		script += `# Upgrade active partition
` + agent + ` upgrade --source dir:/` + excludes + `
exit 0
`
	} else {
		// Neither specified - default to active
		script += `# Upgrade active partition (default)
` + agent + ` upgrade --source dir:/` + excludes + `
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
