package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
	"github.com/kairos-io/kairos-operator/internal/utils"
)

const (
	scheduleUIDLabel       = "operator.kairos.io/upgrade-schedule-uid"
	scheduledAtAnnotation  = "operator.kairos.io/scheduled-at"
	scheduleUpgradeRequeue = 30 * time.Second
)

func scheduledUpgradeName(schedule *operatorv1alpha1.NodeOpUpgradeSchedule, scheduledAt time.Time) string {
	return utils.TruncateNameWithHash(
		fmt.Sprintf("%s-%s-%d", schedule.Name, schedule.UID, scheduledAt.Unix()), utils.KubernetesNameLengthLimit)
}

// syncScheduledUpgrade recovers children created before a failed status write,
// and tracks an active upgrade even while the schedule is suspended or edited.
func (r *NodeOpUpgradeScheduleReconciler) syncScheduledUpgrade(ctx context.Context,
	schedule *operatorv1alpha1.NodeOpUpgradeSchedule) (*operatorv1alpha1.NodeOpUpgrade, error) {
	var upgrade *operatorv1alpha1.NodeOpUpgrade
	if schedule.Status.ActiveUpgradeRef != nil {
		upgrade = &operatorv1alpha1.NodeOpUpgrade{}
		if err := r.upgradeReader().Get(ctx, types.NamespacedName{
			Namespace: schedule.Namespace, Name: schedule.Status.ActiveUpgradeRef.Name,
		}, upgrade); err != nil {
			return nil, fmt.Errorf("read active upgrade: %w; clear activeUpgradeRef after addressing a missing upgrade", err)
		}
	} else {
		upgrades := &operatorv1alpha1.NodeOpUpgradeList{}
		if err := r.upgradeReader().List(ctx, upgrades, client.InNamespace(schedule.Namespace),
			client.MatchingLabels{scheduleUIDLabel: string(schedule.UID)}); err != nil {
			return nil, fmt.Errorf("list scheduled upgrades: %w", err)
		}
		// Prefer the latest execution when recovering status after a restart.
		sort.Slice(upgrades.Items, func(i, j int) bool {
			return upgrades.Items[i].Annotations[scheduledAtAnnotation] > upgrades.Items[j].Annotations[scheduledAtAnnotation]
		})
		for i := range upgrades.Items {
			candidate := &upgrades.Items[i]
			if !metav1.IsControlledBy(candidate, schedule) {
				continue
			}
			if candidate.Status.Phase != phaseCompleted {
				upgrade = candidate
				break
			}
			if upgrade == nil && (schedule.Status.LastScheduledTime == nil ||
				candidate.Annotations[scheduledAtAnnotation] > schedule.Status.LastScheduledTime.UTC().Format(time.RFC3339)) {
				upgrade = candidate
			}
		}
	}
	if upgrade == nil {
		return nil, nil
	}
	if !metav1.IsControlledBy(upgrade, schedule) {
		return nil, fmt.Errorf("upgrade %q is not controlled by this schedule", upgrade.Name)
	}
	if err := recordScheduledUpgrade(schedule, upgrade); err != nil {
		return nil, err
	}
	if upgrade.Status.Phase == phaseCompleted && upgrade.DeletionTimestamp.IsZero() {
		schedule.Status.LastSuccessfulImage = upgrade.Spec.Image
		schedule.Status.ActiveUpgradeRef = nil
		schedule.Status.AvailableImage = ""
		meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
			Type: conditionUpgradeAvailable, Status: metav1.ConditionFalse, Reason: "UpgradeSucceeded",
			Message: "Upgrade completed successfully", ObservedGeneration: schedule.Generation,
			LastTransitionTime: metav1.NewTime(r.currentTime()),
		})
		return nil, nil
	}
	return upgrade, nil
}

func recordScheduledUpgrade(schedule *operatorv1alpha1.NodeOpUpgradeSchedule, upgrade *operatorv1alpha1.NodeOpUpgrade) error {
	scheduledAt, err := time.Parse(time.RFC3339, upgrade.Annotations[scheduledAtAnnotation])
	if err != nil {
		return fmt.Errorf("upgrade %q has an invalid scheduled-at annotation: %w", upgrade.Name, err)
	}
	schedule.Status.ActiveUpgradeRef = &corev1.LocalObjectReference{Name: upgrade.Name}
	schedule.Status.LastAttemptedImage = upgrade.Spec.Image
	schedule.Status.LastScheduledTime = &metav1.Time{Time: scheduledAt}
	// Wait for the active upgrade before calculating the next execution.
	schedule.Status.NextScheduledTime = nil
	return nil
}

func (r *NodeOpUpgradeScheduleReconciler) createScheduledUpgrade(ctx context.Context,
	schedule *operatorv1alpha1.NodeOpUpgradeSchedule, image string) (*operatorv1alpha1.NodeOpUpgrade, error) {
	// Image discovery may take time. Check that this execution is still authorized
	// by the latest schedule before creating a resource that can upgrade nodes.
	latest := &operatorv1alpha1.NodeOpUpgradeSchedule{}
	if err := r.upgradeReader().Get(ctx, client.ObjectKeyFromObject(schedule), latest); err != nil {
		return nil, err
	}
	if latest.UID != schedule.UID || latest.Generation != schedule.Generation ||
		!latest.DeletionTimestamp.IsZero() || latest.Spec.Suspend {
		return nil, fmt.Errorf("schedule changed or stopped before upgrade creation; reconcile its latest state")
	}
	if latest.Status.ActiveUpgradeRef != nil || latest.Status.NextScheduledTime == nil ||
		!latest.Status.NextScheduledTime.Time.Equal(schedule.Status.NextScheduledTime.Time) {
		return nil, fmt.Errorf("schedule execution changed before upgrade creation; reconcile its latest state")
	}
	scheduledAt := schedule.Status.NextScheduledTime.Time
	upgrade := &operatorv1alpha1.NodeOpUpgrade{
		ObjectMeta: metav1.ObjectMeta{
			Name: scheduledUpgradeName(schedule, scheduledAt), Namespace: schedule.Namespace,
			Labels:      map[string]string{scheduleUIDLabel: string(schedule.UID)},
			Annotations: map[string]string{scheduledAtAnnotation: scheduledAt.UTC().Format(time.RFC3339)},
		},
		Spec: *schedule.Spec.UpgradeTemplate.DeepCopy(),
	}
	upgrade.Spec.Image = image
	if policy := schedule.Spec.ImagePolicy; policy != nil {
		seen := make(map[string]bool, len(upgrade.Spec.ImagePullSecrets))
		for _, ref := range upgrade.Spec.ImagePullSecrets {
			seen[ref.Name] = true
		}
		for _, ref := range policy.ImagePullSecrets {
			if !seen[ref.Name] {
				upgrade.Spec.ImagePullSecrets = append(upgrade.Spec.ImagePullSecrets, ref)
				seen[ref.Name] = true
			}
		}
	}
	if err := controllerutil.SetControllerReference(schedule, upgrade, r.Client.Scheme()); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, upgrade); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create scheduled upgrade: %w", err)
		}
		// A previous create may have succeeded even if its response or the
		// schedule's status update was lost. Reuse that execution's resource.
		if err := r.upgradeReader().Get(ctx, client.ObjectKeyFromObject(upgrade), upgrade); err != nil {
			return nil, err
		}
		if !metav1.IsControlledBy(upgrade, schedule) ||
			upgrade.Annotations[scheduledAtAnnotation] != scheduledAt.UTC().Format(time.RFC3339) {
			return nil, fmt.Errorf("upgrade name %q is already used by another execution", upgrade.Name)
		}
	}
	return upgrade, recordScheduledUpgrade(schedule, upgrade)
}

func (r *NodeOpUpgradeScheduleReconciler) upgradeReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
