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
	kindNodeOpUpgrade = "NodeOpUpgrade"
	phaseInitializing = "Initializing"
	hostMountPath     = "/host"
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

	// Check if a NodeOp already exists for this NodeOpUpgrade
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

	// NodeOp exists, update NodeOpUpgrade status from NodeOp status
	if err := r.updateStatusFromNodeOp(ctx, nodeOpUpgrade, nodeOp); err != nil {
		log.Error(err, "Failed to update NodeOpUpgrade status from NodeOp")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Second * 30}, nil
}

// createNodeOp creates a NodeOp resource based on the NodeOpUpgrade spec
func (r *NodeOpUpgradeReconciler) createNodeOp(ctx context.Context, nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade) error {
	// Generate the upgrade command based on the NodeOpUpgrade spec
	upgradeCommand := r.generateUpgradeCommand(nodeOpUpgrade)

	nodeOp := &kairosiov1alpha1.NodeOp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeOpUpgrade.Name,
			Namespace: nodeOpUpgrade.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nodeopupgrade-controller",
				"nodeopupgrade.kairos.io/name": nodeOpUpgrade.Name,
			},
		},
		Spec: kairosiov1alpha1.NodeOpSpec{
			TargetNodes:     nodeOpUpgrade.Spec.TargetNodes,
			Image:           nodeOpUpgrade.Spec.Image,
			Concurrency:     nodeOpUpgrade.Spec.Concurrency,
			StopOnFailure:   nodeOpUpgrade.Spec.StopOnFailure,
			Command:         upgradeCommand,
			HostMountPath:   hostMountPath,
			Cordon:          true,
			RebootOnSuccess: nodeOpUpgrade.Spec.UpgradeActive,
			DrainOptions: &kairosiov1alpha1.DrainOptions{
				Enabled: true,
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
	script := `#!/bin/bash
set -x -e

`

	// Add version check logic unless force is enabled
	if !nodeOpUpgrade.Spec.Force {
		script += `get_version() {
    local file_path="$1"
    # shellcheck disable=SC1090
    source "$file_path"

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

    if [ "$CURRENT_VERSION" == "$UPDATE_VERSION" ]; then
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

	// Add upgrade logic based on spec
	if nodeOpUpgrade.Spec.UpgradeRecovery && nodeOpUpgrade.Spec.UpgradeActive {
		// Both recovery and active
		script += `# Upgrade recovery partition
kairos-agent upgrade --recovery --source dir:/

# Upgrade active partition
kairos-agent upgrade --source dir:/
exit 0
`
	} else if nodeOpUpgrade.Spec.UpgradeRecovery {
		// Recovery only
		script += `# Upgrade recovery partition only
kairos-agent upgrade --recovery --source dir:/
exit 0
`
	} else if nodeOpUpgrade.Spec.UpgradeActive {
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
	command := []string{"/bin/sh", "-c"}

	command = append(command, script)
	return command
}

// updateStatusFromNodeOp updates the NodeOpUpgrade status based on the NodeOp status
func (r *NodeOpUpgradeReconciler) updateStatusFromNodeOp(ctx context.Context, nodeOpUpgrade *kairosiov1alpha1.NodeOpUpgrade, nodeOp *kairosiov1alpha1.NodeOp) error {
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
func (r *NodeOpUpgradeReconciler) findNodeOpUpgradesForNodeOp(ctx context.Context, obj client.Object) []reconcile.Request {
	nodeOp := obj.(*kairosiov1alpha1.NodeOp)

	// Look for the owning NodeOpUpgrade using labels
	if nodeOpUpgradeName, exists := nodeOp.Labels["nodeopupgrade.kairos.io/name"]; exists {
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
