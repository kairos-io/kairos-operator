package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata" // The runtime image does not include a time zone database.

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	operatorv1alpha1 "github.com/kairos-io/kairos-operator/api/v1alpha1"
)

const conditionUpgradeAvailable = "UpgradeAvailable"

// NodeOpUpgradeScheduleReconciler reconciles a NodeOpUpgradeSchedule object
type NodeOpUpgradeScheduleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// APIReader avoids stale cached child state when preventing overlapping upgrades.
	APIReader client.Reader
	// now allows tests to advance time without sleeping.
	now func() time.Time
	// registryTransport allows tests to use a local registry with a test certificate.
	registryTransport http.RoundTripper
}

// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgradeschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgradeschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgradeschedules/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get
// +kubebuilder:rbac:groups=operator.kairos.io,resources=nodeopupgrades,verbs=get;list;watch;create

// Reconcile checks for newer matching image tags on each enabled cron execution.
// Each execution creates at most one upgrade, and active or failed upgrades block new executions.
func (r *NodeOpUpgradeScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	schedule := &operatorv1alpha1.NodeOpUpgradeSchedule{}
	if err := r.Get(ctx, req.NamespacedName, schedule); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !schedule.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	original := schedule.DeepCopy()
	now := r.currentTime()
	if schedule.Status.ObservedGeneration != schedule.Generation {
		schedule.Status.AvailableImage = ""
		meta.RemoveStatusCondition(&schedule.Status.Conditions, conditionUpgradeAvailable)
	}
	active, err := r.syncScheduledUpgrade(ctx, schedule)
	if err != nil {
		if statusErr := r.updateScheduleStatus(ctx, original, schedule, now,
			metav1.ConditionFalse, "UpgradeTrackingFailed", err.Error()); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}
	if active != nil {
		return r.waitForScheduledUpgrade(ctx, original, schedule, active)
	}
	if schedule.Spec.Suspend {
		schedule.Status.NextScheduledTime = nil
		return ctrl.Result{}, r.updateScheduleStatus(ctx, original, schedule, now,
			metav1.ConditionFalse, "Suspended", "Cron executions are suspended")
	}

	cronSchedule, err := parseUpgradeSchedule(schedule.Spec.Schedule, schedule.Spec.TimeZone)
	if err != nil {
		schedule.Status.NextScheduledTime = nil
		return ctrl.Result{}, r.updateScheduleStatus(ctx, original, schedule, now,
			metav1.ConditionFalse, "InvalidSchedule", err.Error())
	}
	imagePolicy, err := parseScheduledImagePolicy(schedule)
	if err != nil {
		schedule.Status.NextScheduledTime = nil
		return ctrl.Result{}, r.updateScheduleStatus(ctx, original, schedule, now,
			metav1.ConditionFalse, "InvalidImagePolicy", err.Error())
	}
	next := cronSchedule.Next(now)
	if next.IsZero() {
		schedule.Status.NextScheduledTime = nil
		return ctrl.Result{}, r.updateScheduleStatus(ctx, original, schedule, now,
			metav1.ConditionFalse, "InvalidSchedule", "Cron expression has no future execution")
	}

	// A spec change starts a fresh wait. An old queued wake-up must not execute
	// a previous schedule after its expression, time zone, or suspend flag changes.
	if schedule.Status.ObservedGeneration != schedule.Generation || schedule.Status.NextScheduledTime == nil {
		schedule.Status.NextScheduledTime = &metav1.Time{Time: next}
	} else if !schedule.Status.NextScheduledTime.After(now) {
		// Only process one overdue execution, even after extended downtime.
		if imagePolicy != nil {
			if err := r.checkScheduledImage(ctx, schedule, imagePolicy); err != nil {
				schedule.Status.AvailableImage = ""
				meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
					Type: conditionUpgradeAvailable, Status: metav1.ConditionUnknown,
					Reason: "ImageCheckFailed", Message: err.Error(),
					ObservedGeneration: schedule.Generation, LastTransitionTime: metav1.NewTime(now),
				})
				if statusErr := r.updateScheduleStatus(ctx, original, schedule, now,
					metav1.ConditionFalse, "ImageCheckFailed", err.Error()); statusErr != nil {
					return ctrl.Result{}, statusErr
				}
				// Keep this execution pending for the work queue's retry with backoff.
				return ctrl.Result{}, err
			}
		}
		image := schedule.Status.AvailableImage
		if imagePolicy == nil {
			image = schedule.Spec.UpgradeTemplate.Image
		}
		if image != "" {
			upgrade, err := r.createScheduledUpgrade(ctx, schedule, image)
			if err != nil {
				if statusErr := r.updateScheduleStatus(ctx, original, schedule, r.currentTime(),
					metav1.ConditionFalse, "UpgradeCreateFailed", err.Error()); statusErr != nil {
					return ctrl.Result{}, statusErr
				}
				return ctrl.Result{}, err
			}
			return r.waitForScheduledUpgrade(ctx, original, schedule, upgrade)
		}
		now = r.currentTime()
		next = cronSchedule.Next(now)
		schedule.Status.LastScheduledTime = schedule.Status.NextScheduledTime.DeepCopy()
		schedule.Status.NextScheduledTime = &metav1.Time{Time: next}
		logf.FromContext(ctx).Info("Cron execution is due", "scheduledTime", schedule.Status.LastScheduledTime)
	}

	if err := r.updateScheduleStatus(ctx, original, schedule, now,
		metav1.ConditionTrue, "WaitingForSchedule", "Waiting for the next cron execution"); err != nil {
		return ctrl.Result{}, err
	}
	delay := schedule.Status.NextScheduledTime.Sub(r.currentTime())
	if delay <= 0 {
		delay = time.Millisecond
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

func parseUpgradeSchedule(expression, timeZone string) (cron.Schedule, error) {
	// Only five-field expressions are supported. In particular, time zones
	// must come from spec.timeZone rather than an inline TZ or CRON_TZ prefix.
	if len(strings.Fields(expression)) != 5 || strings.Contains(expression, "TZ=") {
		return nil, fmt.Errorf("schedule must be a five-field cron expression without an inline time zone")
	}
	if timeZone == "" {
		timeZone = "UTC"
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return nil, fmt.Errorf("invalid timeZone %q: %w", timeZone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(expression)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}
	schedule.(*cron.SpecSchedule).Location = location
	return schedule, nil
}

func (r *NodeOpUpgradeScheduleReconciler) updateScheduleStatus(ctx context.Context,
	original, schedule *operatorv1alpha1.NodeOpUpgradeSchedule, now time.Time,
	status metav1.ConditionStatus, reason, message string) error {
	schedule.Status.ObservedGeneration = schedule.Generation
	meta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: message,
		ObservedGeneration: schedule.Generation, LastTransitionTime: metav1.NewTime(now),
	})
	if equality.Semantic.DeepEqual(original.Status, schedule.Status) {
		return nil
	}
	return r.Status().Patch(ctx, schedule, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
}

func (r *NodeOpUpgradeScheduleReconciler) currentTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeOpUpgradeScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1alpha1.NodeOpUpgradeSchedule{}).
		Owns(&operatorv1alpha1.NodeOpUpgrade{}).
		Named("nodeopupgradeschedule").
		Complete(r)
}

func (r *NodeOpUpgradeScheduleReconciler) waitForScheduledUpgrade(ctx context.Context,
	original, schedule *operatorv1alpha1.NodeOpUpgradeSchedule, upgrade *operatorv1alpha1.NodeOpUpgrade) (ctrl.Result, error) {
	status, reason := metav1.ConditionTrue, "UpgradeRunning"
	message := "Waiting for upgrade " + upgrade.Name
	if upgrade.Status.Phase == phaseFailed {
		status, reason = metav1.ConditionFalse, "UpgradeFailed"
		message = "Upgrade " + upgrade.Name + " failed; address it before allowing another execution"
	}
	return ctrl.Result{RequeueAfter: scheduleUpgradeRequeue}, r.updateScheduleStatus(ctx, original, schedule,
		r.currentTime(), status, reason, message)
}
